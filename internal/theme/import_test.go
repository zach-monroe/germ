package theme

import (
	"strconv"
	"strings"
	"testing"
)

const base16YAML = `scheme: "Ocean"
author: "Chris Kempson"
base00: "2b303b"
base01: "343d46"
base02: "4f5b66"
base03: "65737e"
base04: "a7adba"
base05: "c0c5ce"
base06: "dfe1e8"
base07: "eff1f5"
base08: "bf616a"
base09: "d08770"
base0A: "ebcb8b"
base0B: "a3be8c"
base0C: "96b5b4"
base0D: "8fa1b3"
base0E: "b48ead"
base0F: "ab7967"
`

// tinted-theming's newer layout
const tintedYAML = `system: "base16"
name: "Tomorrow Night"
variant: "dark"
palette:
  base00: "#1d1f21"
  base01: "#282a2e"
  base02: "#373b41"
  base03: "#969896"
  base04: "#b4b7b4"
  base05: "#c5c8c6"
  base06: "#e0e0e0"
  base07: "#ffffff"
  base08: "#cc6666"
  base09: "#de935f"
  base0A: "#f0c674"
  base0B: "#b5bd68"
  base0C: "#8abeb7"
  base0D: "#81a2be"
  base0E: "#b294bb"
  base0F: "#a3685a"
`

func itermColor(name string, r, g, b float64) string {
	return `<key>` + name + `</key><dict><key>Color Space</key><string>sRGB</string>` +
		`<key>Blue Component</key><real>` + ftoa(b) + `</real><key>Green Component</key><real>` + ftoa(g) +
		`</real><key>Red Component</key><real>` + ftoa(r) + `</real></dict>`
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func TestImportBase16(t *testing.T) {
	f, err := Import([]byte(base16YAML), "ocean.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "Ocean" || f.Background != "#2b303b" || f.Foreground != "#c0c5ce" ||
		f.Normal[1] != "#bf616a" || f.Bright[0] != "#65737e" || f.Bright[7] != "#eff1f5" || f.SelectionBackground != "#4f5b66" {
		t.Errorf("%+v", f)
	}
	f, err = Import([]byte(tintedYAML), "tomorrow-night.yaml")
	if err != nil || f.Name != "Tomorrow Night" || f.Normal[4] != "#81a2be" {
		t.Errorf("tinted: %v %+v", err, f)
	}
	if _, err := Import([]byte("scheme: x\nbase00: \"000000\"\n"), "x.yaml"); err == nil {
		t.Error("accepted an incomplete scheme")
	}
}

func TestImportITerm(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>`)
	for i := 0; i < 16; i++ {
		b.WriteString(itermColor("Ansi "+itoa(i)+" Color", float64(i)/15, 0, 1-float64(i)/15))
	}
	b.WriteString(itermColor("Foreground Color", 1, 1, 1))
	b.WriteString(itermColor("Background Color", 0, 0, 0.5))
	b.WriteString(itermColor("Selection Color", 0.2, 0.2, 0.2))
	b.WriteString(`</dict></plist>`)
	f, err := Import([]byte(b.String()), "Mine.itermcolors")
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "Mine" || f.Foreground != "#ffffff" || f.Background != "#000080" || f.Normal[0] != "#0000ff" ||
		f.Bright[7] != "#ff0000" || f.SelectionBackground != "#333333" {
		t.Errorf("%+v", f)
	}
}

func TestImportAlacritty(t *testing.T) {
	src := `[colors.primary]
background = "0x1e1e2e"
foreground = "#cdd6f4"
[colors.cursor]
text = "CellBackground"
cursor = "#f5e0dc"
[colors.normal]
black = "#45475a"
red = "#f38ba8"
green = "#a6e3a1"
yellow = "#f9e2af"
blue = "#89b4fa"
magenta = "#f5c2e7"
cyan = "#94e2d5"
white = "#bac2de"
`
	f, err := Import([]byte(src), "catppuccin.toml")
	if err != nil {
		t.Fatal(err)
	}
	if f.Background != "#1e1e2e" || f.CursorText != "" || f.Cursor != "#f5e0dc" || f.Bright[1] != "#f38ba8" || f.Name != "catppuccin" {
		t.Errorf("%+v", f)
	}
	// The result is a valid germ theme.
	if th, err := Parse(f.Encode()); err != nil || th.Palette[4] != (RGB{0x89, 0xb4, 0xfa}) {
		t.Errorf("parse: %v", err)
	}
}

func TestImportUnknown(t *testing.T) {
	if _, err := Import([]byte("hello"), "x.txt"); err == nil {
		t.Error("accepted garbage")
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
