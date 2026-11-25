package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/theme"
)

const themeUsage = `usage: germ theme COMMAND

  list                          bundled and installed themes
  import [-name NAME] [-f] FILE convert a base16/base24 YAML, iTerm2
                                .itermcolors, or Alacritty TOML scheme into
                                ~/.config/germ/themes/
`

func runTheme(args []string) int {
	if len(args) == 0 {
		fmt.Print(themeUsage)
		return 2
	}
	switch args[0] {
	case "list", "ls":
		fmt.Println(strings.Join(theme.Bundled(), "\n"))
		for _, d := range config.ThemeDirs() {
			files, _ := filepath.Glob(filepath.Join(d, "*.toml"))
			for _, f := range files {
				fmt.Println(strings.TrimSuffix(filepath.Base(f), ".toml"), "\t("+d+")")
			}
		}
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

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func themeSlug(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		s = "imported"
	}
	return s
}
