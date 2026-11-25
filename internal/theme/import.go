package theme

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Import converts another terminal's color scheme to germ's format:
// base16/base24 YAML, iTerm2 .itermcolors, or Alacritty TOML (picked by
// content, with the file name as a hint). The name defaults to the
// scheme's own, else the file name.
func Import(data []byte, filename string) (*File, error) {
	var f *File
	var err error
	ext := strings.ToLower(filepath.Ext(filename))
	switch {
	case ext == ".itermcolors" || bytes.Contains(data, []byte("<plist")):
		f, err = importITerm(data)
	case bytes.Contains(data, []byte("base0")) && (ext == ".yaml" || ext == ".yml" || !bytes.Contains(data, []byte("[colors"))):
		f, err = importBase16(data)
	case bytes.Contains(data, []byte("[colors")):
		f, err = importAlacritty(data)
	default:
		return nil, errors.New("not a base16 YAML, iTerm2 .itermcolors, or Alacritty TOML color scheme")
	}
	if err != nil {
		return nil, err
	}
	if f.Name == "" {
		f.Name = strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	}
	// Check the result parses as a germ theme.
	if _, err := Parse(f.Encode()); err != nil {
		return nil, fmt.Errorf("converted theme is invalid: %w", err)
	}
	return f, nil
}

// Encode writes a theme file.
func (f *File) Encode() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "name = %q\n", f.Name)
	for _, kv := range [][2]string{{"foreground", f.Foreground}, {"background", f.Background},
		{"cursor", f.Cursor}, {"cursor_text", f.CursorText},
		{"selection_foreground", f.SelectionForeground}, {"selection_background", f.SelectionBackground}} {
		if kv[1] != "" {
			fmt.Fprintf(&b, "%s = %q\n", kv[0], kv[1])
		}
	}
	list := func(k string, v []string) {
		q := make([]string, len(v))
		for i, c := range v {
			q[i] = strconv.Quote(c)
		}
		fmt.Fprintf(&b, "%s = [%s]\n", k, strings.Join(q, ", "))
	}
	list("normal", f.Normal)
	list("bright", f.Bright)
	return b.Bytes()
}

// ---- base16 / base24 --------------------------------------------------------

var (
	base16Key  = regexp.MustCompile(`(?m)^\s*(base[01][0-9A-Fa-f])\s*:\s*["']?#?([0-9A-Fa-f]{6})`)
	base16Name = regexp.MustCompile(`(?m)^\s*(?:scheme|name)\s*:\s*["']?([^"'\n]+?)["']?\s*$`)
)

func importBase16(data []byte) (*File, error) {
	c := map[string]string{}
	for _, m := range base16Key.FindAllSubmatch(data, -1) {
		c[strings.ToUpper(string(m[1][4:]))] = "#" + strings.ToLower(string(m[2]))
	}
	for _, k := range []string{"00", "02", "03", "05", "07", "08", "0A", "0B", "0C", "0D", "0E"} {
		if c[k] == "" {
			return nil, fmt.Errorf("base16 scheme is missing base%s", k)
		}
	}
	f := &File{
		Foreground: c["05"], Background: c["00"], Cursor: c["05"], CursorText: c["00"],
		SelectionForeground: c["05"], SelectionBackground: c["02"],
		Normal: []string{c["00"], c["08"], c["0B"], c["0A"], c["0D"], c["0E"], c["0C"], c["05"]},
		Bright: []string{c["03"], c["08"], c["0B"], c["0A"], c["0D"], c["0E"], c["0C"], c["07"]},
	}
	// base24 has distinct bright colors.
	if c["12"] != "" && c["13"] != "" && c["14"] != "" && c["15"] != "" && c["16"] != "" && c["17"] != "" {
		f.Bright = []string{c["03"], c["12"], c["14"], c["13"], c["16"], c["17"], c["15"], c["07"]}
	}
	if m := base16Name.FindSubmatch(data); m != nil {
		f.Name = strings.TrimSpace(string(m[1]))
	}
	return f, nil
}

// ---- iTerm2 -----------------------------------------------------------------

