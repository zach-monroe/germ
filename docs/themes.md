# Themes

## Pick one

**Ctrl+Shift+Space**, type `theme`: every theme is listed, bundled ones
and your own. Moving onto one recolors the window right away; **Enter**
keeps it (germ sets `theme = "…"` in your config, changing only that line,
and every window using the config follows), **Esc** goes back to what you
had.

From a shell:

```sh
germ theme list          # * marks the current one
germ theme use nord      # set it in the config
germ -theme dracula      # one window only, config untouched
```

Bundled: catppuccin-latte, catppuccin-mocha, dracula, gruvbox-dark, nord,
one-dark, rose-pine, solarized-dark, tokyonight.

## Make your own

```sh
germ theme new ocean             # starts from your current theme
germ theme new -from nord ocean  # or from any other
```

This writes `~/.config/germ/themes/ocean.toml` and switches to it. Open it
in your editor: every save recolors germ, so you can tune it while looking
at it.

```toml
name = "ocean"

foreground = "#d8dee9"            # text
background = "#2e3440"
cursor = "#d8dee9"
cursor_text = "#2e3440"           # text under a block cursor
selection_foreground = "#d8dee9"
selection_background = "#434c5e"

# The 16 terminal colors programs pick from:
#          black      red        green      yellow     blue       magenta    cyan       white
normal = ["#3b4252", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#88c0d0", "#e5e9f0"]
bright = ["#4c566a", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#8fbcbb", "#eceff4"]
```

Colors are `#rrggbb` (or `#rgb`). `normal` and `bright` are the 16 ANSI
colors, in order; the other 240 of the 256-color palette are the standard
xterm ones.

To tweak just a color or two of any theme, without a theme file, use
`[colors]` in the config (same keys as above).

## Import one

Thousands of schemes exist for other terminals. `germ theme import FILE`
converts:

- base16 / base24 YAML (the tinted-theming collection),
- iTerm2 `.itermcolors` (iterm2colorschemes.com has hundreds),
- Alacritty TOML.

```sh
germ theme import ~/Downloads/Ocean.itermcolors
germ theme import -name ocean2 base16-ocean.yaml
```

Imported themes land in `~/.config/germ/themes/` and show up in the
palette and `germ theme list`.

## Shells that set colors

Some setups set the terminal's colors themselves with escape sequences
(OSC 4/10/11): pywal, desktop theme tools that `cat` a sequences file in
your shell startup, a prompt framework's "theme". By default germ lets
them, as other terminals do, and they win over germ's theme. Every new
tab runs your shell startup again, so new tabs look different from tabs
germ reattached to.

To have germ's theme always win, set:

```toml
[shell]
program_colors = false
```

It applies at once, to open tabs too. Programs can still ask for the
colors (so editors pick light or dark correctly); they just can't change
them.
