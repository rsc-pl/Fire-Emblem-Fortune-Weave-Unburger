package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"fefw/internal/importer"
)

type screen int

const (
	screenDashboard screen = iota
	screenOperation
	screenResults
	screenHelp
)

type opKind int

const (
	opNone opKind = iota
	opScan
	opCorpus
)

type uiOptions struct {
	Color   bool
	Light   bool
	NoColor bool
}

// Model is the Bubble Tea application state. It contains no long-running work:
// operations run in commands and report back as messages.
type Model struct {
	root   string
	width  int
	height int
	screen screen

	man       *importer.Manifest
	manSHA    string
	manErr    string
	build     buildInfo
	source    bool
	sourceErr string

	ops *table.Model

	spinner  spinner.Model
	progress progress.Model
	viewport viewport.Model
	filter   textinput.Model

	opKind      opKind
	opRunning   bool
	opToken     int
	opStarted   time.Time
	scanProg    scanProgress
	scanRes     *scanResult
	scanErr     error
	corpusPhase string
	corpusRes   *corpusResult
	events      chan opEvent
	cancel      contextCancel

	lastScan     *scanResult
	lastScanAt   time.Time
	lastCorpus   *corpusResult
	lastCorpusAt time.Time

	resultsAll   []string
	resultsLines []string
	resultsQuery string
	filterActive bool

	helpReturn screen
	notice     string

	noColor bool
	palette palette
	st      uiStyles

	exitCode int
}

// contextCancel keeps the model free of a context import in most files while
// still allowing an operation to be stopped cooperatively.
type contextCancel func()

func newModel(root string, man *importer.Manifest, manSHA, manErr string, opts uiOptions) *Model {
	m := &Model{
		root:    root,
		width:   80,
		height:  24,
		screen:  screenDashboard,
		man:     man,
		manSHA:  manSHA,
		manErr:  manErr,
		noColor: opts.NoColor || !opts.Color,
		palette: darkPalette(),
	}
	if opts.Light {
		m.palette = lightPalette()
	}
	m.st = newUIStyles(!m.noColor, opts.Light)
	m.build = inspectBuild(root)
	m.source, m.sourceErr = stagedSourceStatus(root)

	m.spinner = spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(m.st.Spinner),
	)
	m.progress = progress.New(
		progress.WithSolidFill(m.palette.Accent),
		progress.WithWidth(32),
		progress.WithoutPercentage(),
	)
	m.viewport = viewport.New(78, 18)
	m.filter = textinput.New()
	m.filter.Prompt = "/ "
	m.filter.Placeholder = "filter diagnostics"
	m.filter.CharLimit = 80
	m.filter.Width = 40
	m.filter.PromptStyle = m.st.Accent
	m.filter.TextStyle = m.st.Value
	m.filter.PlaceholderStyle = m.st.Muted
	m.filter.Cursor.Style = m.st.Value
	if m.noColor {
		m.filter.Cursor.SetMode(cursor.CursorHide)
	} else {
		m.filter.Cursor.SetMode(cursor.CursorStatic)
	}

	m.ops = newOpsTable(m.st)
	m.syncOpsTable(80)
	m.resize(80, 24)
	return m
}

func newOpsTable(st uiStyles) *table.Model {
	t := table.New(
		table.WithFocused(true),
		table.WithStyles(table.Styles{
			Header:   st.TableHeader,
			Cell:     st.TableCell,
			Selected: st.TableSelected,
		}),
	)
	return &t
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		return m, m.handleKey(msg)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.opRunning {
			return m, cmd
		}
		return m, nil
	case opEvent:
		return m, m.handleOpEvent(msg)
	case tickMsg:
		if msg.token == m.opToken && m.opRunning {
			return m, tickCmd(msg.token)
		}
		return m, nil
	case tea.InterruptMsg:
		m.exitCode = 130
		return m, m.quit()
	}
	return m, nil
}

func (m *Model) View() string {
	switch m.screen {
	case screenOperation:
		return m.operationView()
	case screenResults:
		return m.resultsView()
	case screenHelp:
		return m.helpView()
	default:
		return m.dashboardView()
	}
}

