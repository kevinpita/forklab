package tui

import (
	"image/color"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
)

// Palette is the small set of semantic colors every style derives from.
type Palette struct {
	Accent  color.Color // focused borders, keys, titles
	Accent2 color.Color // prompts and secondary highlights
	Fg      color.Color
	Muted   color.Color
	Border  color.Color
	Good    color.Color
	Warn    color.Color
	Bad     color.Color
	SelFg   color.Color
	SelBg   color.Color
	LogoFg  color.Color
	LogoBg  color.Color
	// ReverseSel highlights the selection with reverse video so it follows
	// the terminal's own colors.
	ReverseSel bool
}

// Theme is a palette plus the styles the views render with.
type Theme struct {
	Name string
	Dark bool
	P    Palette

	Logo, Key, Val, Dim, Text, Title       lipgloss.Style
	Good, Warn, Bad, Accent2               lipgloss.Style
	Border, BorderActive, TitleActive      lipgloss.Style
	Sel, SelDim                            lipgloss.Style
	FooterKey, FooterDesc, StatusOK, Error lipgloss.Style
}

func newTheme(name string, dark bool, p Palette) Theme {
	s := lipgloss.NewStyle
	t := Theme{Name: name, Dark: dark, P: p}
	t.Logo = s().Bold(true).Foreground(p.LogoFg).Background(p.LogoBg).Padding(0, 1)
	t.Key = s().Foreground(p.Muted)
	t.Val = s().Foreground(p.Accent).Bold(true)
	t.Dim = s().Foreground(p.Muted)
	t.Text = s().Foreground(p.Fg)
	t.Title = s().Foreground(p.Fg).Bold(true)
	t.Good = s().Foreground(p.Good)
	t.Warn = s().Foreground(p.Warn)
	t.Bad = s().Foreground(p.Bad)
	t.Accent2 = s().Foreground(p.Accent2).Bold(true)
	t.Border = s().Foreground(p.Border)
	t.BorderActive = s().Foreground(p.Accent)
	t.TitleActive = s().Foreground(p.Accent).Bold(true)
	if p.ReverseSel {
		t.Sel = s().Reverse(true).Bold(true)
	} else {
		t.Sel = s().Foreground(p.SelFg).Background(p.SelBg).Bold(true)
	}
	t.SelDim = s().Foreground(p.Accent).Bold(true)
	t.FooterKey = s().Foreground(p.Accent).Bold(true)
	t.FooterDesc = s().Foreground(p.Muted)
	t.StatusOK = s().Foreground(p.Good).Bold(true)
	t.Error = s().Foreground(p.Bad).Bold(true)
	return t
}

func ansiPalette(dark bool) Palette {
	c := lipgloss.Color
	if dark {
		return Palette{
			Accent: c("12"), Accent2: c("13"), Fg: lipgloss.NoColor{}, Muted: c("7"), Border: c("8"),
			Good: c("10"), Warn: c("11"), Bad: c("9"), SelFg: lipgloss.NoColor{}, SelBg: lipgloss.NoColor{},
			LogoFg: c("0"), LogoBg: c("12"), ReverseSel: true,
		}
	}
	return Palette{
		Accent: c("4"), Accent2: c("5"), Fg: lipgloss.NoColor{}, Muted: c("8"), Border: c("8"),
		Good: c("2"), Warn: c("3"), Bad: c("1"), SelFg: lipgloss.NoColor{}, SelBg: lipgloss.NoColor{},
		LogoFg: c("15"), LogoBg: c("4"), ReverseSel: true,
	}
}

type hexPalette struct {
	accent, accent2, fg, muted, border, good, warn, bad, selFg, selBg string
}

func (h hexPalette) palette() Palette {
	c := lipgloss.Color
	return Palette{
		Accent: c(h.accent), Accent2: c(h.accent2), Fg: c(h.fg), Muted: c(h.muted), Border: c(h.border),
		Good: c(h.good), Warn: c(h.warn), Bad: c(h.bad), SelFg: c(h.selFg), SelBg: c(h.selBg),
		LogoFg: c(h.selFg), LogoBg: c(h.accent),
	}
}

// builtinThemes are the named themes after "ansi", each with a dark and a
// light variant.
var builtinThemes = []struct {
	name        string
	dark, light hexPalette
}{
	{
		"tokyonight",
		hexPalette{"#7aa2f7", "#bb9af7", "#c0caf5", "#737aa2", "#3b4261", "#9ece6a", "#e0af68", "#f7768e", "#1a1b26", "#7aa2f7"},
		hexPalette{"#2e7de9", "#9854f1", "#3760bf", "#6172b0", "#a8aecb", "#587539", "#8c6c3e", "#f52a65", "#e1e2e7", "#2e7de9"},
	},
	{
		"catppuccin",
		hexPalette{"#cba6f7", "#f5c2e7", "#cdd6f4", "#9399b2", "#45475a", "#a6e3a1", "#f9e2af", "#f38ba8", "#1e1e2e", "#cba6f7"},
		hexPalette{"#8839ef", "#ea76cb", "#4c4f69", "#7c7f93", "#bcc0cc", "#40a02b", "#df8e1d", "#d20f39", "#eff1f5", "#8839ef"},
	},
	{
		"gruvbox",
		hexPalette{"#fabd2f", "#d3869b", "#ebdbb2", "#a89984", "#504945", "#b8bb26", "#fe8019", "#fb4934", "#282828", "#fabd2f"},
		hexPalette{"#b57614", "#8f3f71", "#3c3836", "#7c6f64", "#bdae93", "#79740e", "#af3a03", "#9d0006", "#fbf1c7", "#b57614"},
	},
}

// themeNames lists every theme in cycling order.
func themeNames() []string {
	names := []string{"ansi"}
	for _, t := range builtinThemes {
		names = append(names, t.name)
	}
	return names
}

// buildTheme builds a theme by name for a known background, without
// touching the terminal. An unknown name falls back to ansi.
func buildTheme(name string, dark bool) Theme {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, t := range builtinThemes {
		if t.name == name {
			p := t.light
			if dark {
				p = t.dark
			}
			return newTheme(t.name, dark, p.palette())
		}
	}
	return newTheme("ansi", dark, ansiPalette(dark))
}

func nextTheme(t Theme) Theme {
	names := themeNames()
	i := slices.Index(names, t.Name)
	return buildTheme(names[(i+1)%len(names)], t.Dark)
}
