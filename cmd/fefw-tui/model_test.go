package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func newTestModel(t *testing.T) *Model {
	t.Helper()
	root, _ := writeTestCorpus(t)
	man, sha, err := loadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(root, man, sha, "", uiOptions{NoColor: true})
	m.resize(120, 40)
	return m
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func assertViewBounds(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Fatalf("view height=%d, want %d", len(lines), height)
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w > width {
			t.Fatalf("line %d width=%d > %d: %q", i, w, width, line)
		}
	}
}

func TestDashboardRendersAtRepresentativeSizes(t *testing.T) {
	for _, size := range []struct{ w, h int }{
		{120, 40}, {100, 30}, {80, 24}, {60, 20}, {40, 12},
	} {
		m := newTestModel(t)
		m.resize(size.w, size.h)
		view := m.View()
		assertViewBounds(t, view, size.w, size.h)
		if strings.Contains(view, "\x1b[") {
			t.Fatalf("%dx%d: NO_COLOR view contains ANSI", size.w, size.h)
		}
		if size.w >= 100 {
			for _, want := range []string{"CANONICAL CORPUS CONSOLE", "WORKSPACE", "READINESS", "OPERATIONS", "RECENT RESULTS"} {
				if !strings.Contains(view, want) {
					t.Fatalf("%dx%d: dashboard missing %q", size.w, size.h, want)
				}
			}
		}
		if size.w <= 60 {
			if !strings.Contains(view, "q quit") {
				t.Fatalf("%dx%d: compact footer missing quit hint", size.w, size.h)
			}
		}
	}
}

func TestOperationDoesNotClaimSuccessBeforeDone(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenOperation
	m.opKind = opScan
	m.opRunning = true
	m.opToken = 7
	m.opStarted = time.Now().Add(-1200 * time.Millisecond)
	m.scanProg = scanProgress{Done: 2, Total: 4, Records: 10, Current: "unclassified/Test.json"}

	running := m.View()
	assertViewBounds(t, running, m.width, m.height)
	if strings.Contains(running, "[PASS]") {
		t.Fatalf("running view claims success before completion:\n%s", running)
	}
	for _, want := range []string{"RUNNING", "files 2 / 4", "50%", "current unclassified/Test.json"} {
		if !strings.Contains(running, want) {
			t.Fatalf("running view missing %q:\n%s", want, running)
		}
	}

	cmd := m.handleOpEvent(opEvent{
		Token: 7,
		Kind:  opEventScanDone,
		Scan: &scanResult{
			Files: 4, Records: 10, ManifestSHA: "a", Recomputed: "a",
		},
	})
	if cmd != nil {
		t.Fatal("done event should not schedule another wait")
	}
	if m.opRunning {
		t.Fatal("operation still running after done event")
	}
	final := m.View()
	assertViewBounds(t, final, m.width, m.height)
	for _, want := range []string{"[PASS]", "outcome", "success 4 files"} {
		if !strings.Contains(final, want) {
			t.Fatalf("final view missing %q:\n%s", want, final)
		}
	}
	if m.lastScan == nil {
		t.Fatal("last scan result was not recorded")
	}
}

func TestProgressWithoutTotalStaysIndeterminate(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenOperation
	m.opKind = opScan
	m.opRunning = true
	m.opToken = 2
	m.opStarted = time.Now()
	m.scanProg = scanProgress{}
	view := m.View()
	if strings.Contains(view, "%") {
		t.Fatalf("unknown total invented a percentage:\n%s", view)
	}
	if !strings.Contains(view, "Preparing scan") {
		t.Fatalf("indeterminate state missing:\n%s", view)
	}
}

func TestCancelOperationRestoresDashboardAndInvalidatesEvents(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenOperation
	m.opKind = opScan
	m.opRunning = true
	m.opToken = 3
	canceled := false
	m.cancel = func() { canceled = true }

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !canceled {
		t.Fatal("cancel function was not called")
	}
	if m.opRunning || m.screen != screenDashboard {
		t.Fatalf("cancel did not return to dashboard: running=%v screen=%v", m.opRunning, m.screen)
	}
	if m.opToken == 3 {
		t.Fatal("token was not invalidated")
	}
	if !strings.Contains(m.View(), "operation canceled") {
		t.Fatal("cancel notice missing")
	}
	m.handleOpEvent(opEvent{Token: 3, Kind: opEventScanProgress, Progress: scanProgress{Done: 1, Total: 2}})
	if m.scanProg.Done != 0 {
		t.Fatal("stale event changed the model after cancellation")
	}
}

func TestCorpusSkippedView(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenOperation
	m.opKind = opCorpus
	m.corpusRes = &corpusResult{
		Skipped: true,
		Reason:  "private source is not staged",
		Started: time.Now().Add(-time.Second), Finished: time.Now(),
	}
	view := m.View()
	assertViewBounds(t, view, m.width, m.height)
	for _, want := range []string{"[SKIPPED]", "skipped", "private source is not staged"} {
		if !strings.Contains(view, want) {
			t.Fatalf("skipped view missing %q:\n%s", want, view)
		}
	}
}

