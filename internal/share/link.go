package share

import (
	"fmt"
	"net/url"
	"strings"
)

// Link is an invite: which relay, which room, and the key (which decides
// whether the guest can type or only watch).
type Link struct {
	Relay *url.URL // base URL of the relay (http/https)
	Room  string
	Key   Key
}

// String is the shareable https link (opens the join page in a browser).
func (l Link) String() string {
	u := *l.Relay
	u.Path = strings.TrimRight(u.Path, "/") + "/j/" + l.Room
	u.Fragment = l.Key.String()
	u.RawFragment = ""
	return u.String()
}

// GermURL is the germ:// form, opened by germ's URL handler.
func (l Link) GermURL() string {
	scheme := ""
	if l.Relay.Scheme == "http" {
		scheme = "http:" // plain HTTP relays (LAN/testing) say so
	}
	return "germ://join/" + scheme + l.Relay.Host + strings.TrimRight(l.Relay.Path, "/") + "/" + l.Room + "#" + l.Key.String()
}

// websocketURL is the relay endpoint for a role ("host" or "guest").
func (l Link) websocketURL(role string) string {
	u := *l.Relay
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/ws/" + role + "/" + l.Room
	u.Fragment = ""
	return u.String()
}

// ParseLink accepts https://relay/j/ROOM#KEY or germ://join/[http:]relay/ROOM#KEY.
func ParseLink(s string) (Link, error) {
	s = strings.TrimSpace(s)
	var l Link
	u, err := url.Parse(s)
	if err != nil {
		return l, fmt.Errorf("bad link: %v", err)
	}
	if u.Fragment == "" {
		return l, fmt.Errorf("link has no key (the part after #); copy the whole link")
	}
	if l.Key, err = ParseKey(u.Fragment); err != nil {
		return l, err
	}
	switch u.Scheme {
	case "germ":
		// germ://join/[http:]host[/prefix]/ROOM
		if u.Host != "join" {
			return l, fmt.Errorf("bad germ link %q", s)
		}
		rest := strings.TrimPrefix(u.Path, "/")
		scheme := "https"
		if after, ok := strings.CutPrefix(rest, "http:"); ok {
			scheme, rest = "http", after
		}
		i := strings.LastIndexByte(rest, '/')
		if i <= 0 {
			return l, fmt.Errorf("bad germ link %q", s)
		}
		hostPath, room := rest[:i], rest[i+1:]
		host, path, _ := strings.Cut(hostPath, "/")
		l.Relay = &url.URL{Scheme: scheme, Host: host}
		if path != "" {
			l.Relay.Path = "/" + path
		}
		l.Room = room
	case "http", "https":
		i := strings.LastIndex(u.Path, "/j/")
		if i < 0 {
			return l, fmt.Errorf("not a germ invite link: %q", s)
		}
		l.Room = u.Path[i+3:]
		l.Relay = &url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path[:i]}
	default:
		return l, fmt.Errorf("not a germ invite link: %q", s)
	}
	if l.Room == "" || strings.ContainsAny(l.Room, "/?#") {
		return l, fmt.Errorf("bad room in link")
	}
	return l, nil
}
