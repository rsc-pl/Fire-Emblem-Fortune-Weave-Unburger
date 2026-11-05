package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func (m *Model) resultsView() string {
	w, h := m.width, m.height
	if h <= 1 {
		return fitView(m.headerLine(), w, h)
	}
	viewportHeight := h - 3
	if m.filterActive {
		viewportHeight = h - 4
	}
	m.viewport.Width = maxInt(1, w)
	m.viewport.Height = maxInt(1, viewportHeight)

	left := m.st.Title.Render("DIAGNOSTICS")
	meta := fmt.Sprintf("%d/%d lines", len(m.resultsLines), len(m.resultsAll))
	if m.resultsQuery != "" {
		meta += " · filter: " + m.resultsQuery
	}
	right := m.st.Muted.Render(meta)
	titleLine := left
	if ansi.StringWidth(left)+ansi.StringWidth(right)+1 <= w {
		titleLine = left + strings.Repeat(" ", w-ansi.StringWidth(left)-ansi.StringWidth(right)) + right
	} else {
		titleLine = truncateANSI(left+"  "+right, w)
	}

	lines := []string{m.headerLine(), titleLine}
	if m.filterActive {
		lines = append(lines, truncateANSI("  "+m.filter.View(), w))
	}
	lines = append(lines, strings.Split(m.viewport.View(), "\n")...)
	lines = append(lines, m.footerLine(m.resultsHints()))
	return fitView(strings.Join(lines, "\n"), w, h)
}

func (m *Model) resultsHints() string {
	if m.filterActive {
		return m.hintLine(
			m.hint("enter", "apply"),
			m.hint("esc", "clear"),
			m.hint("type", "filter"),
		)
	}
	if m.width < 70 {
		return m.hintLine(
			m.hint("j/k", "scroll"),
			m.hint("/", "filter"),
			m.hint("esc", "back"),
			m.hint("q", "quit"),
		)
	}
	return m.hintLine(
		m.hint("j/k", "scroll"),
		m.hint("PgUp/PgDn", "page"),
		m.hint("g/G", "ends"),
		m.hint("/", "filter"),
		m.hint("esc", "back"),
		m.hint("q", "quit"),
	)
}

func (m *Model) helpView() string {
	w, h := m.width, m.height
	if h <= 1 {
		return fitView(m.headerLine(), w, h)
	}
	lines := []string{
		"",
		m.st.Title.Render("HELP") + "  " + m.st.Muted.Render("read-only console"),
		"",
		m.section("keys", w),
	}
	keys := [][2]string{
		{"q / ctrl+c", "quit and restore the terminal"},
		{"↑/↓ or j/k", "move in lists, scroll one line"},
		{"enter", "run the selected operation, open a result or return"},
		{"r", "rescan the canonical translation tree"},
		{"v", "run the in-memory corpus dry-run"},
		{"d", "open diagnostics and build details"},
		{"b", "refresh build and launcher fingerprint state"},
		{"PgUp / PgDn", "scroll diagnostics by one page"},
		{"g / G", "jump to the top or bottom"},
		{"/", "filter diagnostics (enter applies, esc clears)"},
		{"esc", "cancel a running operation or go back"},
		{"?", "toggle this help"},
	}
	for _, pair := range keys {
		line := "  " + m.st.Key.Render(padRight(pair[0], 14)) + " " + m.st.Body.Render(pair[1])
		lines = append(lines, truncateANSI(line, w))
	}
	lines = append(lines, "")
	lines = append(lines, m.section("safety", w))
	safety := []string{
		"Inspection never writes to translations, glossary, schemas or reports.",
		"The corpus dry-run loads private staged banks only when they are present.",
		"Diagnostics report measured counts and timings; unknown totals stay indeterminate.",
	}
	for _, line := range safety {
		lines = append(lines, "  "+m.st.Muted.Render(truncateANSI(line, maxInt(8, w-2))))
	}
	lines = append(lines, "")
	lines = append(lines, m.section("display", w))
	display := []string{
		"FEFW_THEME=light|dark selects a palette; otherwise COLORFGBG is used.",
		"NO_COLOR=1 or --no-color disables ANSI styling.",
		"Non-TTY output and --plain print the text report instead of the console.",
	}
	for _, line := range display {
		lines = append(lines, "  "+m.st.Muted.Render(truncateANSI(line, maxInt(8, w-2))))
	}

	body := fitView(strings.Join(lines, "\n"), w, maxInt(0, h-2))
	return m.headerLine() + "\n" + body + "\n" + m.footerLine(m.hintLine(
		m.hint("esc", "back"),
		m.hint("?", "close"),
		m.hint("q", "quit"),
	))
}
