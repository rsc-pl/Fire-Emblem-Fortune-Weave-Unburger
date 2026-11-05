package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

func (m *Model) dashboardView() string {
	w, h := m.width, m.height
	if h <= 1 {
		return fitView(m.headerLine(), w, h)
	}
	compact := w < 72 || h < 24
	bordered := !compact && w >= 84 && h >= 30

	var blocks [][]string
	workspace := m.workspaceBody(w, bordered, compact)
	readiness := m.readinessBody(w, bordered, compact)
	if bordered && w >= 104 {
		leftW := w/2 - 1
		rightW := w - leftW - 1
		blocks = append(blocks, hjoin(
			m.panel("WORKSPACE", workspace, leftW, true),
			m.panel("READINESS", readiness, rightW, true),
			1,
		))
	} else {
		blocks = append(blocks, m.panel("WORKSPACE", workspace, w, bordered))
		blocks = append(blocks, m.panel("READINESS", readiness, w, bordered))
	}

	tableW := w
	if bordered {
		tableW = w - 4
	}
	m.syncOpsTable(tableW)
	var opBody []string
	if compact || w < 76 {
		opBody = m.compactOps(w)
	} else {
		opBody = strings.Split(m.ops.View(), "\n")
	}
	blocks = append(blocks, m.panel("OPERATIONS", opBody, w, bordered))
	blocks = append(blocks, m.panel("RECENT RESULTS", m.recentBody(w, compact), w, bordered))

	body := make([]string, 0, h)
	for i, block := range blocks {
		if i > 0 {
			body = append(body, "")
		}
		body = append(body, block...)
	}
	bodyText := fitView(strings.Join(body, "\n"), w, maxInt(0, h-2))
	return m.headerLine() + "\n" + bodyText + "\n" + m.footerLine(m.dashboardHints(compact))
}

func (m *Model) contentWidth(width int, bordered bool) int {
	if bordered {
		return maxInt(8, width-4)
	}
	return maxInt(8, width)
}

func (m *Model) workspaceBody(width int, bordered, compact bool) []string {
	cw := m.contentWidth(width, bordered)
	var lines []string

	root := truncateMiddle(m.root, maxInt(8, cw-7))
	lines = append(lines, m.st.Label.Render("root ")+m.st.Value.Render(root))

	if m.manErr != "" || m.man == nil {
		detail := "manifest unavailable"
		if m.manErr != "" {
			detail = strings.TrimPrefix(m.manErr, m.root+string(filepath.Separator))
			detail = truncateANSI(detail, maxInt(8, cw-18))
		}
		lines = append(lines, m.badge("FAIL")+" "+m.st.Value.Render(detail))
		return lines
	}

	c := m.man.Counts
	counts := fmt.Sprintf("%s files · %s records · %s real_en",
		comma(c.Files), comma(c.Records), comma(c.RealEN))
	lines = append(lines, m.badge("PASS")+" "+m.st.Value.Render(truncateANSI(counts, maxInt(8, cw-7))))

	if !compact {
		review := "review " + reviewSummary(c.ByReviewStatus)
		lines = append(lines, m.st.Muted.Render(truncateANSI(review, cw)))
		sha := "manifest sha256 " + truncateMiddle(m.manSHA, maxInt(8, cw-18))
		lines = append(lines, m.st.Muted.Render(sha))
	}
	return lines
}

func (m *Model) readinessBody(width int, bordered, compact bool) []string {
	cw := m.contentWidth(width, bordered)
	var lines []string

	scanStatus := m.scanReadiness()
	scanDetail := "not scanned yet"
	if m.lastScan != nil {
		scanDetail = fmt.Sprintf("%s records · %d issue(s) · %s",
			comma(m.lastScan.Records), scanIssues(m.lastScan), formatDuration(m.lastScan.Elapsed()))
	}
	lines = append(lines, m.readinessLine("canonical tree", scanStatus, scanDetail, cw))

	sourceDetail := "private banks available"
	if !m.source {
		sourceDetail = m.sourceErr
		if sourceDetail == "" {
			sourceDetail = "private banks not available"
		}
	}
	lines = append(lines, m.readinessLine("private source", m.sourceReadiness(), sourceDetail, cw))

	binaryStatus := "MISSING"
	binaryDetail := "not installed; run START.sh"
	if m.build.BinaryPresent {
		binaryStatus = "PRESENT"
		binaryDetail = humanBytes(m.build.BinarySize)
		if m.build.FPPresent {
			binaryDetail += " · fingerprint recorded"
		} else {
			binaryDetail += " · no fingerprint"
		}
	}
	if compact {
		lines = append(lines, m.st.Label.Render("binary ")+m.badge(binaryStatus)+"  "+
			m.st.Label.Render("corpus ")+m.badge(m.corpusReadiness()))
		return lines
	}
	lines = append(lines, m.readinessLine("console binary", binaryStatus, binaryDetail, cw))
	corpusDetail := "not run"
	if m.lastCorpus != nil {
		corpusDetail = truncateANSI(m.lastCorpus.Status(), 40)
		if m.lastCorpus.Elapsed() > 0 {
			corpusDetail += " · " + formatDuration(m.lastCorpus.Elapsed())
		}
	}
	lines = append(lines, m.readinessLine("corpus dry-run", m.corpusReadiness(), corpusDetail, cw))
	return lines
}