func (m *Model) resize(w, h int) {
	m.width = maxInt(1, w)
	m.height = maxInt(1, h)
	m.progress.Width = clampInt(m.width-18, 8, 44)
	m.viewport.Width = maxInt(1, m.width-2)
	m.viewport.Height = maxInt(1, m.height-4)
	m.filter.Width = maxInt(10, m.width-14)
	m.syncOpsTable(maxInt(10, m.width-6))
	if m.screen == screenResults {
		m.rebuildResults()
	}
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()

	if key == "ctrl+c" {
		m.exitCode = 130
		return m.quit()
	}
	if m.screen == screenResults && m.filterActive {
		return m.handleFilterKey(msg)
	}
	if key == "q" {
		return m.quit()
	}

	switch m.screen {
	case screenDashboard:
		return m.handleDashboardKey(key)
	case screenOperation:
		return m.handleOperationKey(key)
	case screenResults:
		return m.handleResultsKey(key)
	case screenHelp:
		return m.handleHelpKey(key)
	}
	return nil
}

func (m *Model) handleDashboardKey(key string) tea.Cmd {
	switch key {
	case "up", "k":
		m.ops.MoveUp(1)
	case "down", "j":
		m.ops.MoveDown(1)
	case "home", "g":
		m.ops.GotoTop()
	case "end", "G":
		m.ops.GotoBottom()
	case "enter":
		return m.runSelected()
	case "r":
		return m.startScan()
	case "v":
		return m.startCorpus()
	case "d":
		m.openResults()
	case "b":
		m.build = inspectBuild(m.root)
		m.source, m.sourceErr = stagedSourceStatus(m.root)
		m.notice = "build state refreshed"
	case "?":
		m.openHelp()
	}
	return nil
}

func (m *Model) handleOperationKey(key string) tea.Cmd {
	switch key {
	case "esc":
		if m.opRunning {
			m.cancelOp()
			return nil
		}
		m.screen = screenDashboard
	case "enter":
		if !m.opRunning {
			m.screen = screenDashboard
		}
	case "d":
		if !m.opRunning {
			m.openResults()
		}
	case "r":
		if !m.opRunning {
			return m.startScan()
		}
	case "v":
		if !m.opRunning {
			return m.startCorpus()
		}
	case "?":
		if !m.opRunning {
			m.openHelp()
		}
	}
	return nil
}

func (m *Model) handleResultsKey(key string) tea.Cmd {
	switch key {
	case "esc":
		if m.resultsQuery != "" {
			m.resultsQuery = ""
			m.filter.SetValue("")
			m.rebuildResults()
			return nil
		}
		m.screen = screenDashboard
	case "/":
		m.filterActive = true
		m.filter.SetValue(m.resultsQuery)
		m.filter.Focus()
		return textinput.Blink
	case "?":
		m.openHelp()
	case "up", "k":
		m.viewport.LineUp(1)
	case "down", "j":
		m.viewport.LineDown(1)
	case "pgup", "b":
		m.viewport.ViewUp()
	case "pgdown", "f", " ":
		m.viewport.ViewDown()
	case "home", "g":
		m.viewport.GotoTop()
	case "end", "G":
		m.viewport.GotoBottom()
	}
	return nil
}

func (m *Model) handleFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.filterActive = false
		m.filter.Blur()
		m.filter.SetValue("")
		m.resultsQuery = ""
		m.rebuildResults()
		return nil
	case "enter":
		m.filterActive = false
		m.filter.Blur()
		m.resultsQuery = strings.TrimSpace(m.filter.Value())
		m.rebuildResults()
		return nil
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.resultsQuery = strings.TrimSpace(m.filter.Value())
	m.rebuildResults()
	return cmd
}

func (m *Model) handleHelpKey(key string) tea.Cmd {
	switch key {
	case "esc", "enter", "?":
		if m.helpReturn == screenHelp {
			m.helpReturn = screenDashboard
		}
		m.screen = m.helpReturn
	}
	return nil
}

func (m *Model) openHelp() {
	if m.screen == screenHelp {
		return
	}
	m.helpReturn = m.screen
	m.screen = screenHelp
}

func (m *Model) quit() tea.Cmd {
	if m.opRunning {
		m.cancelOp()
	}
	return tea.Quit
}

func (m *Model) runSelected() tea.Cmd {
	if m.ops == nil {
		return nil
	}
	switch m.ops.Cursor() {
	case 0:
		return m.startScan()
	case 1:
		return m.startCorpus()
	case 2:
		m.openResults()
	case 3:
		m.build = inspectBuild(m.root)
		m.source, m.sourceErr = stagedSourceStatus(m.root)
		m.notice = "build state refreshed"
	case 4:
		m.openHelp()
	}
	return nil
}