func TestResultsFilterAndNavigation(t *testing.T) {
	m := newTestModel(t)
	m.openResults()
	if m.screen != screenResults {
		t.Fatalf("screen=%v", m.screen)
	}
	all := len(m.resultsAll)
	if all == 0 {
		t.Fatal("diagnostics are empty")
	}

	m.Update(keyRunes("/"))
	if !m.filterActive {
		t.Fatal("filter did not activate")
	}
	m.Update(keyRunes("manifest"))
	if m.resultsQuery != "manifest" {
		t.Fatalf("query=%q", m.resultsQuery)
	}
	if len(m.resultsLines) == 0 || len(m.resultsLines) >= all {
		t.Fatalf("filter did not narrow results: %d/%d", len(m.resultsLines), all)
	}
	for _, line := range m.resultsLines {
		if !strings.Contains(strings.ToLower(line), "manifest") {
			t.Fatalf("filtered line does not match: %q", line)
		}
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.filterActive {
		t.Fatal("enter did not close the filter")
	}
	assertViewBounds(t, m.View(), m.width, m.height)

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.resultsQuery != "" {
		t.Fatal("esc did not clear the filter")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenDashboard {
		t.Fatalf("second esc screen=%v", m.screen)
	}
}

func TestHelpOpensAndCloses(t *testing.T) {
	m := newTestModel(t)
	m.Update(keyRunes("?"))
	if m.screen != screenHelp {
		t.Fatalf("screen=%v", m.screen)
	}
	view := m.View()
	assertViewBounds(t, view, m.width, m.height)
	for _, want := range []string{"HELP", "KEYS", "SAFETY", "DISPLAY"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help missing %q", want)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenDashboard {
		t.Fatalf("help did not close: %v", m.screen)
	}
}

func TestColorViewBounds(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	root, _ := writeTestCorpus(t)
	man, sha, err := loadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(root, man, sha, "", uiOptions{Color: true})
	m.resize(100, 30)
	view := m.View()
	assertViewBounds(t, view, 100, 30)
	if !strings.Contains(view, "\x1b[") {
		t.Fatal("colour view did not emit ANSI styling")
	}
}

func TestScanContextCancellation(t *testing.T) {
	root, _ := writeTestCorpus(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := scanCanonicalContext(ctx, root, func(scanProgress) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
}

func TestFinishedScanViewFitsCompactTerminal(t *testing.T) {
	m := newTestModel(t)
	m.resize(60, 20)
	m.screen = screenOperation
	m.opKind = opScan
	m.scanRes = &scanResult{
		Files: 1322, Records: 81103, CheckedBytes: 170 * 1024 * 1024,
		ManifestSHA: "a", Recomputed: "a",
	}
	view := m.View()
	assertViewBounds(t, view, 60, 20)
	for _, want := range []string{"[PASS]", "outcome", "success 1,322 files"} {
		if !strings.Contains(view, want) {
			t.Fatalf("compact final scan missing %q:\n%s", want, view)
		}
	}
}

func TestCorpusEditorialBadgeUsesWarnLabel(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenOperation
	m.opKind = opCorpus
	m.corpusRes = &corpusResult{
		Banks: 523, BuiltFiles: 523, BuiltRecords: 81103,
		DiskFiles: 1322, DiskRecords: 81103, DiskRealEN: 3011,
		ValidatorStatus: "FAIL", Checks: map[string]bool{"a": true, "b": true},
		GuardErrors: 3011, Started: time.Now().Add(-time.Second), Finished: time.Now(),
	}
	view := m.View()
	assertViewBounds(t, view, m.width, m.height)
	if !strings.Contains(view, "[WARN]") {
		t.Fatalf("editorial result did not display WARN:\n%s", view)
	}
	if !strings.Contains(view, "Editorial guard") {
		t.Fatalf("editorial guard explanation missing:\n%s", view)
	}
}

func TestThemeDetection(t *testing.T) {
	t.Setenv("FEFW_THEME", "light")
	if !detectLightTheme() {
		t.Fatal("FEFW_THEME=light not detected")
	}
	t.Setenv("FEFW_THEME", "dark")
	if detectLightTheme() {
		t.Fatal("FEFW_THEME=dark not detected")
	}
	t.Setenv("FEFW_THEME", "")
	t.Setenv("COLORFGBG", "0;15")
	if !detectLightTheme() {
		t.Fatal("light COLORFGBG not detected")
	}
	t.Setenv("COLORFGBG", "0;0")
	if detectLightTheme() {
		t.Fatal("dark COLORFGBG not detected")
	}
}

func TestResultsViewBoundsAtNarrowSizes(t *testing.T) {
	m := newTestModel(t)
	for _, size := range []struct{ w, h int }{{120, 40}, {80, 24}, {60, 20}, {40, 12}} {
		m.resize(size.w, size.h)
		m.openResults()
		assertViewBounds(t, m.View(), size.w, size.h)
		m.screen = screenDashboard
	}
}

func TestCorpusFinalViewFitsCompactTerminal(t *testing.T) {
	m := newTestModel(t)
	m.resize(60, 20)
	m.screen = screenOperation
	m.opKind = opCorpus
	m.corpusRes = &corpusResult{
		Banks: 523, BuiltFiles: 523, BuiltRecords: 81103,
		DiskFiles: 1322, DiskRecords: 81103, DiskRealEN: 3011,
		ValidatorStatus: "FAIL", Checks: map[string]bool{"a": true, "b": true},
		GuardErrors: 3011, Started: time.Now().Add(-time.Second), Finished: time.Now(),
	}
	assertViewBounds(t, m.View(), 60, 20)
}

func TestLightPaletteSelection(t *testing.T) {
	root, _ := writeTestCorpus(t)
	man, sha, err := loadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(root, man, sha, "", uiOptions{Color: true, Light: true})
	if m.palette != lightPalette() {
		t.Fatalf("light palette not selected: %+v", m.palette)
	}
	d := newModel(root, man, sha, "", uiOptions{Color: true})
	if d.palette != darkPalette() {
		t.Fatalf("dark palette not selected: %+v", d.palette)
	}
}
