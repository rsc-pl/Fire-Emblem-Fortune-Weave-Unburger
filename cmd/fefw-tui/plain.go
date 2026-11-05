package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"fefw/internal/importer"
)

func runPlain(root string) int {
	man, manSHA, err := loadManifest(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintf(os.Stderr, "hint: run from the project root or pass --root\n")
		return 1
	}
	scan, err := scanCanonical(root, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: scan: %v\n", err)
		return 1
	}
	build := inspectBuild(root)
	fmt.Print(plainReportWithCorpus(root, man, manSHA, scan, nil, build))
	if !scan.OK() {
		return 1
	}
	return 0
}

// plainReport is kept as the five-argument convenience form used by callers
// that do not have a corpus result.
func plainReport(root string, man *importer.Manifest, manSHA string, scan *scanResult, build buildInfo) string {
	return plainReportWithCorpus(root, man, manSHA, scan, nil, build)
}

func plainReportWithCorpus(root string, man *importer.Manifest, manSHA string, scan *scanResult, corpus *corpusResult, build buildInfo) string {
	var b strings.Builder
	b.WriteString("FEFW canonical corpus console " + version + "\n")
	b.WriteString("read-only inspection\n")
	b.WriteString("root: " + root + "\n")
	b.WriteString("runtime: " + build.Runtime + "\n")
	b.WriteString("\n")
	b.WriteString("MANIFEST\n")
	if man == nil {
		b.WriteString("  status: FAIL (manifest could not be loaded)\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  files: %s   records: %s\n", comma(man.Counts.Files), comma(man.Counts.Records))
	fmt.Fprintf(&b, "  jp: %s   official_en: %s   real_en: %s\n",
		comma(man.Counts.JP), comma(man.Counts.OfficialEN), comma(man.Counts.RealEN))
	fmt.Fprintf(&b, "  manifest sha256: %s\n", manSHA)
	b.WriteString("  by type:\n")
	for _, k := range sortedKeys(man.Counts.ByType) {
		fmt.Fprintf(&b, "    %-14s %8s records\n", k, comma(man.Counts.ByType[k]))
	}
	b.WriteString("  by review:\n")
	for _, k := range sortedKeys(man.Counts.ByReviewStatus) {
		fmt.Fprintf(&b, "    %-14s %8s records\n", k, comma(man.Counts.ByReviewStatus[k]))
	}
	b.WriteString("\nCANONICAL TREE\n")
	if scan == nil {
		b.WriteString("  status: FAIL (scan did not complete)\n")
	} else {
		status := "PASS"
		if !scan.OK() {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "  status: %s\n", status)
		fmt.Fprintf(&b, "  files: %s   records: %s   bytes: %s\n",
			comma(scan.Files), comma(scan.Records), comma(int(scan.CheckedBytes/1024))+" KiB")
		fmt.Fprintf(&b, "  hashes: %d mismatch   missing: %d   extra: %d   schema: %d   duplicate ids: %d   symlinks: %d\n",
			scan.HashMismatchCount, scan.MissingCount, scan.ExtraCount, scan.SchemaErrorCount, scan.DuplicateIDCount, scan.SymlinkCount)
		fmt.Fprintf(&b, "  manifest digest: %s\n", map[bool]string{true: "match", false: "MISMATCH"}[scan.ManifestSHA == scan.Recomputed])
		fmt.Fprintf(&b, "  elapsed: %s\n", scan.Elapsed().Round(time.Millisecond))
	}
	b.WriteString("\nCORPUS DRY-RUN\n")
	switch {
	case corpus == nil:
		b.WriteString("  status: NOT_RUN (interactive key 'v'; private sources are not read in plain mode)\n")
	case corpus.Skipped:
		b.WriteString("  status: SKIPPED\n")
		fmt.Fprintf(&b, "  reason: %s\n", corpus.Reason)
	case corpus.Error != "":
		b.WriteString("  status: FAIL\n")
		fmt.Fprintf(&b, "  error: %s\n", corpus.Error)
	default:
		fmt.Fprintf(&b, "  status: %s\n", corpus.Status())
		fmt.Fprintf(&b, "  banks: %s   in-memory: %s files / %s records / %s real_en\n",
			comma(corpus.Banks), comma(corpus.BuiltFiles), comma(corpus.BuiltRecords), comma(corpus.BuiltRealEN))
		fmt.Fprintf(&b, "  on disk: %s files / %s records / %s real_en\n",
			comma(corpus.DiskFiles), comma(corpus.DiskRecords), comma(corpus.DiskRealEN))
		fmt.Fprintf(&b, "  validator: %s (%s)\n", corpus.ValidatorStatus, checksSummary(corpus.Checks))
		fmt.Fprintf(&b, "  editorial guard: %d   other errors: %d\n", corpus.GuardErrors, corpus.OtherErrors)
		fmt.Fprintf(&b, "  elapsed: %s\n", corpus.Elapsed().Round(time.Millisecond))
	}
	b.WriteString("\nBUILD\n")
	if build.BinaryPresent {
		fmt.Fprintf(&b, "  binary: %s (%s, %s)\n", build.BinaryPath, comma(int(build.BinarySize)), build.BinaryMod.Format("2006-01-02 15:04:05"))
	} else {
		b.WriteString("  binary: missing\n")
	}
	if build.FPPresent {
		fmt.Fprintf(&b, "  launcher fingerprint: %s\n", build.Fingerprint)
	} else {
		b.WriteString("  launcher fingerprint: not recorded\n")
	}
	if build.SourceErr != "" {
		fmt.Fprintf(&b, "  source digest: unavailable (%s)\n", build.SourceErr)
	} else {
		fmt.Fprintf(&b, "  source digest: %s (%d source files)\n", build.SourceDigest, build.SourceFiles)
	}
	return b.String()
}
