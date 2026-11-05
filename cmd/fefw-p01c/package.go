package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"fefw/internal/importer"
)

// p01cPackageManifest is the content manifest of the cumulative package.
type p01cPackageManifest struct {
	SchemaVersion int               `json:"schema_version"`
	Release       string            `json:"release"`
	CreatedFrom   string            `json:"created_from"`
	Files         []p01cPackageFile `json:"files"`
	Totals        map[string]int    `json:"totals"`
	ContentGate   map[string]any    `json:"content_gate"`
	Notes         []string          `json:"notes"`
}

type p01cPackageFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	Group  string `json:"group"`
}

type p01cPackItem struct {
	Src   string
	Rel   string
	Group string
}

// redaction replaces a machine-specific root with a neutral token. Redactions
// are derived at runtime; no user path is compiled into this program.
type redaction struct {
	from string
	to   string
}

func runPackage(cfg importer.Config, dryRun bool, editorialRoot, editorialFiles string) {
	l := mustLoad(cfg)
	// Validate before packaging. Routine validation accepts an edited tree.
	rep, err := importer.ValidateOutput(cfg, l.corp)
	if err != nil {
		fatal(err)
	}
	if rep.Status != "PASS" {
		fatal(fmt.Errorf("refusing to package an invalid canonical tree: %v", rep.Errors))
	}
	docs, err := parseEditorialFiles(editorialFiles)
	if err != nil {
		fatal(err)
	}
	if editorialRoot == "" && len(docs) > 0 {
		fatal(fmt.Errorf("--editorial-files requires --editorial-docs"))
	}
	if editorialRoot != "" && len(docs) == 0 {
		fatal(fmt.Errorf("--editorial-docs requires an explicit --editorial-files allowlist; no directory is scanned implicitly"))
	}
	items, err := collectPackageItems(cfg, editorialRoot, docs, !dryRun)
	if err != nil {
		fatal(err)
	}
	if dryRun {
		var bytes int64
		groups := map[string]int{}
		for _, it := range items {
			if info, statErr := os.Lstat(it.Src); statErr == nil {
				bytes += info.Size()
			}
			groups[it.Group]++
		}
		fmt.Printf("package dry-run: files=%d bytes=%d groups=%v archive=%s\n", len(items), bytes, groups, filepath.Join(cfg.WorkDir, "editorial-package.tar.zst"))
		for _, it := range items {
			fmt.Printf("  would include %s\n", it.Rel)
		}
		fmt.Println("dry-run: no files written")
		return
	}
	outDir := filepath.Join(cfg.WorkDir, "editorial-package")
	if err := removeOwnedDir(cfg.WorkDir, outDir); err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatal(err)
	}
	// Copy items into a staging tree, preserving relative paths.
	for _, it := range items {
		dst := filepath.Join(outDir, filepath.FromSlash(it.Rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fatal(err)
		}
		if err := copyFile(it.Src, dst); err != nil {
			fatal(err)
		}
	}
	// Sanitize only staged documentation copies. Translations, glossary,
	// variants, manifests and schemas are never rewritten here.
	redactions, err := buildRedactions(cfg)
	if err != nil {
		fatal(err)
	}
	if err := sanitizeStaged(outDir, redactions); err != nil {
		fatal(err)
	}
	man := &p01cPackageManifest{
		SchemaVersion: 1,
		Release:       "editorial-package",
		CreatedFrom:   "translations + schemas + glossary + optional editorial docs + compact private evidence",
		Totals:        map[string]int{},
		Notes: []string{
			"No source ROM, MSBT/MSBP, ExeFS, audio, keys or absolute user paths are included.",
			"translations/**/*.json is complete (all canonical shards and manifest); no language value is changed by this tool.",
			"Private evidence is limited to compact JSON/TSV/MD; no raw offsets or source bytes.",
		},
	}
	for _, it := range items {
		dst := filepath.Join(outDir, filepath.FromSlash(it.Rel))
		b, err := os.ReadFile(dst)
		if err != nil {
			fatal(err)
		}
		man.Files = append(man.Files, p01cPackageFile{Path: it.Rel, Bytes: int64(len(b)), SHA256: shaBytes(b), Group: it.Group})
		man.Totals[it.Group]++
		man.Totals["files"]++
		man.Totals["bytes"] += len(b)
	}
	sort.Slice(man.Files, func(i, j int) bool { return man.Files[i].Path < man.Files[j].Path })
	// Content gate over the staged payload before the manifest is added.
	gate, err := contentGate(outDir, redactionSources(redactions))
	if err != nil {
		fatal(err)
	}
	man.ContentGate = gate
	manBytes := mustMarshalIndent(man)
	if err := os.WriteFile(filepath.Join(outDir, "PACKAGE_MANIFEST.json"), manBytes, 0o644); err != nil {
		fatal(err)
	}
	// Re-run the gate after writing the manifest so the final tree is checked.
	gate, err = contentGate(outDir, redactionSources(redactions))
	if err != nil {
		fatal(err)
	}
	if st, _ := gate["status"].(string); st != "PASS" {
		writeJSON(filepath.Join(cfg.ReportsDir, "editorial_package_content_gate.json"), gate)
		fatal(fmt.Errorf("content gate failed: %v", gate["violations"]))
	}
	man.ContentGate = gate
	manBytes = mustMarshalIndent(man)
	if err := os.WriteFile(filepath.Join(outDir, "PACKAGE_MANIFEST.json"), manBytes, 0o644); err != nil {
		fatal(err)
	}
	// Final gate over the exact tree that will be archived.
	finalGate, err := contentGate(outDir, redactionSources(redactions))
	if err != nil {
		fatal(err)
	}
	if st, _ := finalGate["status"].(string); st != "PASS" {
		writeJSON(filepath.Join(cfg.ReportsDir, "editorial_package_content_gate.json"), finalGate)
		fatal(fmt.Errorf("final content gate failed: %v", finalGate["violations"]))
	}
	gate = finalGate
	archive := filepath.Join(cfg.WorkDir, "editorial-package.tar.zst")
	if err := removeOwnedFile(cfg.WorkDir, archive); err != nil {
		fatal(err)
	}
	if err := tarZst(outDir, archive); err != nil {
		fatal(err)
	}
	// External SHA/size record.
	ab, err := os.ReadFile(archive)
	if err != nil {
		fatal(err)
	}
	sum := sha256.Sum256(ab)
	sumHex := hex.EncodeToString(sum[:])
	rec := map[string]any{
		"schema_version": 1,
		"archive":        "editorial-package.tar.zst",
		"bytes":          len(ab),
		"sha256":         sumHex,
		"files":          man.Totals["files"],
		"content_gate":   gate["status"],
		"manifest":       "PACKAGE_MANIFEST.json",
	}
	writeJSON(filepath.Join(cfg.ReportsDir, "editorial_package.json"), rec)
	writeJSON(filepath.Join(cfg.EvidenceDir, "editorial_package.json"), rec)
	writeText(filepath.Join(cfg.ReportsDir, "editorial_package_sha256.txt"), sumHex+"  editorial-package.tar.zst\n")
	fmt.Printf("package: %s bytes=%d sha256=%s files=%d gate=%v\n", archive, len(ab), sumHex, man.Totals["files"], gate["status"])
}