func (m *Model) openResults() {
	m.build = inspectBuild(m.root)
	m.source, m.sourceErr = stagedSourceStatus(m.root)
	m.resultsAll = strings.Split(plainReportWithCorpus(m.root, m.man, m.manSHA, m.lastScan, m.lastCorpus, m.build), "\n")
	m.resultsQuery = ""
	m.filterActive = false
	m.filter.Blur()
	m.filter.SetValue("")
	m.rebuildResults()
	m.viewport.GotoTop()
	m.screen = screenResults
}

func (m *Model) rebuildResults() {
	query := strings.ToLower(strings.TrimSpace(m.resultsQuery))
	lines := make([]string, 0, len(m.resultsAll))
	for _, line := range m.resultsAll {
		if query == "" || strings.Contains(strings.ToLower(line), query) {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		lines = []string{
			"No diagnostics match the current filter.",
			"",
			"Press Esc to clear the filter, or / to edit it.",
		}
	}
	m.resultsLines = lines
	styled := make([]string, 0, len(lines))
	width := maxInt(10, m.viewport.Width)
	for _, line := range lines {
		for _, part := range strings.Split(ansi.Wrap(line, width, " /_"), "\n") {
			styled = append(styled, m.styleDiagnostic(part))
		}
	}
	m.viewport.SetContent(strings.Join(styled, "\n"))
}

func (m *Model) styleDiagnostic(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return line
	}
	switch {
	case strings.HasPrefix(trimmed, "status: PASS"),
		strings.HasPrefix(trimmed, "status: OK"),
		strings.Contains(trimmed, "status: PASS"):
		return m.st.Success.Render(line)
	case strings.HasPrefix(trimmed, "status: FAIL"),
		strings.Contains(trimmed, "status: FAIL"),
		strings.Contains(trimmed, "MISMATCH"):
		return m.st.Failure.Render(line)
	case strings.HasPrefix(trimmed, "status: SKIPPED"),
		strings.HasPrefix(trimmed, "status: NOT_RUN"),
		strings.HasPrefix(trimmed, "status: EDITORIAL"),
		strings.Contains(trimmed, "status: WARN"):
		return m.st.Warning.Render(line)
	case strings.HasPrefix(trimmed, "status: RUNNING"):
		return m.st.Accent.Render(line)
	case strings.HasPrefix(trimmed, "MANIFEST"),
		strings.HasPrefix(trimmed, "CANONICAL TREE"),
		strings.HasPrefix(trimmed, "CORPUS DRY-RUN"),
		strings.HasPrefix(trimmed, "BUILD"):
		return m.st.Section.Render(line)
	}
	return m.st.Body.Render(line)
}

func (m *Model) headerLine() string {
	left := m.st.Brand.Render("FEFW") + " " + m.st.Title.Render("CANONICAL CORPUS CONSOLE")
	right := m.st.Muted.Render("read-only · v" + version)
	if ansi.StringWidth(left)+ansi.StringWidth(right)+1 <= m.width {
		return left + strings.Repeat(" ", m.width-ansi.StringWidth(left)-ansi.StringWidth(right)) + right
	}
	right = m.st.Muted.Render("v" + version)
	if ansi.StringWidth(left)+ansi.StringWidth(right)+1 <= m.width {
		return left + strings.Repeat(" ", m.width-ansi.StringWidth(left)-ansi.StringWidth(right)) + right
	}
	return padANSIWidth(left, m.width)
}

func (m *Model) section(title string, width int) string {
	label := " " + strings.ToUpper(title) + " "
	if width < displayWidth(label)+4 {
		return m.st.Section.Render(truncateANSI(strings.ToUpper(title), width))
	}
	return m.st.Section.Render(label + strings.Repeat("─", width-displayWidth(label)))
}

