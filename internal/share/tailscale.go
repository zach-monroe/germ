package share

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Tailscale Funnel: `tailscale funnel` publishes a local port at
// https://<machine>.<tailnet>.ts.net with a certificate issued to this
// machine; Tailscale's servers only forward the encrypted connection.
// germ runs it in the foreground, so the public address disappears as
// soon as sharing stops.

// errNoTailscale means Tailscale isn't installed or isn't running: not
// worth a message when falling back.
var errNoTailscale = errors.New("tailscale isn't running")

type tsStatus struct {
	BackendState string `json:"BackendState"`
	Self         struct {
		DNSName string                     `json:"DNSName"`
		CapMap  map[string]json.RawMessage `json:"CapMap"`
	} `json:"Self"`
}

// funnelAllowed reports whether the tailnet's policy lets this machine use
// Funnel (the "funnel" node attribute).
func (s *tsStatus) funnelAllowed() bool {
	for k := range s.Self.CapMap {
		if k == "funnel" || strings.HasSuffix(k, "/cap/funnel") {
			return true
		}
	}
	return false
}

func tailscaleStatus(ctx context.Context) (*tsStatus, error) {
	if _, err := exec.LookPath("tailscale"); err != nil {
		return nil, errNoTailscale
	}
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return nil, errNoTailscale
	}
	var st tsStatus
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, err
	}
	if st.BackendState != "Running" {
		return nil, fmt.Errorf("tailscale is %s (run `tailscale up`)", strings.ToLower(st.BackendState))
	}
	return &st, nil
}

var funnelURLRE = regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.ts\.net(:\d+)?`)
var anyURLRE = regexp.MustCompile(`https://\S+`)

func exposeTailscale(ctx context.Context, localPort int, o ExposeOptions) (*Exposure, error) {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	st, err := tailscaleStatus(sctx)
	cancel()
	if err != nil {
		return nil, err
	}
	if !st.funnelAllowed() {
		return nil, errors.New("Funnel isn't enabled for this machine; turn it on in the Tailscale admin console " +
			"(Access controls → Funnel), see https://tailscale.com/kb/1223/funnel")
	}
	host := strings.TrimSuffix(st.Self.DNSName, ".")
	target := fmt.Sprintf("http://127.0.0.1:%d", localPort)
	var lastErr error
	for _, port := range []int{443, 8443, 10000} { // the ports Funnel allows
		e, err := runFunnel(ctx, port, target, host)
		if err == nil {
			o.Log("Tailscale Funnel: %s", e.URL)
			return e, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "in use") && !strings.Contains(err.Error(), "already") {
			break // not a port clash: another port won't help
		}
	}
	return nil, lastErr
}

// runFunnel starts `tailscale funnel` in the foreground and waits until it
// says it's serving.
func runFunnel(ctx context.Context, port int, target, host string) (*Exposure, error) {
	cmd := exec.Command("tailscale", "funnel", fmt.Sprintf("--https=%d", port), target)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stop := func() { cmd.Process.Kill(); cmd.Wait() }
	type result struct {
		url string
		err error
	}
	res := make(chan result, 1)
	var once sync.Once
	report := func(r result) { once.Do(func() { res <- r }) }
	go func() {
		var text strings.Builder
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			line := sc.Text()
			text.WriteString(line + "\n")
			low := strings.ToLower(line)
			switch {
			case strings.Contains(low, "access denied") || strings.Contains(low, "permission denied"):
				report(result{err: errors.New("your user may not configure Tailscale; run once: sudo tailscale set --operator=$USER")})
			case strings.Contains(low, "not enabled") || strings.Contains(low, "login.tailscale.com"):
				u := anyURLRE.FindString(line)
				report(result{err: fmt.Errorf("Funnel needs to be enabled for this tailnet: %s", strings.TrimSpace(u))})
			case funnelURLRE.MatchString(line):
				report(result{url: funnelURLRE.FindString(line)})
			}
		}
		report(result{err: fmt.Errorf("tailscale funnel exited: %s", strings.TrimSpace(text.String()))})
	}()
	select {
	case r := <-res:
		if r.err != nil {
			stop()
			return nil, r.err
		}
		u, err := url.Parse(r.url)
		if err != nil || u.Host == "" {
			u = &url.URL{Scheme: "https", Host: host}
			if port != 443 {
				u.Host = fmt.Sprintf("%s:%d", host, port)
			}
		}
		return &Exposure{URL: u, Mode: "tailscale", stop: stop}, nil
	case <-time.After(20 * time.Second):
		stop()
		return nil, errors.New("tailscale funnel didn't start in time")
	case <-ctx.Done():
		stop()
		return nil, ctx.Err()
	}
}
