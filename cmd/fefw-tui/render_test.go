package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"fefw/internal/importer"
)

func TestComma(t *testing.T) {
	cases := map[int]string{0: "0", 12: "12", 123: "123", 1234: "1,234", 1234567: "1,234,567", -1234: "-1,234"}
	for in, want := range cases {
		if got := comma(in); got != want {
			t.Fatalf("comma(%d)=%q want %q", in, got, want)
		}
	}
}

func TestTruncateUnicodeWidth(t *testing.T) {
	for _, s := range []string{"plain", "日本語のファイル名.json", "a日本語b", "e\u0301clair"} {
		for _, n := range []int{1, 2, 4, 8, 12} {
			got := truncate(s, n)
			if displayWidth(got) > n {
				t.Fatalf("truncate(%q,%d)=%q width=%d", s, n, got, displayWidth(got))
			}
		}
	}
	got := truncateMiddle("characters/profiles/Person.json", 16)
	if displayWidth(got) > 16 {
		t.Fatalf("middle truncation too wide: %q (%d)", got, displayWidth(got))
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("middle truncation missing ellipsis: %q", got)
	}
}

func TestAsciiProgressUsesRealCounts(t *testing.T) {
	bar := asciiProgress(50, 100, 10)
	if !strings.Contains(bar, "50%") {
		t.Fatalf("progress percent missing: %q", bar)
	}
	if strings.Count(bar, "█") != 5 {
		t.Fatalf("progress fill wrong: %q", bar)
	}
	if strings.Contains(asciiProgress(0, 0, 10), "%") {
		t.Fatalf("indeterminate bar must not invent a percentage")
	}
}

func TestFitViewBoundsAndANSIWidth(t *testing.T) {
	styled := "\x1b[32m" + strings.Repeat("日", 40) + "\x1b[0m"
	for _, width := range []int{10, 20, 60} {
		got := fitView(styled, width, 3)
		lines := strings.Split(got, "\n")
		if len(lines) != 3 {
			t.Fatalf("fitView height=%d", len(lines))
		}
		for _, line := range lines {
			if w := ansi.StringWidth(line); w > width {
				t.Fatalf("fitView line width=%d > %d: %q", w, width, line)
			}
		}
	}
}

func TestPlainReportHasNoANSIAndRealStatus(t *testing.T) {
	man := &importer.Manifest{
		SchemaVersion: 1,
		Counts: importer.ManifestCounts{
			Files: 2, Records: 10, JP: 10, OfficialEN: 10, RealEN: 3,
			ByType:         map[string]int{"story": 10},
			ByReviewStatus: map[string]int{"UNREVIEWED": 7, "CHANGE": 3},
		},
	}
	scan := &scanResult{
		Files: 2, Records: 10, ManifestSHA: "a", Recomputed: "a",
		ManifestFiles: 2, ManifestRecords: 10,
	}
	report := plainReport("/project", man, "a", scan, buildInfo{Runtime: "go-test"})
	if strings.Contains(report, "\x1b[") {
		t.Fatal("plain report contains ANSI")
	}
	for _, want := range []string{"MANIFEST", "CANONICAL TREE", "CORPUS DRY-RUN", "BUILD", "status: PASS", "records: 10"} {
		if !strings.Contains(report, want) {
			t.Fatalf("plain report missing %q", want)
		}
	}
}

func TestPlainReportIncludesCorpusResult(t *testing.T) {
	man := &importer.Manifest{SchemaVersion: 1, Counts: importer.ManifestCounts{
		Files: 1, Records: 2, JP: 2, OfficialEN: 2,
		ByType: map[string]int{"story": 2}, ByReviewStatus: map[string]int{"UNREVIEWED": 2},
	}}
	corpus := &corpusResult{Skipped: true, Reason: "private source is not staged"}
	report := plainReportWithCorpus("/project", man, "sha", &scanResult{ManifestSHA: "sha", Recomputed: "sha", Files: 1, Records: 2}, corpus, buildInfo{Runtime: "go-test"})
	for _, want := range []string{"CORPUS DRY-RUN", "status: SKIPPED", "private source is not staged"} {
		if !strings.Contains(report, want) {
			t.Fatalf("corpus report missing %q", want)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	if humanBytes(512) != "512 B" || humanBytes(1024) != "1.0 KiB" {
		t.Fatalf("humanBytes: %q %q", humanBytes(512), humanBytes(1024))
	}
}
