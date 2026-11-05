package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fefw/internal/importer"
)

func writePackageFixture(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectEditorialDocsUsesExplicitAllowlistOnly(t *testing.T) {
	projectRoot := t.TempDir()
	root := filepath.Join(t.TempDir(), "Editorial Docs α β")
	writePackageFixture(t, root, "Ünicode (1).md", "text")
	writePackageFixture(t, root, "plain notes.txt", "text")
	writePackageFixture(t, root, "unselected-private-report.md", "must not be copied")
	writePackageFixture(t, root, "Nested Dir/hidden-from-selection.md", "must not be copied")
	items, err := collectEditorialDocs(projectRoot, root, []string{"plain notes.txt", "Ünicode (1).md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%d %+v", len(items), items)
	}
	got := map[string]bool{}
	for _, it := range items {
		if !strings.HasPrefix(it.Rel, "editorial/") {
			t.Fatalf("unexpected relative path: %s", it.Rel)
		}
		got[it.Rel] = true
	}
	if !got["editorial/Ünicode (1).md"] || !got["editorial/plain notes.txt"] {
		t.Fatalf("selected documents missing: %+v", items)
	}
	if got["editorial/unselected-private-report.md"] || got["editorial/Nested Dir/hidden-from-selection.md"] {
		t.Fatalf("unselected or nested document was copied: %+v", items)
	}
}

func TestCollectEditorialDocsEmptyAllowlistMeansNone(t *testing.T) {
	projectRoot := t.TempDir()
	root := t.TempDir()
	writePackageFixture(t, root, "unselected.md", "text")
	items, err := collectEditorialDocs(projectRoot, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("empty allowlist copied %d documents", len(items))
	}
}

func TestCollectEditorialDocsRejectsByDefault(t *testing.T) {
	projectRoot := t.TempDir()
	root := t.TempDir()
	writePackageFixture(t, root, "notes.json", "{}")
	if _, err := collectEditorialDocs(projectRoot, root, []string{"notes.json"}); err == nil {
		t.Fatal("non-document accepted")
	}
	root2 := t.TempDir()
	writePackageFixture(t, root2, ".hidden.md", "text")
	if _, err := collectEditorialDocs(projectRoot, root2, []string{".hidden.md"}); err == nil {
		t.Fatal("hidden document accepted")
	}
	root3 := t.TempDir()
	writePackageFixture(t, root3, "ok.md", "text")
	if err := os.Symlink(filepath.Join(root3, "ok.md"), filepath.Join(root3, "link.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := collectEditorialDocs(projectRoot, root3, []string{"link.md"}); err == nil {
		t.Fatal("symlink document accepted")
	}
	if _, err := collectEditorialDocs(projectRoot, root3, []string{"missing.md"}); err == nil {
		t.Fatal("missing selected document accepted")
	}
	writePackageFixture(t, root3, "Nested Dir/ok.md", "text")
	if _, err := collectEditorialDocs(projectRoot, root3, []string{"Nested Dir/ok.md"}); err == nil {
		t.Fatal("nested selected path accepted; selection must be a bare basename")
	}
}

func TestCollectEditorialDocsRejectsReservedWorkspaceRoot(t *testing.T) {
	projectRoot := t.TempDir()
	reservedRoot := filepath.Join(projectRoot, ".local", "editorial-docs")
	writePackageFixture(t, reservedRoot, "notes.md", "working data")
	if _, err := collectEditorialDocs(projectRoot, reservedRoot, []string{"notes.md"}); err == nil {
		t.Fatal("root inside the reserved workspace accepted")
	}
	if _, err := collectEditorialDocs(projectRoot, filepath.Join(projectRoot, ".local"), []string{"notes.md"}); err == nil {
		t.Fatal("reserved workspace root accepted")
	}
	nested := filepath.Join(reservedRoot, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := collectEditorialDocs(projectRoot, nested, []string{"notes.md"}); err == nil {
		t.Fatal("nested path below the reserved workspace accepted")
	}
}

func TestCollectEditorialDocsRejectsReservedWorkspaceThroughSymlink(t *testing.T) {
	projectRoot := t.TempDir()
	reservedRoot := filepath.Join(projectRoot, ".local", "editorial-docs")
	writePackageFixture(t, reservedRoot, "notes.md", "working data")
	link := filepath.Join(projectRoot, "linked-docs")
	if err := os.Symlink(filepath.Join(projectRoot, ".local"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := collectEditorialDocs(projectRoot, filepath.Join(link, "editorial-docs"), []string{"notes.md"}); err == nil {
		t.Fatal("reserved workspace reached through a symlinked parent accepted")
	}
}

func TestCollectEditorialDocsAcceptsNonReservedRoots(t *testing.T) {
	projectRoot := t.TempDir()
	for _, name := range []string{"editorial-docs", ".local-notes"} {
		root := filepath.Join(projectRoot, name)
		writePackageFixture(t, root, "notes.md", "text")
		items, err := collectEditorialDocs(projectRoot, root, []string{"notes.md"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(items) != 1 || items[0].Rel != "editorial/notes.md" {
			t.Fatalf("%s: unexpected items %+v", name, items)
		}
	}
}

func TestParseEditorialFilesRejectsUnsafeSelection(t *testing.T) {
	bad := []string{
		"a/b.md",
		"../a.md",
		".hidden.md",
		"notes.json",
		"notes.md,notes.md",
		"notes.md,",
	}
	for _, raw := range bad {
		if _, err := parseEditorialFiles(raw); err == nil {
			t.Fatalf("unsafe allowlist accepted: %q", raw)
		}
	}
	got, err := parseEditorialFiles("b.txt, a.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a.md,b.txt" {
		t.Fatalf("allowlist not normalized: %v", got)
	}
}

func TestSanitizeStagedRewritesOnlyDocumentCopies(t *testing.T) {
	base := filepath.Join(t.TempDir(), "Project Ω with spaces")
	src := filepath.Join(base, "Source β with spaces")
	staged := filepath.Join(base, "staged")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	docBody := "project=" + base + "\nsource=" + src + "\n"
	writePackageFixture(t, staged, "editorial/Nested/notes.md", docBody)
	writePackageFixture(t, staged, "private-evidence/notes.txt", docBody)
	writePackageFixture(t, staged, "translations/A.json", `{"x":"unchanged"}`)
	writePackageFixture(t, staged, "glossary/glossary.json", `{"x":"unchanged"}`)
	writePackageFixture(t, staged, "schemas/x.schema.json", `{"x":"unchanged"}`)
	writePackageFixture(t, staged, "config/project.json", `{"x":"unchanged"}`)
	cfg := importer.Config{ProjectRoot: base, SourceRoot: src}
	redactions, err := buildRedactions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := sanitizeStaged(staged, redactions); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"editorial/Nested/notes.md", "private-evidence/notes.txt"} {
		b, err := os.ReadFile(filepath.Join(staged, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		if strings.Contains(text, base) || strings.Contains(text, src) {
			t.Fatalf("%s not sanitized: %q", rel, text)
		}
		if !strings.Contains(text, "<project-root>") || !strings.Contains(text, "<source-root>") {
			t.Fatalf("%s missing neutral tokens: %q", rel, text)
		}
	}
	for _, rel := range []string{"translations/A.json", "glossary/glossary.json", "schemas/x.schema.json", "config/project.json"} {
		b, err := os.ReadFile(filepath.Join(staged, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != `{"x":"unchanged"}` {
			t.Fatalf("non-document changed: %s %q", rel, b)
		}
	}
}

func TestSanitizeStagedFailsOnLeakedPathInData(t *testing.T) {
	staged := t.TempDir()
	writePackageFixture(t, staged, "translations/A.json", `{"leak":"/home/example/private"}`)
	redactions, err := buildRedactions(importer.Config{ProjectRoot: staged})
	if err != nil {
		t.Fatal(err)
	}
	if err := sanitizeStaged(staged, redactions); err == nil {
		t.Fatal("leaked absolute path in a non-document was not rejected")
	}
}

func TestContentGateRejectsMagicInDataButAllowsInDocs(t *testing.T) {
	root := t.TempDir()
	writePackageFixture(t, root, "translations/A.json", "MsgStdBn")
	gate, err := contentGate(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gate["status"] != "FAIL" {
		t.Fatal("magic in data not rejected")
	}
	root2 := t.TempDir()
	writePackageFixture(t, root2, "editorial/notes.md", "MsgStdBn is the MSBT magic")
	gate, err = contentGate(root2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gate["status"] != "PASS" {
		t.Fatalf("magic in docs rejected: %v", gate["violations"])
	}
}

func TestRemoveOwnedDirRefusesOutsideWork(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	if err := removeOwnedDir(work, outside); err == nil {
		t.Fatal("removal outside work dir accepted")
	}
	inside := filepath.Join(work, "editorial-package")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedDir(work, inside); err != nil {
		t.Fatal(err)
	}
}
