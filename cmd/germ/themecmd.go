package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/theme"
)

const themeUsage = `usage: germ theme COMMAND

  list                          every theme: bundled, and yours
  use NAME                      set theme = NAME in the config (open windows follow)
  new [-from THEME] [-f] NAME   start your own theme from THEME (default: the
                                current one) in ~/.config/germ/themes/NAME.toml,
                                and use it; edit and save it to see changes live
  import [-name NAME] [-f] FILE convert a base16/base24 YAML, iTerm2
                                .itermcolors, or Alacritty TOML scheme into
                                ~/.config/germ/themes/

Ctrl+Shift+Space, then "theme", previews every theme in the window.
`

func runTheme(args []string) int {
	if len(args) == 0 {
		fmt.Print(themeUsage)
		return 2
	}
	switch args[0] {
	case "list", "ls":
		listThemes()
		return 0
	case "use":
		if len(args) != 2 {
			fmt.Print(themeUsage)
			return 2
		}
		if _, err := theme.Load(args[1], config.ThemeDirs()...); err != nil {
			fmt.Fprintln(os.Stderr, "germ theme:", err)
			return 1
		}
		return useTheme(args[1])
	case "new":
		fs := flag.NewFlagSet("germ theme new", flag.ContinueOnError)
		from := fs.String("from", "", "theme to start from (default: the current one)")
		force := fs.Bool("f", false, "replace an existing theme of the same name")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
			fmt.Print(themeUsage)
			return 2
		}
		name := themeSlug(fs.Arg(0))
		if *from == "" {
			*from = loadConfig().Theme
			if *from == "" {
				*from = theme.DefaultName
			}
		}
		t, err := theme.Load(*from, config.ThemeDirs()...)
		if err != nil {
			fmt.Fprintln(os.Stderr, "germ theme:", err)
			return 1
		}
		dir := config.ThemeDirs()[0]
		dst := filepath.Join(dir, name+".toml")
		if _, err := os.Stat(dst); err == nil && !*force {
			fmt.Fprintf(os.Stderr, "germ theme: %s exists (-f replaces it)\n", dst)
			return 1
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "germ theme:", err)
			return 1
		}
		if err := os.WriteFile(dst, theme.Template(name, t), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "germ theme:", err)
			return 1
		}
		fmt.Printf("made %s from %s\n", dst, *from)
		if code := useTheme(name); code != 0 {
			return code
		}
		fmt.Printf("edit it (e.g. $EDITOR %s): each save recolors germ\n", dst)
		return 0
	case "import":
		fs := flag.NewFlagSet("germ theme import", flag.ContinueOnError)
		name := fs.String("name", "", "theme name (default: the scheme's own)")
		force := fs.Bool("f", false, "replace an existing theme of the same name")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
			fmt.Print(themeUsage)
			return 2
		}
		src := fs.Arg(0)
		data, err := os.ReadFile(src)
		if err != nil {
			fmt.Fprintln(os.Stderr, "germ theme:", err)
			return 1
		}
		f, err := theme.Import(data, src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "germ theme: %s: %v\n", src, err)
			return 1
		}
		if *name != "" {
			f.Name = *name
		}
		slug := themeSlug(f.Name)
		dir := config.ThemeDirs()[0]
		dst := filepath.Join(dir, slug+".toml")
		if _, err := os.Stat(dst); err == nil && !*force {
			fmt.Fprintf(os.Stderr, "germ theme: %s exists (-f replaces it, -name picks another name)\n", dst)
			return 1
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "germ theme:", err)
			return 1
		}
		if err := os.WriteFile(dst, f.Encode(), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "germ theme:", err)
			return 1
		}
		fmt.Printf("imported %q to %s\nuse it with theme = %q in %s (open windows recolor when you save)\n",
			f.Name, dst, slug, config.Path())
		return 0
	}
	fmt.Print(themeUsage)
	return 2
}

// listThemes prints every theme, marking yours and the current one.
func listThemes() {
	mine := map[string]bool{}
	for _, n := range theme.Installed(config.ThemeDirs()...) {
		mine[n] = true
	}
	all := map[string]bool{}
	for _, n := range theme.Bundled() {
		all[n] = true
	}
	for n := range mine {
		all[n] = true
	}
	var names []string
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	cur := loadConfig().Theme
	for _, n := range names {
		note := ""
		if mine[n] {
			note = "  (yours: " + filepath.Join(tildePath(config.ThemeDirs()[0]), n+".toml") + ")"
		}
		mark := "  "
		if n == cur {
			mark = "* "
		}
		fmt.Println(mark + n + note)
	}
}

// useTheme sets the theme in the config.
func useTheme(name string) int {
	path := os.Getenv("GERM_CONFIG")
	if path == "" {
		path = config.Path()
	}
	old, err := config.SetTheme(path, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ theme:", err)
		return 1
	}
	if old != "" && old != name {
		fmt.Printf("theme = %q in %s (was %q)\n", name, tildePath(path), old)
	} else {
		fmt.Printf("theme = %q in %s\n", name, tildePath(path))
	}
	return 0
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func themeSlug(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		s = "imported"
	}
	return s
}
