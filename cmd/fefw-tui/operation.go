package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type opEventKind int

const (
	opEventScanProgress opEventKind = iota
	opEventScanDone
	opEventCorpusPhase
	opEventCorpusDone
)

type opEvent struct {
	Token    int
	Kind     opEventKind
	Progress scanProgress
	Scan     *scanResult
	ScanErr  error
	Phase    string
	Corpus   *corpusResult
}

type tickMsg struct {
	token int
	at    time.Time
}

func tickCmd(token int) tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg{token: token, at: t}
	})
}

func waitOpEvent(ch <-chan opEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return ev
	}
}

func emitOpEvent(ctx context.Context, ch chan<- opEvent, ev opEvent) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

func (m *Model) startScan() tea.Cmd {
	if m.opRunning {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.opToken++
	token := m.opToken
	ch := make(chan opEvent, 128)
	m.events = ch
	m.cancel = contextCancel(cancel)
	m.opKind = opScan
	m.opRunning = true
	m.opStarted = time.Now()
	m.scanProg = scanProgress{}
	m.scanRes = nil
	m.scanErr = nil
	m.notice = ""
	m.screen = screenOperation

	root := m.root
	go func() {
		var last time.Time
		res, err := scanCanonicalContext(ctx, root, func(p scanProgress) {
			if p.Done != p.Total && time.Since(last) < 60*time.Millisecond {
				return
			}
			last = time.Now()
			emitOpEvent(ctx, ch, opEvent{Token: token, Kind: opEventScanProgress, Progress: p})
		})
		emitOpEvent(ctx, ch, opEvent{Token: token, Kind: opEventScanDone, Scan: res, ScanErr: err})
		cancel()
	}()

	return tea.Batch(waitOpEvent(ch), m.spinner.Tick, tickCmd(token))
}

func (m *Model) startCorpus() tea.Cmd {
	if m.opRunning {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.opToken++
	token := m.opToken
	ch := make(chan opEvent, 64)
	m.events = ch
	m.cancel = contextCancel(cancel)
	m.opKind = opCorpus
	m.opRunning = true
	m.opStarted = time.Now()
	m.corpusPhase = "checking private source"
	m.corpusRes = nil
	m.notice = ""
	m.screen = screenOperation

	root := m.root
	go func() {
		res := runCorpusDryRunContext(ctx, root, func(phase string) {
			emitOpEvent(ctx, ch, opEvent{Token: token, Kind: opEventCorpusPhase, Phase: phase})
		})
		emitOpEvent(ctx, ch, opEvent{Token: token, Kind: opEventCorpusDone, Corpus: res})
		cancel()
	}()

	return tea.Batch(waitOpEvent(ch), m.spinner.Tick, tickCmd(token))
}

func (m *Model) cancelOp() {
	if !m.opRunning {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.opToken++
	m.opRunning = false
	m.opKind = opNone
	m.cancel = nil
	m.events = nil
	m.notice = "operation canceled"
	m.screen = screenDashboard
}

func (m *Model) handleOpEvent(ev opEvent) tea.Cmd {
	if ev.Token != m.opToken || !m.opRunning {
		return nil
	}
	switch ev.Kind {
	case opEventScanProgress:
		m.scanProg = ev.Progress
		return waitOpEvent(m.events)
	case opEventCorpusPhase:
		m.corpusPhase = ev.Phase
		return waitOpEvent(m.events)
	case opEventScanDone:
		m.opRunning = false
		m.scanRes = ev.Scan
		m.scanErr = ev.ScanErr
		m.cancel = nil
		m.events = nil
		if ev.ScanErr == nil && ev.Scan != nil {
			m.lastScan = ev.Scan
			m.lastScanAt = time.Now()
		}
		return nil
	case opEventCorpusDone:
		m.opRunning = false
		m.corpusRes = ev.Corpus
		m.cancel = nil
		m.events = nil
		if ev.Corpus != nil {
			m.lastCorpus = ev.Corpus
			m.lastCorpusAt = time.Now()
		}
		return nil
	}
	return nil
}

func (m *Model) operationView() string {
	w, h := m.width, m.height
	if h <= 1 {
		return fitView(m.headerLine(), w, h)
	}
	title := m.operationTitle()
	status := m.operationStatus()

	lines := []string{
		"",
		m.st.Title.Render(title) + "  " + m.badge(status),
		m.st.Muted.Render("Read-only operation · no files are written."),
		"",
	}
	if m.opRunning {
		lines = append(lines, m.runningBody(w)...)
	} else {
		lines = append(lines, m.finishedBody(w)...)
	}
	body := fitView(strings.Join(lines, "\n"), w, maxInt(0, h-2))
	return m.headerLine() + "\n" + body + "\n" + m.footerLine(m.operationHints())
}

func (m *Model) operationTitle() string {
	switch m.opKind {
	case opScan:
		return "TREE SCAN"
	case opCorpus:
		return "CORPUS DRY-RUN"
	default:
		return "OPERATION"
	}
}

func (m *Model) operationStatus() string {
	if m.opRunning {
		return "RUNNING"
	}
	switch m.opKind {
	case opScan:
		if m.scanErr != nil {
			return "ERROR"
		}
		if m.scanRes == nil {
			return "IDLE"
		}
		if m.scanRes.OK() {
			return "PASS"
		}
		return "FAIL"
	case opCorpus:
		if m.corpusRes == nil {
			return "IDLE"
		}
		return m.corpusRes.Status()
	default:
		return "IDLE"
	}
}

func (m *Model) runningBody(width int) []string {
	if m.opKind == opCorpus {
		return m.corpusRunningBody(width)
	}
	return m.scanRunningBody(width)
}

func (m *Model) scanRunningBody(width int) []string {
	cw := maxInt(12, width-2)
	p := m.scanProg
	var lines []string

	lines = append(lines, m.spinner.View()+" "+m.st.Value.Render(
		truncateANSI("Verifying manifest hashes, record counts and shard layout", maxInt(10, cw-2))))

	if p.Total <= 0 {
		lines = append(lines, m.st.Muted.Render("Preparing scan…"))
		return lines
	}

	pct := float64(p.Done) / float64(p.Total)
	barWidth := clampInt(cw-12, 10, 44)
	bar := asciiProgress(p.Done, p.Total, barWidth)
	if !m.noColor {
		m.progress.Width = barWidth
		bar = m.progress.ViewAs(pct) + fmt.Sprintf(" %3d%%", int(pct*100))
	}
	lines = append(lines, "  "+truncateANSI(bar, cw-2))
	lines = append(lines, "  "+m.st.Value.Render(fmt.Sprintf("files %s / %s · records %s",
		comma(p.Done), comma(p.Total), comma(p.Records))))

	current := "waiting for the first file"
	if p.Current != "" {
		current = truncateMiddle(p.Current, maxInt(10, cw-10))
	}
	lines = append(lines, "  "+m.st.Muted.Render("current "+current))

	elapsed := m.opElapsed()
	timing := "elapsed " + formatDuration(elapsed)
	if p.Done >= 2 && elapsed >= 500*time.Millisecond {
		rate := float64(p.Done) / elapsed.Seconds()
		timing += fmt.Sprintf(" · %.1f files/s", rate)
		if p.Done < p.Total {
			remaining := time.Duration(float64(p.Total-p.Done) / rate * float64(time.Second))
			timing += " · est. remaining " + formatDuration(remaining)
		}
	}
	lines = append(lines, "  "+m.st.Muted.Render(timing))
	return lines
}

func (m *Model) corpusRunningBody(width int) []string {
	cw := maxInt(12, width-2)
	phase := m.corpusPhase
	if phase == "" {
		phase = "working"
	}
	var lines []string
	lines = append(lines, m.spinner.View()+" "+m.st.Value.Render(truncateANSI(phase, maxInt(10, cw-2))))
	lines = append(lines, "")

	steps := []string{
		"Check private source",
		"Load and decode message banks",
		"Build bank pairs in memory",
		"Validate shard fidelity and record ids",
	}
	current := corpusPhaseIndex(m.corpusPhase)
	for i, step := range steps {
		prefix := m.st.Muted.Render("·")
		style := m.st.Muted
		switch {
		case i < current:
			prefix = m.st.Success.Render("✓")
			style = m.st.Body
		case i == current:
			prefix = m.st.Accent.Render("▸")
			style = m.st.Value
		}
		lines = append(lines, "  "+prefix+" "+style.Render(truncateANSI(step, maxInt(10, cw-6))))
	}
	lines = append(lines, "")
	lines = append(lines, "  "+m.st.Muted.Render("elapsed "+formatDuration(m.opElapsed())))
	lines = append(lines, "  "+m.st.Muted.Render("Record counts appear when the in-memory build completes."))
	return lines
}

func corpusPhaseIndex(phase string) int {
	switch {
	case strings.Contains(phase, "loading"):
		return 1
	case strings.Contains(phase, "building"):
		return 2
	case strings.Contains(phase, "validating"):
		return 3
	default:
		return 0
	}
}

func (m *Model) finishedBody(width int) []string {
	if m.opKind == opCorpus {
		return m.corpusFinishedBody(width)
	}
	return m.scanFinishedBody(width)
}

func (m *Model) scanFinishedBody(width int) []string {
	cw := maxInt(12, width-2)
	var lines []string
	if m.scanErr != nil {
		lines = append(lines, m.badge("FAIL")+" "+m.st.Failure.Render(truncateANSI(m.scanErr.Error(), cw-7)))
		lines = append(lines, "")
		lines = append(lines, m.st.Muted.Render("Press d for the diagnostic report or esc to return."))
		return lines
	}
	r := m.scanRes
	if r == nil {
		lines = append(lines, m.st.Muted.Render("No result available."))
		return lines
	}

	issues := scanIssues(r)
	failedChecks := r.HashMismatchCount + r.CountMismatchCount + r.SchemaErrorCount +
		r.DuplicateIDCount + r.ExtraCount
	skipped := r.MissingCount + r.ReadErrorCount + r.SymlinkCount

	lines = append(lines,
		m.metric("files", fmt.Sprintf("%s verified", comma(r.Files)), cw),
		m.metric("records", fmt.Sprintf("%s read", comma(r.Records)), cw),
		m.metric("bytes", humanBytes(r.CheckedBytes), cw),
		m.metric("manifest", map[bool]string{true: "digest matches", false: "DIGEST MISMATCH"}[r.ManifestSHA == r.Recomputed], cw),
		m.metric("elapsed", formatDuration(r.Elapsed()), cw),
		"",
		m.metric("outcome", fmt.Sprintf("success %s files · failed %d checks · skipped %d files",
			comma(r.Files), failedChecks, skipped), cw),
	)
	if issues > 0 {
		lines = append(lines, "")
		lines = append(lines, m.st.Warning.Render(fmt.Sprintf("Findings (%d)", issues)))
		lines = append(lines, m.scanDetailLines(width)...)
		lines = append(lines, m.st.Muted.Render("Press d for the complete diagnostic report."))
	} else {
		lines = append(lines, "")
		lines = append(lines, m.st.Success.Render("All manifest, hash, schema and layout checks passed."))
	}
	return lines
}

func (m *Model) scanDetailLines(width int) []string {
	r := m.scanRes
	if r == nil {
		return nil
	}
	cw := maxInt(12, width-4)
	type category struct {
		label string
		items []string
		count int
	}
	categories := []category{
		{"missing", r.Missing, r.MissingCount},
		{"hash", r.HashMismatch, r.HashMismatchCount},
		{"count", r.CountMismatch, r.CountMismatchCount},
		{"schema", r.SchemaErrors, r.SchemaErrorCount},
		{"duplicate", r.DuplicateIDs, r.DuplicateIDCount},
		{"extra", r.Extra, r.ExtraCount},
		{"symlink", r.Symlinks, r.SymlinkCount},
		{"read", r.ReadErrors, r.ReadErrorCount},
	}
	var lines []string
	shown := 0
	for _, c := range categories {
		if c.count == 0 {
			continue
		}
		limit := minInt(len(c.items), 2)
		for i := 0; i < limit && shown < 6; i++ {
			lines = append(lines, "  "+m.st.Warning.Render(truncateANSI(c.label+": "+c.items[i], cw)))
			shown++
		}
	}
	if shown == 0 && scanIssues(r) > 0 {
		lines = append(lines, "  "+m.st.Muted.Render("Details are available in diagnostics."))
	}
	return lines
}

func (m *Model) corpusFinishedBody(width int) []string {
	cw := maxInt(12, width-2)
	r := m.corpusRes
	if r == nil {
		return []string{m.st.Muted.Render("No result available.")}
	}
	if r.Skipped {
		lines := []string{
			m.st.Warning.Render("Corpus dry-run skipped."),
			"",
			m.st.Value.Render(truncateANSI(r.Reason, cw)),
			"",
			m.st.Muted.Render("The operation needs the private staged message banks; nothing was read or written."),
		}
		return lines
	}
	if r.Error != "" {
		return []string{
			m.st.Failure.Render(truncateANSI(r.Error, cw)),
			"",
			m.st.Muted.Render("Press d for the diagnostic report or esc to return."),
		}
	}

	passed, failed := checkCounts(r.Checks)
	var lines []string
	lines = append(lines,
		m.metric("banks", fmt.Sprintf("%s loaded", comma(r.Banks)), cw),
		m.metric("in-memory", fmt.Sprintf("%s files · %s records · %s real_en",
			comma(r.BuiltFiles), comma(r.BuiltRecords), comma(r.BuiltRealEN)), cw),
		m.metric("on disk", fmt.Sprintf("%s files · %s records · %s real_en",
			comma(r.DiskFiles), comma(r.DiskRecords), comma(r.DiskRealEN)), cw),
		m.metric("validator", fmt.Sprintf("%s · %s", r.ValidatorStatus, checksSummary(r.Checks)), cw),
		m.metric("elapsed", formatDuration(r.Elapsed()), cw),
		"",
		m.metric("outcome", fmt.Sprintf("success %d checks · failed %d checks · skipped 0", passed, failed), cw),
	)
	if r.GuardErrors > 0 {
		lines = append(lines, "", m.st.Warning.Render(fmt.Sprintf(
			"Editorial guard: %s records already carry real_en or a reviewed status (expected outside an initial import).",
			comma(r.GuardErrors))))
	}
	if r.OtherErrors > 0 {
		lines = append(lines, m.st.Failure.Render(fmt.Sprintf("%s other validation error(s).", comma(r.OtherErrors))))
	}
	if r.BuiltFiles != r.DiskFiles {
		lines = append(lines, "", m.st.Muted.Render(fmt.Sprintf(
			"Layout note: in-memory build produced %s bank shards; canonical tree has %s shards.",
			comma(r.BuiltFiles), comma(r.DiskFiles))))
	}
	return lines
}

func (m *Model) metric(label, value string, width int) string {
	line := m.st.Label.Render(padRight(label, 10)) + " " + m.st.Value.Render(value)
	return truncateANSI(line, width)
}

func (m *Model) operationHints() string {
	if m.opRunning {
		return m.hintLine(
			m.hint("esc", "cancel"),
			m.hint("q", "quit"),
		)
	}
	return m.hintLine(
		m.hint("enter", "dashboard"),
		m.hint("d", "diagnostics"),
		m.hint("r", "scan"),
		m.hint("v", "dry-run"),
		m.hint("q", "quit"),
	)
}
