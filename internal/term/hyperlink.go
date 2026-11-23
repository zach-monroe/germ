package term

import "strings"

// OSC 8 hyperlinks: OSC 8 ; params ; URI ST starts a link, OSC 8 ; ; ST
// ends it. Printed cells carry the link's ID (Cell.Link), which indexes a
// per-terminal table of URIs.

const maxLinks = 1<<16 - 1

type hyperlinks struct {
	uris []string          // index = Cell.Link; [0] unused
	ids  map[string]uint16 // "id\x00uri" -> index, to share entries
}

// allowedSchemes are the links germ will open: ones a click can't turn into
// something surprising.
var allowedSchemes = []string{"http://", "https://", "mailto:", "file://", "ftp://", "sftp://", "ssh://", "man:",
	"germ-pane:"} // germ-pane:PID/PANE focuses a pane (agents widget)

func (t *Terminal) oscHyperlink(rest string) {
	params, uri, ok := strings.Cut(rest, ";")
	if !ok || uri == "" {
		t.cur.Pen.Link = 0
		return
	}
	allowed := false
	for _, s := range allowedSchemes {
		if len(uri) >= len(s) && strings.EqualFold(uri[:len(s)], s) {
			allowed = true
			break
		}
	}
	if !allowed || len(uri) > 4096 {
		t.cur.Pen.Link = 0
		return
	}
	id := ""
	for _, p := range strings.Split(params, ":") {
		if v, ok := strings.CutPrefix(p, "id="); ok {
			id = v
		}
	}
	h := &t.links
	key := id + "\x00" + uri
	if n, ok := h.ids[key]; ok {
		t.cur.Pen.Link = n
		return
	}
	if len(h.uris) == 0 {
		h.uris = []string{""}
		h.ids = map[string]uint16{}
	}
	if len(h.uris) > maxLinks {
		t.cur.Pen.Link = 0 // table full; text still prints, unlinked
		return
	}
	n := uint16(len(h.uris))
	h.uris = append(h.uris, uri)
	h.ids[key] = n
	t.cur.Pen.Link = n
}

// LinkURL returns the URI of a hyperlink ID. Caller must hold the lock.
func (t *Terminal) LinkURL(id uint16) string {
	if int(id) < len(t.links.uris) {
		return t.links.uris[id]
	}
	return ""
}
