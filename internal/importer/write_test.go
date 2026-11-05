package importer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteImportDryRunDoesNotCreateTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "translations")
	cfg := Config{TranslationsDir: root}
	res := &ImportResult{Shards: map[string][]byte{"unclassified/A.json": []byte(`{"schema_version":1,"records":[]}`)}}
	written, err := WriteImport(cfg, res, WriteOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 {
		t.Fatalf("dry-run did not report would-write path: %v", written)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("dry-run mutated the tree: %v", err)
	}
}

func TestWriteImportCreatesTreeWhenApplied(t *testing.T) {
	root := filepath.Join(t.TempDir(), "translations")
	cfg := Config{TranslationsDir: root}
	body := []byte(`{"schema_version":1,"records":[]}`)
	res := &ImportResult{Shards: map[string][]byte{"unclassified/A.json": body}, ManifestBytes: []byte(`{"schema_version":1,"counts":{},"files":[]}`)}
	if _, err := WriteImport(cfg, res, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "unclassified", "A.json")); err != nil || string(b) != string(body) {
		t.Fatalf("shard not written: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
}