func importITerm(data []byte) (*File, error) {
	colors, err := parsePlistColors(data)
	if err != nil {
		return nil, err
	}
	get := func(k string) string { return colors[k] }
	f := &File{
		Foreground: get("Foreground Color"), Background: get("Background Color"),
		Cursor: get("Cursor Color"), CursorText: get("Cursor Text Color"),
		SelectionForeground: get("Selected Text Color"), SelectionBackground: get("Selection Color"),
	}
	for i := 0; i < 16; i++ {
		c := get(fmt.Sprintf("Ansi %d Color", i))
		if c == "" {
			return nil, fmt.Errorf("iTerm2 scheme is missing Ansi %d Color", i)
		}
		if i < 8 {
			f.Normal = append(f.Normal, c)
		} else {
			f.Bright = append(f.Bright, c)
		}
	}
	if f.Foreground == "" || f.Background == "" {
		return nil, errors.New("iTerm2 scheme has no foreground/background")
	}
	return f, nil
}

// parsePlistColors reads the top-level dict of color dicts in an
// .itermcolors plist: name -> "#rrggbb".
func parsePlistColors(data []byte) (map[string]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	out := map[string]string{}
	depth := 0 // dict nesting
	var key, inner string
	comp := map[string]float64{}
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			text.Reset()
			if t.Name.Local == "dict" {
				depth++
				if depth == 2 {
					comp = map[string]float64{}
				}
			}
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			s := strings.TrimSpace(text.String())
			switch t.Name.Local {
			case "key":
				if depth == 1 {
					key = s
				} else {
					inner = s
				}
			case "real", "integer":
				if depth == 2 {
					v, _ := strconv.ParseFloat(s, 64)
					comp[inner] = v
				}
			case "dict":
				if depth == 2 && key != "" {
					if _, ok := comp["Red Component"]; ok {
						ch := func(n string) int { return int(math.Round(max(0, min(1, comp[n])) * 255)) }
						out[key] = fmt.Sprintf("#%02x%02x%02x", ch("Red Component"), ch("Green Component"), ch("Blue Component"))
					}
				}
				depth--
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no colors found in the plist")
	}
	return out, nil
}

// ---- Alacritty ----------------------------------------------------------------

func importAlacritty(data []byte) (*File, error) {
	type ansi struct {
		Black, Red, Green, Yellow, Blue, Magenta, Cyan, White string
	}
	var a struct {
		Colors struct {
			Primary struct {
				Foreground, Background string
			}
			Cursor struct {
				Text, Cursor string
			}
			Selection struct {
				Text, Background string
			}
			Normal, Bright ansi
		}
	}
	if _, err := toml.Decode(string(data), &a); err != nil {
		return nil, fmt.Errorf("alacritty theme: %w", err)
	}
	c := a.Colors
	hex := func(s string) string {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			s = "#" + s[2:]
		}
		if _, err := ParseColor(s); err != nil {
			return "" // "CellForeground" and the like
		}
		return strings.ToLower(s)
	}
	list := func(x ansi) []string {
		return []string{hex(x.Black), hex(x.Red), hex(x.Green), hex(x.Yellow), hex(x.Blue), hex(x.Magenta), hex(x.Cyan), hex(x.White)}
	}
	f := &File{
		Foreground: hex(c.Primary.Foreground), Background: hex(c.Primary.Background),
		Cursor: hex(c.Cursor.Cursor), CursorText: hex(c.Cursor.Text),
		SelectionForeground: hex(c.Selection.Text), SelectionBackground: hex(c.Selection.Background),
		Normal: list(c.Normal), Bright: list(c.Bright),
	}
	if f.Foreground == "" || f.Background == "" {
		return nil, errors.New("alacritty theme has no [colors.primary] foreground/background")
	}
	for i, n := range f.Normal {
		if n == "" {
			return nil, fmt.Errorf("alacritty theme is missing a normal color (%d)", i)
		}
	}
	for i := range f.Bright {
		if f.Bright[i] == "" {
			f.Bright[i] = f.Normal[i] // bright is optional
		}
	}
	return f, nil
}
