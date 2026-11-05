package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"fefw/internal/importer"
)

const sampleRecord = `{
  "record_id": "r1",
  "source": {"platform": "switch2", "bank": "Test"},
  "context": {},
  "function": "unclassified",
  "text": {"jp": "source", "official_en": "official", "real_en": null},
  "review": {"status": "UNREVIEWED"}
}`

func writeTestCorpus(t *testing.T, records ...string) (root string, shard string) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, "translations")
	if err := os.MkdirAll(filepath.Join(dir, "unclassified"), 0o755); err != nil {
		t.Fatal(err)
	}
	shard = filepath.Join(dir, "unclassified", "Test.json")
	if len(records) == 0 {
		records = []string{sampleRecord}
	}
	var raw []json.RawMessage
	for _, r := range records {
		raw = append(raw, json.RawMessage(r))
	}
	body, err := json.MarshalIndent(map[string]any{"schema_version": 1, "records": raw}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(shard, body, 0o644); err != nil {
		t.Fatal(err)
	}
	man := &importer.Manifest{
		SchemaVersion: 1,
		Counts: importer.ManifestCounts{
			Files: 1, Records: len(raw), JP: len(raw), OfficialEN: len(raw),
			ByType:            map[string]int{"unclassified": len(raw)},
			ByReviewStatus:    map[string]int{"UNREVIEWED": len(raw)},
			MissingJP:         0,
			MissingOfficialEN: 0,
		},
		Files: []importer.ManifestFile{{
			Path: "unclassified/Test.json", SHA256: shaBytes(body), Records: len(raw), Type: "unclassified",
		}},
	}
	mb, err := marshalManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	return root, shard
}

func TestScanCanonicalClean(t *testing.T) {
	root, _ := writeTestCorpus(t)
	res, err := scanCanonical(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("expected clean scan: %+v", res)
	}
	if res.Files != 1 || res.Records != 1 || res.ManifestSHA != res.Recomputed {
		t.Fatalf("unexpected scan result: files=%d records=%d sha=%s/%s", res.Files, res.Records, res.ManifestSHA, res.Recomputed)
	}
}

func TestScanCanonicalDetectsHashMismatch(t *testing.T) {
	root, shard := writeTestCorpus(t)
	if err := os.WriteFile(shard, []byte(`{"schema_version":1,"records":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := scanCanonical(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() || res.HashMismatchCount != 1 {
		t.Fatalf("hash mismatch not detected: %+v", res)
	}
}

func TestScanCanonicalDetectsMissingAndExtra(t *testing.T) {
	root, shard := writeTestCorpus(t)
	if err := os.Remove(shard); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "translations", "extra.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := scanCanonical(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.MissingCount != 1 || res.ExtraCount != 1 || res.OK() {
		t.Fatalf("missing/extra not detected: %+v", res)
	}
}

func TestScanCanonicalDetectsDuplicateIDs(t *testing.T) {
	root, _ := writeTestCorpus(t, sampleRecord)
	// Replace the manifest with two files that both contain r1.
	dir := filepath.Join(root, "translations")
	body := []byte(`{"schema_version":1,"records":[` + sampleRecord + `]}` + "\n")
	second := filepath.Join(dir, "unclassified", "Second.json")
	if err := os.WriteFile(second, body, 0o644); err != nil {
		t.Fatal(err)
	}
	man := &importer.Manifest{
		SchemaVersion: 1,
		Counts: importer.ManifestCounts{
			Files: 2, Records: 2, JP: 2, OfficialEN: 2,
			ByType:         map[string]int{"unclassified": 2},
			ByReviewStatus: map[string]int{"UNREVIEWED": 2},
		},
		Files: []importer.ManifestFile{
			{Path: "unclassified/Test.json", SHA256: shaBytes(body), Records: 1, Type: "unclassified"},
			{Path: "unclassified/Second.json", SHA256: shaBytes(body), Records: 1, Type: "unclassified"},
		},
	}
	mb, err := marshalManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := scanCanonical(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.DuplicateIDCount != 1 {
		t.Fatalf("duplicate id not detected: %+v", res)
	}
}

func TestScanCanonicalRejectsSymlinkedShard(t *testing.T) {
	root, shard := writeTestCorpus(t)
	target := filepath.Join(root, "outside.json")
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(shard); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, shard); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	res, err := scanCanonical(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.SymlinkCount != 1 || res.OK() {
		t.Fatalf("symlink was not rejected: %+v", res)
	}
}

func TestSafeManifestPath(t *testing.T) {
	for _, p := range []string{"a.json", "dir/a.json", "a/b/c.json"} {
		if !safeManifestPath(p) {
			t.Fatalf("%q should be safe", p)
		}
	}
	for _, p := range []string{"", "/a.json", "../a.json", "a/../b.json", `a\b.json`, "."} {
		if safeManifestPath(p) {
			t.Fatalf("%q should be rejected", p)
		}
	}
}
