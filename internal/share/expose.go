package share

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
)

// Exposing germ share's built-in relay to the internet, for free and
// without accounts: directly, by asking the router to forward a port
// (UPnP), or else through a Cloudflare quick tunnel.

// Exposure is a public address for a local port.
type Exposure struct {
	URL  *url.URL // base URL guests use
	Mode string   // "direct" or "tunnel"
	stop func()
}

// Close removes the port mapping or stops the tunnel.
func (e *Exposure) Close() {
	if e != nil && e.stop != nil {
		e.stop()
	}
}

// ExposeOptions configures Expose.
type ExposeOptions struct {
	Mode string // "auto" (direct, else tunnel), "direct", or "tunnel"
	// PublicAddress, for direct mode, is a host name or IP that already
	// forwards to this machine (a manual port forward); skips UPnP.
	PublicAddress string
	Log           func(format string, args ...any)
}

// Expose makes localPort reachable from the internet.
func Expose(ctx context.Context, localPort int, o ExposeOptions) (*Exposure, error) {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	var directErr error
	if o.Mode != "tunnel" {
		e, err := exposeDirect(ctx, localPort, o)
		if err == nil {
			return e, nil
		}
		directErr = err
		if o.Mode == "direct" {
			return nil, err
		}
		o.Log("direct connection isn't possible (%v); using a Cloudflare quick tunnel", err)
	}
	e, err := exposeTunnel(ctx, localPort, o)
	if err != nil && directErr != nil {
		return nil, fmt.Errorf("couldn't open a direct connection (%v) or a tunnel (%w)", directErr, err)
	}
	return e, err
}

// ---- direct (UPnP) ------------------------------------------------------------

type portMapper interface {
	AddPortMapping(remoteHost string, extPort uint16, proto string, intPort uint16, intClient string, enabled bool, desc string, lease uint32) error
	DeletePortMapping(remoteHost string, extPort uint16, proto string) error
	GetExternalIPAddress() (string, error)
}

func exposeDirect(ctx context.Context, localPort int, o ExposeOptions) (*Exposure, error) {
	if o.PublicAddress != "" {
		u := &url.URL{Scheme: "http", Host: net.JoinHostPort(o.PublicAddress, strconv.Itoa(localPort))}
		return &Exposure{URL: u, Mode: "direct"}, nil
	}
	dctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	m, router, err := findRouter(dctx)
	if err != nil {
		return nil, err
	}
	ip, err := m.GetExternalIPAddress()
	if err != nil {
		return nil, fmt.Errorf("router didn't say its public address: %v", err)
	}
	pub := net.ParseIP(ip)
	if pub == nil || !isPublic(pub) {
		return nil, fmt.Errorf("the router's address %s isn't public (your ISP shares one public address between customers)", ip)
	}
	local, err := localIPToward(router)
	if err != nil {
		return nil, err
	}
	// Prefer the same port outside; pick another if it's taken.
	ext := uint16(localPort)
	var mapErr error
	for attempt := 0; attempt < 4; attempt++ {
		mapErr = addMapping(m, ext, uint16(localPort), local)
		if mapErr == nil {
			break
		}
		ext = uint16(20000 + rand.IntN(40000))
	}
	if mapErr != nil {
		return nil, fmt.Errorf("router refused to forward a port: %v", mapErr)
	}
	o.Log("router forwards port %d to this machine (UPnP)", ext)
	done := make(chan struct{})
	go func() { // renew the lease while sharing
		t := time.NewTicker(20 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				addMapping(m, ext, uint16(localPort), local)
			}
		}
	}()
	var once sync.Once
	return &Exposure{
		URL:  &url.URL{Scheme: "http", Host: net.JoinHostPort(pub.String(), strconv.Itoa(int(ext)))},
		Mode: "direct",
		stop: func() {
			once.Do(func() {
				close(done)
				m.DeletePortMapping("", ext, "TCP")
			})
		},
	}, nil
}