func collectPackageItems(cfg importer.Config, editorialRoot string, editorialFiles []string, materializeEvidence bool) ([]p01cPackItem, error) {
	var items []p01cPackItem
	seen := map[string]bool{}
	add := func(src, rel, group string) error {
		if err := safeRel(rel); err != nil {
			return err
		}
		if seen[rel] {
			return fmt.Errorf("duplicate package path: %s", rel)
		}
		info, err := os.Lstat(src)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink package input: %s", rel)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("package input is not a regular file: %s", rel)
		}
		seen[rel] = true
		items = append(items, p01cPackItem{Src: src, Rel: rel, Group: group})
		return nil
	}
	// translations: every JSON including manifest and variants.
	err := filepath.WalkDir(cfg.TranslationsDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in translations: %s", p)
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		rel, err := filepath.Rel(cfg.TranslationsDir, p)
		if err != nil {
			return err
		}
		return add(p, "translations/"+filepath.ToSlash(rel), "translations")
	})
	if err != nil {
		return nil, err
	}
	// schemas.
	for _, n := range []string{"canonical-record.schema.json", "canonical-shard.schema.json", "qa-report.schema.json"} {
		p := filepath.Join("schemas", n)
		if _, err := os.Lstat(p); err == nil {
			if err := add(p, "schemas/"+n, "schemas"); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	// glossary.
	if _, err := os.Lstat("glossary/glossary.json"); err == nil {
		if err := add("glossary/glossary.json", "glossary/glossary.json", "glossary"); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// Optional editorial documentation. Selection is by explicit filename
	// allowlist only; there is no recursive directory sweep.
	if editorialRoot != "" {
		docs, err := collectEditorialDocs(cfg.ProjectRoot, editorialRoot, editorialFiles)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			if err := add(d.Src, d.Rel, d.Group); err != nil {
				return nil, err
			}
		}
	}
	// compact private evidence. Large working ledgers are reduced to auditable
	// summaries so the editorial package stays reviewable; the full files
	// remain in the private evidence directory.
	compactDir := filepath.Join(cfg.WorkDir, "editorial-package-evidence")
	if materializeEvidence {
		if err := writeCompactEvidence(cfg, compactDir); err != nil {
			return nil, err
		}
	}
	evidence := []string{
		"p01c_census.json",
		"p01c_census_by_function.csv",
		"p01c_census_by_bank.csv",
		"p01c_promotion_audit.json",
		"p01c_examples.json",
		"p01c_speaker_candidates.tsv",
		"p01c_branch_map.json",
		"p01c_voice_audit.json",
		"p01c_name_lookup.json",
		"p01c_source_to_shard.json",
		"p01b_callsite_index.json",
	}
	for _, n := range evidence {
		p := filepath.Join(cfg.EvidenceDir, n)
		if _, err := os.Lstat(p); err == nil {
			if err := add(p, "private-evidence/"+n, "private_evidence"); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if materializeEvidence {
		for _, n := range []string{"p01c_migration_summary.json", "p01c_preimage_summary.json", "p01c_scene_summary.json", "p01c_translator_view_sample.tsv"} {
			p := filepath.Join(compactDir, n)
			if _, err := os.Lstat(p); err == nil {
				if err := add(p, "private-evidence/"+n, "private_evidence"); err != nil {
					return nil, err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	if _, err := os.Lstat(filepath.Join("config", "project.json")); err == nil {
		if err := add(filepath.Join("config", "project.json"), "config/project.json", "config"); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Rel < items[j].Rel })
	return items, nil
}

// parseEditorialFiles validates the explicit filename allowlist. Names are
// basenames only: no directories, absolute paths, parent components or hidden
// files. .md/.txt are the only allowed document types. An empty allowlist means
// no editorial documents.
func parseEditorialFiles(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, item := range strings.Split(raw, ",") {
		name := strings.TrimSpace(item)
		if name == "" {
			return nil, fmt.Errorf("empty filename in --editorial-files")
		}
		if filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("editorial filename must be a bare basename: %q", name)
		}
		if err := safeEditorialRel(name); err != nil {
			return nil, fmt.Errorf("unsafe editorial filename %q: %w", name, err)
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".md", ".txt":
		default:
			return nil, fmt.Errorf("unsupported editorial file (only .md/.txt allowed): %s", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate editorial filename: %s", name)
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// collectEditorialDocs packages only the filenames explicitly allowed by the
// caller. It never walks a directory, so a broad root cannot pull in unrelated
// documents. The root itself must be a real directory and must not be inside the
// project's reserved workspace (.local/), whose contents are excluded from
// packaging. Every selected entry must be a regular, non-symlink .md/.txt file.
func collectEditorialDocs(projectRoot, root string, names []string) ([]p01cPackItem, error) {
	if len(names) == 0 {
		return nil, nil
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("editorial docs root must be a real directory: %s", root)
	}
	excluded, err := insideReservedWorkspace(projectRoot, root)
	if err != nil {
		return nil, err
	}
	if excluded {
		return nil, fmt.Errorf("refusing to package from the project's reserved workspace: %s", root)
	}
	var items []p01cPackItem
	for _, name := range names {
		if filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("editorial filename must be a bare basename: %q", name)
		}
		if err := safeEditorialRel(name); err != nil {
			return nil, err
		}
		src := filepath.Join(root, filepath.FromSlash(name))
		st, err := os.Lstat(src)
		if err != nil {
			return nil, err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("editorial path is a symlink: %s", name)
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("editorial entry is not a regular file: %s", name)
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".md", ".txt":
		default:
			return nil, fmt.Errorf("unsupported editorial file (only .md/.txt allowed): %s", name)
		}
		items = append(items, p01cPackItem{Src: src, Rel: "editorial/" + name, Group: "editorial"})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Rel < items[j].Rel })
	return items, nil
}

// reservedWorkspaceDir is the project's working-state directory. Its contents
// are not packageable project material, so an editorial root inside it is
// rejected.
const reservedWorkspaceDir = ".local"

// insideReservedWorkspace reports whether root is the project's reserved
// workspace or a directory below it. Symlinks are resolved before comparison so
// an intermediate link cannot bypass the exclusion.
func insideReservedWorkspace(projectRoot, root string) (bool, error) {
	workspace, err := filepath.Abs(filepath.Join(projectRoot, reservedWorkspaceDir))
	if err != nil {
		return false, err
	}
	target, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = resolved
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	rel, err := filepath.Rel(workspace, target)
	if err != nil {
		return false, nil
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}

func safeRel(rel string) error {
	if rel == "" || strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) || strings.Contains(rel, "\\") {
		return fmt.Errorf("unsafe relative path: %q", rel)
	}
	if path.Clean(rel) != rel || rel == "." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return fmt.Errorf("unsafe relative path: %q", rel)
	}
	return nil
}

func safeEditorialRel(rel string) error {
	if err := safeRel(rel); err != nil {
		return err
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return fmt.Errorf("unsafe editorial path component in %q", rel)
		}
	}
	return nil
}

// buildRedactions derives replacement pairs from the active configuration and
// the user home directory. No machine-specific path is hard-coded.
func buildRedactions(cfg importer.Config) ([]redaction, error) {
	var out []redaction
	add := func(p, label string) error {
		if strings.TrimSpace(p) == "" {
			return nil
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		abs = filepath.Clean(abs)
		if abs == "" || abs == string(os.PathSeparator) || abs == "." {
			return nil
		}
		out = append(out, redaction{from: abs + string(os.PathSeparator), to: label + "/"})
		out = append(out, redaction{from: abs, to: label})
		return nil
	}
	if err := add(cfg.SourceRoot, "<source-root>"); err != nil {
		return nil, err
	}
	if err := add(cfg.ProjectRoot, "<project-root>"); err != nil {
		return nil, err
	}
	if home, err := os.UserHomeDir(); err == nil {
		if err := add(home, "<home>"); err != nil {
			return nil, err
		}
	}
	// A longer path must be matched before a shorter one (source root before
	// project root before home).
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].from) > len(out[j].from) })
	return out, nil
}

func redactionSources(redactions []redaction) []string {
	out := make([]string, 0, len(redactions))
	for _, r := range redactions {
		out = append(out, r.from)
	}
	return out
}

func sanitizableDoc(rel string) bool {
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "editorial/") && !strings.HasPrefix(rel, "private-evidence/") {
		return false
	}
	ext := strings.ToLower(path.Ext(rel))
	return ext == ".md" || ext == ".txt"
}

// sanitizeStaged rewrites only staged editorial/evidence documentation copies.
// Every other staged file must be byte-identical to its source. A remaining
// absolute path anywhere in the tree is a hard failure.
func sanitizeStaged(root string, redactions []redaction) error {
	before, err := hashTree(root)
	if err != nil {
		return err
	}
	repl := make([]string, 0, len(redactions)*2)
	for _, r := range redactions {
		repl = append(repl, r.from, r.to)
	}
	replacer := strings.NewReplacer(repl...)
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in staged package: %s", p)
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !sanitizableDoc(rel) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n := replacer.Replace(string(b))
		if n == string(b) {
			return nil
		}
		return os.WriteFile(p, []byte(n), 0o644)
	})
	if err != nil {
		return err
	}
	after, err := hashTree(root)
	if err != nil {
		return err
	}
	for rel, h := range before {
		if after[rel] != h && !sanitizableDoc(rel) {
			return fmt.Errorf("sanitizer modified a non-document file: %s", rel)
		}
	}
	forbidden := []string{"/home/", "/Users/", `C:\Users\`}
	forbidden = append(forbidden, redactionSources(redactions)...)
	for rel := range after {
		p := filepath.Join(root, filepath.FromSlash(rel))
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, f := range forbidden {
			if f != "" && strings.Contains(string(b), f) {
				return fmt.Errorf("staged package still contains an absolute/private path (%s): %s", f, rel)
			}
		}
	}
	return nil
}

func hashTree(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in staged package: %s", p)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = shaBytes(b)
		return nil
	})
	return out, err
}

func copyFile(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular package input: %s", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func tarZst(dir, out string) error {
	cmd := exec.Command("tar", "--zstd", "-cf", out, "-C", dir, ".")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func removeOwnedDir(work, dir string) error {
	absWork, err := filepath.Abs(work)
	if err != nil {
		return err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if absDir == absWork || !strings.HasPrefix(absDir, absWork+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to clean path outside work dir: %s", absDir)
	}
	if _, err := os.Lstat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.RemoveAll(dir)
}

func removeOwnedFile(work, file string) error {
	absWork, err := filepath.Abs(work)
	if err != nil {
		return err
	}
	absFile, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	if absFile == absWork || !strings.HasPrefix(absFile, absWork+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to remove file outside work dir: %s", absFile)
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func mustMarshalIndent(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

// contentGate rejects source bytes, absolute user paths, keys and runtime
// traces from the editorial package.
func contentGate(root string, forbidden []string) (map[string]any, error) {
	var violations []string
	patterns := []struct {
		name string
		sub  string
	}{
		{"absolute_home_path", "/home/"},
		{"absolute_users_path", "/Users/"},
		{"absolute_windows_path", `C:\Users\`},
		{"msbt_magic", "MsgStdBn"},
		{"msbp_magic", "MsgPrjBn"},
		{"romfs_path", "romfs/"},
		{"exefs_path", "exefs/"},
		{"audio_asset", ".bfsar"},
		{"audio_asset2", ".bfstm"},
		{"private_key", "BEGIN PRIVATE KEY"},
	}
	for _, f := range forbidden {
		if f != "" {
			patterns = append(patterns, struct {
				name string
				sub  string
			}{"private_absolute_path", f})
		}
	}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in package: %s", p)
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		isDoc := strings.HasSuffix(relSlash, ".md") || strings.HasSuffix(relSlash, ".txt") || strings.HasSuffix(relSlash, ".csv")
		for _, pat := range patterns {
			// Magic words are documentation-safe in prose but not in data files.
			if isDoc && (pat.name == "msbt_magic" || pat.name == "msbp_magic") {
				continue
			}
			if strings.Contains(string(b), pat.sub) {
				violations = append(violations, fmt.Sprintf("%s: %s", relSlash, pat.name))
			}
		}
		low := strings.ToLower(relSlash)
		for _, ext := range []string{".tik", ".cert", ".nca", ".nacp", ".cnmt", ".bfsar", ".bfstm", ".msbt", ".msbp", ".zst", ".elf"} {
			if strings.HasSuffix(low, ext) {
				violations = append(violations, fmt.Sprintf("%s: forbidden extension %s", relSlash, ext))
			}
		}
		// Do not allow giant opaque files in the editorial package.
		if len(b) > 8<<20 {
			violations = append(violations, fmt.Sprintf("%s: file larger than 8 MiB", relSlash))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	status := "PASS"
	if len(violations) > 0 {
		status = "FAIL"
	}
	return map[string]any{"schema_version": 1, "status": status, "violations": violations, "checked_forbidden": len(patterns), "note": "Editorial package content gate: source bytes, keys and machine-specific absolute paths are rejected."}, nil
}

// writeCompactEvidence reduces large private working ledgers to auditable
// summaries for the cumulative editorial package. It never changes the full
// private evidence files.
func writeCompactEvidence(cfg importer.Config, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	// Migration ledger summary.
	if b, err := os.ReadFile(filepath.Join(cfg.EvidenceDir, "p01c_migration_ledger.json")); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			delete(m, "record_to_shard")
			m["record_to_shard"] = "omitted from the compact package; the full map remains in the private evidence directory"
			m["compact_note"] = "The full old->new record map stays private-local. Counts, hashes and the shard mapping are complete here."
			if err := os.WriteFile(filepath.Join(outDir, "p01c_migration_summary.json"), mustMarshalIndent(m), 0o644); err != nil {
				return err
			}
		}
	}
	// Preimage summary.
	if b, err := os.ReadFile(filepath.Join(cfg.EvidenceDir, "p01c_preimage.json")); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			delete(m, "records")
			m["records"] = "omitted from the compact package; per-record text hashes remain in the private evidence directory"
			m["compact_note"] = "The full per-record preimage is private-local. Counts, hashes and the preimage list are complete here."
			if err := os.WriteFile(filepath.Join(outDir, "p01c_preimage_summary.json"), mustMarshalIndent(m), 0o644); err != nil {
				return err
			}
		}
	}
	// Scene map summary.
	if b, err := os.ReadFile(filepath.Join(cfg.EvidenceDir, "p01c_scene_map.json")); err == nil {
		var v struct {
			SchemaVersion int `json:"schema_version"`
			Groups        []struct {
				Bank    string         `json:"bank"`
				Group   string         `json:"authored_group"`
				Records int            `json:"records"`
				ByRole  map[string]int `json:"by_role"`
			} `json:"groups"`
		}
		if json.Unmarshal(b, &v) == nil {
			byBank := map[string]int{}
			byRole := map[string]int{}
			for _, g := range v.Groups {
				byBank[g.Bank]++
				for r, n := range g.ByRole {
					byRole[r] += n
				}
			}
			sort.Slice(v.Groups, func(i, j int) bool {
				if v.Groups[i].Records != v.Groups[j].Records {
					return v.Groups[i].Records > v.Groups[j].Records
				}
				return v.Groups[i].Bank+v.Groups[i].Group < v.Groups[j].Bank+v.Groups[j].Group
			})
			if len(v.Groups) > 300 {
				v.Groups = v.Groups[:300]
			}
			out := map[string]any{
				"schema_version":  1,
				"total_groups":    len(byBank),
				"groups_by_bank":  byBank,
				"records_by_role": byRole,
				"top_groups":      v.Groups,
				"note":            "Compact scene-group summary. The full private scene map remains private-local; this lists the 300 largest groups.",
			}
			if err := os.WriteFile(filepath.Join(outDir, "p01c_scene_summary.json"), mustMarshalIndent(out), 0o644); err != nil {
				return err
			}
		}
	}
	// Translator view sample.
	if f, err := os.Open(filepath.Join(cfg.EvidenceDir, "p01c_translator_view.tsv")); err == nil {
		defer f.Close()
		dst, err := os.Create(filepath.Join(outDir, "p01c_translator_view_sample.tsv"))
		if err != nil {
			return err
		}
		defer dst.Close()
		// Read line by line without a large allocation.
		br, err := newLineReader(f)
		if err != nil {
			return err
		}
		n := 0
		for {
			line, ok, err := br.next()
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			if n == 0 || n <= 5000 {
				if _, err := dst.WriteString(line + "\n"); err != nil {
					return err
				}
			}
			n++
			if n > 5000 {
				break
			}
		}
	}
	return nil
}

// minimal line reader to avoid adding a bufio import to the package tool
type lineReader struct {
	b   []byte
	pos int
}

func newLineReader(f *os.File) (*lineReader, error) {
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return &lineReader{b: b}, nil
}
func (r *lineReader) next() (string, bool, error) {
	if r.pos >= len(r.b) {
		return "", false, nil
	}
	start := r.pos
	for r.pos < len(r.b) && r.b[r.pos] != '\n' {
		r.pos++
	}
	line := string(r.b[start:r.pos])
	if r.pos < len(r.b) {
		r.pos++
	}
	return strings.TrimSuffix(line, "\r"), true, nil
}
