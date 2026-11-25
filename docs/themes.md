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

## Follow your desktop theme

If a desktop tool sets your colors (caelestia, pywal, matugen, …), have it
write a germ theme and point germ at that file. germ recolors whenever the
tool rewrites it, so switching the desktop theme switches germ too.

1. Give the tool a template in germ's theme format (above). With
   caelestia, put it in `~/.config/caelestia/templates/germ.toml`;
   colors are written `"#{{ surface.hex }}"`, and caelestia's terminal
   colors are `onSurface` (text), `surface` (background), `secondary`
   (cursor and selection) and `term0` … `term15`:

   ```toml
   name = "caelestia"
   foreground = "#{{ onSurface.hex }}"
   background = "#{{ surface.hex }}"
   cursor = "#{{ secondary.hex }}"
   cursor_text = "#{{ surface.hex }}"
   selection_foreground = "#{{ onSecondary.hex }}"
   selection_background = "#{{ secondary.hex }}"
   normal = ["#{{ term0.hex }}", "#{{ term1.hex }}", "#{{ term2.hex }}", "#{{ term3.hex }}", "#{{ term4.hex }}", "#{{ term5.hex }}", "#{{ term6.hex }}", "#{{ term7.hex }}"]
   bright = ["#{{ term8.hex }}", "#{{ term9.hex }}", "#{{ term10.hex }}", "#{{ term11.hex }}", "#{{ term12.hex }}", "#{{ term13.hex }}", "#{{ term14.hex }}", "#{{ term15.hex }}"]
   ```

   caelestia writes it to `~/.local/state/caelestia/theme/germ.toml` on
   every scheme change (run `caelestia scheme set` once to create it).

2. Link it into your themes, so it shows in the palette as one theme
   among the others, and use it:

   ```sh
   ln -s ~/.local/state/caelestia/theme/germ.toml ~/.config/germ/themes/caelestia.toml
   germ theme use caelestia
   ```

   (`theme = "~/.local/state/caelestia/theme/germ.toml"` works too.)

3. These tools usually also send colors to terminals directly, and
   your shell startup may replay them. Set `program_colors = false`
   (below) so the theme file is the one source and every tab matches.

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
