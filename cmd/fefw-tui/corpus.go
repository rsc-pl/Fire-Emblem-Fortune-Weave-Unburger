package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fefw/internal/importer"
)

type corpusResult struct {
	Skipped bool
	Reason  string
	Phase   string
	Error   string

	Banks        int
	BuiltFiles   int
	BuiltRecords int
	BuiltRealEN  int
	DiskFiles    int
	DiskRecords  int
	DiskRealEN   int

	ValidatorStatus string
	Checks          map[string]bool
	GuardErrors     int
	OtherErrors     int

	Started  time.Time
	Finished time.Time
}

func (r *corpusResult) Elapsed() time.Duration {
	if r == nil || r.Finished.IsZero() {
		return 0
	}
	return r.Finished.Sub(r.Started)
}

func (r *corpusResult) Status() string {
	switch {
	case r == nil:
		return "NOT_RUN"
	case r.Skipped:
		return "SKIPPED"
	case r.Error != "":
		return "FAIL"
	case r.OtherErrors > 0:
		return "FAIL"
	case r.GuardErrors > 0:
		return "EDITORIAL"
	case r.ValidatorStatus == "PASS":
		return "PASS"
	default:
		return "FAIL"
	}
}

// runCorpusDryRun loads the private staged corpus, builds the importer's
// canonical bank layout in memory and validates the live translation tree
// against the source text. It never writes.
func runCorpusDryRun(root string, progress func(string)) *corpusResult {
	return runCorpusDryRunContext(context.Background(), root, progress)
}

// runCorpusDryRunContext is the cancellable form. Cancellation is checked
// between phases because the importer's decode/build/validate calls are
// synchronous and read-only.
func runCorpusDryRunContext(ctx context.Context, root string, progress func(string)) *corpusResult {
	res := &corpusResult{Started: time.Now(), Phase: "checking private source", Checks: map[string]bool{}}
	jp, en := stagedSourcePaths(root)
	if _, err := os.Stat(jp); err != nil {
		res.Skipped = true
		res.Reason = "private source is not staged (expected .local/_incoming/fefw-original-sources)"
		res.Finished = time.Now()
		return res
	}
	if _, err := os.Stat(en); err != nil {
		res.Skipped = true
		res.Reason = "private source is incomplete (USen message bank is missing)"
		res.Finished = time.Now()
		return res
	}
	if corpusCanceled(ctx, res) {
		return res
	}

	cfg := defaultConfig(root)
	if progress != nil {
		progress("loading and decoding message banks")
	}
	corp, err := importer.LoadCorpus(cfg)
	if err != nil {
		res.Error = "load corpus: " + err.Error()
		res.Finished = time.Now()
		return res
	}
	res.Banks = len(corp.Banks)
	if corpusCanceled(ctx, res) {
		return res
	}

	if progress != nil {
		progress(fmt.Sprintf("building %d bank pairs in memory", len(corp.Banks)))
	}
	built, err := importer.Build(corp)
	if err != nil {
		res.Error = "build: " + err.Error()
		res.Finished = time.Now()
		return res
	}
	res.BuiltFiles = built.Manifest.Counts.Files
	res.BuiltRecords = built.Manifest.Counts.Records
	res.BuiltRealEN = built.Manifest.Counts.RealEN
	if corpusCanceled(ctx, res) {
		return res
	}

	diskMan, _, err := loadManifest(root)
	if err != nil {
		res.Error = "load manifest: " + err.Error()
		res.Finished = time.Now()
		return res
	}
	res.DiskFiles = diskMan.Counts.Files
	res.DiskRecords = diskMan.Counts.Records
	res.DiskRealEN = diskMan.Counts.RealEN

	if progress != nil {
		progress("validating shard text fidelity and record ids")
	}
	rep, err := importer.ValidateOutput(cfg, corp)
	if err != nil {
		res.Error = "validate: " + err.Error()
		res.Finished = time.Now()
		return res
	}
	res.ValidatorStatus = rep.Status
	res.Checks = rep.Checks
	for _, e := range rep.Errors {
		// Routine validation accepts an edited tree. The fresh-import guard is
		// only requested by import/migration callers, so any remaining such
		// error is still surfaced instead of being treated as a clean pass.
		if strings.Contains(e, "fresh import contains reviewed/real_en record") ||
			strings.Contains(e, "initial import contains reviewed/real_en record") {
			res.GuardErrors++
		} else {
			res.OtherErrors++
		}
	}
	res.Finished = time.Now()
	return res
}

func corpusCanceled(ctx context.Context, res *corpusResult) bool {
	if ctx.Err() == nil {
		return false
	}
	res.Error = "canceled"
	res.Finished = time.Now()
	return true
}

func stagedSourcePaths(root string) (jp, en string) {
	src := filepath.Join(root, ".local", "_incoming", "fefw-original-sources")
	return filepath.Join(src, "romfs", "Msg", "JPja"),
		filepath.Join(src, "romfs", "Msg", "USen")
}