func (m *Model) readinessLine(label, status, detail string, width int) string {
	left := m.st.Label.Render(padRight(label, 15)) + " " + m.badge(status)
	if detail == "" {
		return truncateANSI(left, width)
	}
	line := left + "  " + m.st.Muted.Render(detail)
	return truncateANSI(line, width)
}

func (m *Model) compactOps(width int) []string {
	rows := m.opRows()
	cursor := 0
	if m.ops != nil {
		cursor = m.ops.Cursor()
	}
	out := make([]string, 0, len(rows))
	for i, row := range rows {
		marker := "  "
		if i == cursor {
			marker = m.st.Accent.Render("▸ ")
		}
		line := marker + m.st.Value.Render(row[0]) + "  " + m.badge(row[2])
		if width >= 66 && row[1] != "" {
			line += m.st.Muted.Render("  " + row[1])
		}
		out = append(out, truncateANSI(line, width))
	}
	return out
}

func (m *Model) recentBody(width int, compact bool) []string {
	if m.lastScan == nil && m.lastCorpus == nil {
		lines := []string{m.st.Muted.Render("No operations run yet.")}
		if !compact {
			lines = append(lines, m.st.Muted.Render("Select an operation above and press Enter."))
		}
		return lines
	}

	if compact {
		parts := []string{"tree " + m.badge(m.scanReadiness())}
		if m.lastCorpus != nil {
			parts = append(parts, "corpus "+m.badge(m.lastCorpus.Status()))
		}
		return []string{truncateANSI(strings.Join(parts, " · "), width)}
	}

	var lines []string
	if m.lastScan != nil {
		detail := fmt.Sprintf("%s · %s · %d issue(s)",
			m.lastScanAt.Format("15:04:05"), formatDuration(m.lastScan.Elapsed()), scanIssues(m.lastScan))
		lines = append(lines, m.recentLine("tree scan", m.scanReadiness(), detail, width))
	}
	if m.lastCorpus != nil {
		detail := fmt.Sprintf("%s · %s", m.lastCorpusAt.Format("15:04:05"), formatDuration(m.lastCorpus.Elapsed()))
		if m.lastCorpus.Skipped {
			detail = m.lastCorpusAt.Format("15:04:05") + " · not staged"
		}
		lines = append(lines, m.recentLine("corpus dry-run", m.lastCorpus.Status(), detail, width))
	}
	return lines
}

func (m *Model) recentLine(label, status, detail string, width int) string {
	line := m.st.Label.Render(padRight(label, 14)) + " " + m.badge(status) + "  " + m.st.Muted.Render(detail)
	return truncateANSI(line, width)
}

func (m *Model) dashboardHints(compact bool) string {
	if compact {
		return m.hintLine(
			m.hint("q", "quit"),
			m.hint("↑/↓", "select"),
			m.hint("enter", "run"),
			m.hint("r", "scan"),
			m.hint("v", "dry-run"),
			m.hint("d", "results"),
			m.hint("?", "help"),
		)
	}
	if m.width < 96 {
		return m.hintLine(
			m.hint("q", "quit"),
			m.hint("↑/↓", "select"),
			m.hint("enter", "run"),
			m.hint("r", "scan"),
			m.hint("v", "dry-run"),
			m.hint("d", "results"),
			m.hint("?", "help"),
		)
	}
	return m.hintLine(
		m.hint("q", "quit"),
		m.hint("↑/↓", "select"),
		m.hint("enter", "run"),
		m.hint("r", "scan"),
		m.hint("v", "dry-run"),
		m.hint("d", "diagnostics"),
		m.hint("b", "build"),
		m.hint("?", "help"),
	)
}

func scanIssues(r *scanResult) int {
	if r == nil {
		return 0
	}
	return r.HashMismatchCount + r.CountMismatchCount + r.MissingCount +
		r.ExtraCount + r.SchemaErrorCount + r.DuplicateIDCount +
		r.SymlinkCount + r.ReadErrorCount
}
