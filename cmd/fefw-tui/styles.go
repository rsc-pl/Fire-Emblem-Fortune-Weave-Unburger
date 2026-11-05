package main

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// palette holds the semantic colours used by the console. A single palette is
// selected at start-up; no terminal background query is performed.
type palette struct {
	Foreground string
	Muted      string
	Accent     string
	AccentAlt  string
	Success    string
	Warning    string
	Failure    string
	Inactive   string
	Border     string
}

func darkPalette() palette {
	return palette{
		Foreground: "#d8dee9",
		Muted:      "#8b96a8",
		Accent:     "#5fd7ff",
		AccentAlt:  "#c792ea",
		Success:    "#6fdc6f",
		Warning:    "#ffd75f",
		Failure:    "#ff6b6b",
		Inactive:   "#6b7482",
		Border:     "#3b4252",
	}
}

func lightPalette() palette {
	return palette{
		Foreground: "#1f2430",
		Muted:      "#5b6575",
		Accent:     "#006d8f",
		AccentAlt:  "#7b3fa0",
		Success:    "#1f7a1f",
		Warning:    "#8a5a00",
		Failure:    "#b3261e",
		Inactive:   "#78828f",
		Border:     "#b9c1cc",
	}
}

// uiStyles is the complete style set for the interface. Every style is built
// once so views never construct ad-hoc styles in the render path.
type uiStyles struct {
	Brand    lipgloss.Style
	Title    lipgloss.Style
	Subtitle lipgloss.Style
	Body     lipgloss.Style
	Muted    lipgloss.Style
	Label    lipgloss.Style
	Value    lipgloss.Style
	Accent   lipgloss.Style
	Success  lipgloss.Style
	Warning  lipgloss.Style
	Failure  lipgloss.Style
	Inactive lipgloss.Style
	Section  lipgloss.Style
	Panel    lipgloss.Style
	PanelKey lipgloss.Style
	Footer   lipgloss.Style
	Key      lipgloss.Style

	TableHeader   lipgloss.Style
	TableCell     lipgloss.Style
	TableSelected lipgloss.Style

	Spinner lipgloss.Style
}

func newUIStyles(color bool, light bool) uiStyles {
	plain := lipgloss.NewStyle()
	if !color {
		return uiStyles{
			Brand: plain, Title: plain, Subtitle: plain, Body: plain,
			Muted: plain, Label: plain, Value: plain, Accent: plain,
			Success: plain, Warning: plain, Failure: plain, Inactive: plain,
			Section: plain, Panel: plain, PanelKey: plain, Footer: plain,
			Key: plain, TableHeader: plain, TableCell: plain,
			TableSelected: plain, Spinner: plain,
		}
	}

	p := darkPalette()
	if light {
		p = lightPalette()
	}
	c := func(hex string) lipgloss.Color { return lipgloss.Color(hex) }

	return uiStyles{
		Brand:    lipgloss.NewStyle().Bold(true).Foreground(c(p.Accent)),
		Title:    lipgloss.NewStyle().Bold(true).Foreground(c(p.Foreground)),
		Subtitle: lipgloss.NewStyle().Foreground(c(p.Muted)),
		Body:     lipgloss.NewStyle().Foreground(c(p.Foreground)),
		Muted:    lipgloss.NewStyle().Foreground(c(p.Muted)),
		Label:    lipgloss.NewStyle().Bold(true).Foreground(c(p.Muted)),
		Value:    lipgloss.NewStyle().Foreground(c(p.Foreground)),
		Accent:   lipgloss.NewStyle().Foreground(c(p.Accent)),
		Success:  lipgloss.NewStyle().Foreground(c(p.Success)),
		Warning:  lipgloss.NewStyle().Foreground(c(p.Warning)),
		Failure:  lipgloss.NewStyle().Foreground(c(p.Failure)),
		Inactive: lipgloss.NewStyle().Foreground(c(p.Inactive)),
		Section:  lipgloss.NewStyle().Bold(true).Foreground(c(p.Accent)),
		Panel:    lipgloss.NewStyle().Foreground(c(p.Border)),
		PanelKey: lipgloss.NewStyle().Bold(true).Foreground(c(p.Accent)),
		Footer:   lipgloss.NewStyle().Foreground(c(p.Muted)),
		Key:      lipgloss.NewStyle().Bold(true).Foreground(c(p.AccentAlt)),

		TableHeader:   lipgloss.NewStyle().Bold(true).Foreground(c(p.Accent)),
		TableCell:     lipgloss.NewStyle().Foreground(c(p.Foreground)).Padding(0, 1),
		TableSelected: lipgloss.NewStyle().Bold(true).Foreground(c(p.AccentAlt)).Padding(0, 0),

		Spinner: lipgloss.NewStyle().Foreground(c(p.Accent)),
	}
}

func (p palette) accentColor() lipgloss.Color { return lipgloss.Color(p.Accent) }

// detectLightTheme selects a readable palette without querying the terminal.
// FEFW_THEME wins; COLORFGBG is a common hint emitted by terminal emulators.
func detectLightTheme() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FEFW_THEME"))) {
	case "light":
		return true
	case "dark":
		return false
	}
	fgbg := strings.TrimSpace(os.Getenv("COLORFGBG"))
	if fgbg != "" {
		parts := strings.Split(fgbg, ";")
		switch strings.TrimSpace(parts[len(parts)-1]) {
		case "7", "15", "default":
			return true
		}
	}
	return false
}