func (m *Model) panel(title string, body []string, width int, bordered bool) []string {
	if !bordered || width < 24 {
		out := []string{m.section(title, width)}
		out = append(out, body...)
		return out
	}
	inner := width - 4
	if inner < 8 {
		out := []string{m.section(title, width)}
		out = append(out, body...)
		return out
	}
	label := " " + strings.ToUpper(title) + " "
	fill := width - 2 - displayWidth(label)
	if fill < 0 {
		fill = 0
	}
	out := make([]string, 0, len(body)+2)
	out = append(out, m.st.Panel.Render("╭")+m.st.PanelKey.Render(label)+m.st.Panel.Render(strings.Repeat("─", fill)+"╮"))
	for _, line := range body {
		out = append(out, m.st.Panel.Render("│ ")+padANSIWidth(line, inner)+m.st.Panel.Render(" │"))
	}
	out = append(out, m.st.Panel.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	return out
}

func (m *Model) badge(status string) string {
	label := strings.ToUpper(strings.TrimSpace(status))
	style := m.st.Inactive
	switch label {
	case "PASS", "OK", "STAGED", "PRESENT", "CURRENT", "MATCH":
		style = m.st.Success
	case "FAIL", "FAILED", "MISSING", "ERROR", "MISMATCH", "NOT STAGED":
		style = m.st.Failure
	case "WARN", "WARNING", "EDITORIAL", "SKIPPED", "SKIP", "NOT RUN", "NOT SCANNED", "UNAVAILABLE", "STALE":
		style = m.st.Warning
	case "RUNNING", "RUN", "ACTIVE":
		style = m.st.Accent
	}
	if label == "EDITORIAL" {
		label = "WARN"
	}
	return style.Render("[" + label + "]")
}

func (m *Model) hint(key, label string) string {
	return m.st.Key.Render(key) + " " + m.st.Footer.Render(label)
}

func (m *Model) hintLine(parts ...string) string {
	if m.notice != "" {
		parts = append([]string{m.st.Accent.Render(m.notice)}, parts...)
	}
	return truncateANSI(strings.Join(parts, " · "), m.width)
}

func (m *Model) footerLine(hints string) string {
	return m.st.Footer.Render(fitView(hints, m.width, 1))
}

// stagedSourceStatus reports whether the private staged banks needed by the
// corpus operation are present. It only stats paths and never reads them.
func stagedSourceStatus(root string) (bool, string) {
	jp, en := stagedSourcePaths(root)
	if _, err := os.Stat(jp); err != nil {
		return false, "private source is not staged"
	}
	if _, err := os.Stat(en); err != nil {
		return false, "private source is incomplete"
	}
	return true, ""
}

func (m *Model) scanReadiness() string {
	if m.lastScan == nil {
		return "NOT SCANNED"
	}
	if m.lastScan.OK() {
		return "PASS"
	}
	return "FAIL"
}

func (m *Model) sourceReadiness() string {
	if m.source {
		return "STAGED"
	}
	if m.sourceErr != "" {
		return "NOT STAGED"
	}
	return "UNAVAILABLE"
}

func (m *Model) corpusReadiness() string {
	if m.lastCorpus == nil {
		return "NOT RUN"
	}
	return m.lastCorpus.Status()
}

func (m *Model) opElapsed() time.Duration {
	if m.opStarted.IsZero() {
		return 0
	}
	return time.Since(m.opStarted)
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func (m *Model) statusForRow(index int) string {
	switch index {
	case 0:
		if m.lastScan == nil {
			return "NOT RUN"
		}
		if m.lastScan.OK() {
			return "PASS"
		}
		return "FAIL"
	case 1:
		if m.lastCorpus != nil {
			if m.lastCorpus.Status() == "EDITORIAL" {
				return "WARN"
			}
			return m.lastCorpus.Status()
		}
		if !m.source {
			return "UNAVAILABLE"
		}
		return "NOT RUN"
	case 2:
		return "READY"
	case 3:
		return "READY"
	default:
		return "REFERENCE"
	}
}

func (m *Model) syncOpsTable(width int) {
	if m.ops == nil {
		return
	}
	width = maxInt(18, width)
	actionW := 20
	statusW := 12
	if width < 60 {
		actionW = 14
		statusW = 10
	}
	descW := width - 6 - actionW - statusW
	if descW < 6 {
		descW = 6
	}
	m.ops.SetColumns([]table.Column{
		{Title: "ACTION", Width: actionW},
		{Title: "DESCRIPTION", Width: descW},
		{Title: "STATUS", Width: statusW},
	})
	m.ops.SetRows(m.opRows())
	m.ops.SetWidth(width)
	m.ops.SetHeight(len(m.opRows()) + 1)
}

func (m *Model) opRows() []table.Row {
	return []table.Row{
		{"Scan tree (r)", "Verify manifest hashes and record counts", m.statusForRow(0)},
		{"Corpus dry-run (v)", "Build banks in memory and validate output", m.statusForRow(1)},
		{"Diagnostics (d)", "Review scan findings and build details", m.statusForRow(2)},
		{"Refresh build (b)", "Re-read binary and fingerprint state", m.statusForRow(3)},
		{"Help (?)", "Keys, safety notes and display options", m.statusForRow(4)},
	}
}