func addMapping(m portMapper, ext, local uint16, client string) error {
	// Some routers only accept permanent mappings (lease 0); removed on exit.
	err := m.AddPortMapping("", ext, "TCP", local, client, true, "germ share", 3600)
	if err != nil {
		err = m.AddPortMapping("", ext, "TCP", local, client, true, "germ share", 0)
	}
	return err
}

// findRouter finds the internet gateway's port-mapping service.
func findRouter(ctx context.Context) (portMapper, string, error) {
	type result struct {
		m    portMapper
		host string
	}
	ch := make(chan result, 3)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		if cs, _, err := internetgateway2.NewWANIPConnection2ClientsCtx(ctx); err == nil && len(cs) > 0 {
			ch <- result{cs[0], cs[0].Location.Host}
		}
	}()
	go func() {
		defer wg.Done()
		if cs, _, err := internetgateway2.NewWANIPConnection1ClientsCtx(ctx); err == nil && len(cs) > 0 {
			ch <- result{cs[0], cs[0].Location.Host}
		}
	}()
	go func() {
		defer wg.Done()
		if cs, _, err := internetgateway2.NewWANPPPConnection1ClientsCtx(ctx); err == nil && len(cs) > 0 {
			ch <- result{cs[0], cs[0].Location.Host}
		}
	}()
	go func() { wg.Wait(); close(ch) }()
	if r, ok := <-ch; ok {
		return r.m, r.host, nil
	}
	return nil, "", errors.New("no router answered UPnP (it may be turned off)")
}

// localIPToward is this machine's address on the route to host.
func localIPToward(hostport string) (string, error) {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	c, err := net.Dial("udp", net.JoinHostPort(host, "1900"))
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isPublic(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !cgnat.Contains(ip)
}

// ---- Cloudflare quick tunnel ------------------------------------------------

var tunnelURLRE = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

func exposeTunnel(ctx context.Context, localPort int, o ExposeOptions) (*Exposure, error) {
	bin, err := cloudflared(o.Log)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "tunnel", "--no-autoupdate", "--url", fmt.Sprintf("http://127.0.0.1:%d", localPort))
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if m := tunnelURLRE.FindString(sc.Text()); m != "" {
				select {
				case found <- m:
				default:
				}
			}
		}
	}()
	stop := func() { cmd.Process.Kill(); cmd.Wait() }
	var base string
	select {
	case base = <-found:
	case <-time.After(45 * time.Second):
		stop()
		return nil, errors.New("Cloudflare didn't hand out a tunnel (try again in a minute)")
	case <-ctx.Done():
		stop()
		return nil, ctx.Err()
	}
	u, _ := url.Parse(base)
	// The name takes a few seconds to resolve everywhere; wait until it
	// answers so the link works when it's pasted.
	client := &http.Client{Timeout: 5 * time.Second}
	for deadline := time.Now().Add(40 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if resp, err := client.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
	}
	return &Exposure{URL: u, Mode: "tunnel", stop: stop}, nil
}

// cloudflared finds the cloudflared binary, downloading Cloudflare's
// release into germ's cache the first time.
func cloudflared(log func(string, ...any)) (string, error) {
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p, nil
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		home, _ := os.UserHomeDir()
		cache = filepath.Join(home, ".cache")
	}
	bin := filepath.Join(cache, "germ", "bin", "cloudflared")
	if fi, err := os.Stat(bin); err == nil && fi.Size() > 0 {
		return bin, nil
	}
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64", "arm": "arm", "386": "386"}[runtime.GOARCH]
	if arch == "" {
		return "", fmt.Errorf("no cloudflared build for %s", runtime.GOARCH)
	}
	src := "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-" + arch
	log("downloading cloudflared (once) from %s", src)
	resp, err := http.Get(src)
	if err != nil {
		return "", fmt.Errorf("downloading cloudflared: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("downloading cloudflared: %s", resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}
	tmp := bin + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	return bin, os.Rename(tmp, bin)
}
