package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCorpusDryRunSkipsWithoutPrivateSource(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "translations"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := runCorpusDryRun(root, nil)
	if res == nil || !res.Skipped {
		t.Fatalf("expected skip, got %+v", res)
	}
	if res.Status() != "SKIPPED" {
		t.Fatalf("status=%q", res.Status())
	}
	if res.Elapsed() < 0 {
		t.Fatal("negative elapsed")
	}
}

func TestInspectBuildMissing(t *testing.T) {
	root := t.TempDir()
	bi := inspectBuild(root)
	if bi.BinaryPresent || bi.FPPresent {
		t.Fatalf("unexpected build artifacts: %+v", bi)
	}
	if bi.Runtime == "" || bi.SourceFiles != 0 {
		t.Fatalf("runtime/source info missing: %+v", bi)
	}
}
