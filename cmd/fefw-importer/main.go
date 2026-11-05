// Command fefw-importer is the offline technical adapter for the Fire Emblem:
// Fortune's Weave message corpus. It never translates, never edits natural
// English and never writes to the immutable source tree.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"fefw/internal/importer"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfg := importer.DefaultConfig()
	fs.StringVar(&cfg.ProjectRoot, "project-root", ".", "project root (default: current directory)")
	fs.StringVar(&cfg.SourceRoot, "source-root", cfg.SourceRoot, "private staged source root (relative)")
	fs.StringVar(&cfg.TranslationsDir, "translations", cfg.TranslationsDir, "canonical translations root")
	fs.StringVar(&cfg.EvidenceDir, "evidence", cfg.EvidenceDir, "private evidence output directory")
	fs.StringVar(&cfg.ReportsDir, "reports", cfg.ReportsDir, "private reports output directory")
	fs.StringVar(&cfg.WorkDir, "work", cfg.WorkDir, "private work directory")
	dryRun := fs.Bool("dry-run", false, "compute and print but write nothing")
	recompress := fs.Bool("recompress", false, "verify zstd recompression of every encoded bank")
	overwrite := fs.Bool("overwrite-unreviewed", false, "allow replacing existing all-UNREVIEWED shards")
	skipRT := fs.Bool("skip-roundtrip", false, "skip the all-bank roundtrip proof (not recommended)")
	freshImport := fs.Bool("fresh-import", false, "validate as a fresh source import: reject reviewed records and non-null real_en")
	_ = fs.Parse(os.Args[2:])
	cfg.DryRun = *dryRun
	cfg.Recompress = *recompress
	if cfg.ProjectRoot != "." && cfg.ProjectRoot != "" {
		if err := os.Chdir(cfg.ProjectRoot); err != nil {
			fatal(err)
		}
		cfg.ProjectRoot = "."
	}

	switch cmd {
	case "inventory":
		runInventory(cfg)
	case "roundtrip":
		runRoundtrip(cfg)
	case "import":
		runImport(cfg, *overwrite, *skipRT)
	case "all":
		runImport(cfg, *overwrite, *skipRT)
	case "validate":
		runValidate(cfg, *freshImport)
	case "consumer-map":
		if err := importer.WriteRuntimeConsumerMap(cfg); err != nil {
			fatal(err)
		}
		fmt.Println("wrote RUNTIME_CONSUMER_MAP.json (UNKNOWN_NOT_TESTED)")
	case "hash":
		runHash(fs.Args())
	case "doctor":
		runDoctor(cfg)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `fefw-importer — offline lossless message importer

usage: fefw-importer <command> [flags]

commands:
  inventory    hash and classify staged sources; write private ledger
  roundtrip    parse and re-encode every JP/US EN bank; prove byte-exactness
  import       build canonical shards and manifest (use --dry-run first)
  all          inventory + roundtrip + import + private reports
  validate     validate shard schema, manifest, IDs and text fidelity
  consumer-map write RUNTIME_CONSUMER_MAP.json (UNKNOWN_NOT_TESTED)
  hash <path>  print SHA-256 and size for files/directories
  doctor       report local toolchain and source presence

The tool never writes to .local/_incoming, never translates Japanese and
never edits natural English.`)
}

func runInventory(cfg importer.Config) {
	inv, err := importer.Inventory(cfg)
	if err != nil {
		fatal(err)
	}
	if !cfg.DryRun {
		if err := importer.WriteLedger(cfg, inv, nil); err != nil {
			fatal(err)
		}
		if err := writeReport(cfg, "inventory.json", inv); err != nil {
			fatal(err)
		}
	}
	fmt.Printf("source files: %d (%d bytes)\n", inv.FileCount, inv.TotalBytes)
	for _, k := range sortedKeys(inv.Counts) {
		fmt.Printf("  %-16s %d\n", k, inv.Counts[k])
	}
	fmt.Printf("program_id: %s (%s)\n", inv.Identity.ProgramID, inv.Identity.ProgramSource)
	fmt.Printf("title_id: %s  version: %s  region: %s\n", inv.Identity.TitleID, inv.Identity.Version, inv.Identity.Region)
	fmt.Printf("presence: %v\n", inv.Presence)
}

func runRoundtrip(cfg importer.Config) {
	inv, err := importer.Inventory(cfg)
	if err != nil {
		fatal(err)
	}
	if led, err := importer.LoadLedger(cfg); err == nil {
		if err := importer.VerifyLedger(inv, led); err != nil {
			fatal(err)
		}
	}
	corp, err := importer.LoadCorpus(cfg)
	if err != nil {
		fatal(err)
	}
	rt, err := importer.Roundtrip(cfg, corp)
	if err != nil {
		fatal(err)
	}
	if !cfg.DryRun {
		if err := writeReport(cfg, "roundtrip_report.json", rt); err != nil {
			fatal(err)
		}
		if err := importer.WriteLedger(cfg, inv, rt); err != nil {
			fatal(err)
		}
	}
	fmt.Printf("roundtrip: %s  banks: %d  byte-exact: %d  token-fidelity: %d  non-text: %d  failures: %d\n",
		rt.Status, rt.Summary.Banks, rt.Summary.ByteExact, rt.Summary.TokenOK, rt.Summary.NonTextOK, rt.Summary.Failures)
	for _, b := range rt.Banks {
		if !b.ByteExact || !b.TokenFidelity || !b.NonTextUnchanged || b.Error != "" {
			fmt.Printf("  FAIL %s/%s byte=%v token=%v nontext=%v err=%s\n", b.Locale, b.Bank, b.ByteExact, b.TokenFidelity, b.NonTextUnchanged, b.Error)
		}
	}
}

func runImport(cfg importer.Config, overwrite, skipRT bool) {
	inv, err := importer.Inventory(cfg)
	if err != nil {
		fatal(err)
	}
	if led, err := importer.LoadLedger(cfg); err == nil {
		if err := importer.VerifyLedger(inv, led); err != nil {
			fatal(err)
		}
	}
	corp, err := importer.LoadCorpus(cfg)
	if err != nil {
		fatal(err)
	}
	var rt *importer.RoundtripReport
	if !skipRT {
		rt, err = importer.Roundtrip(cfg, corp)
		if err != nil {
			fatal(err)
		}
		if rt.Status != "PASS_ALL_BANKS" {
			fmt.Fprintf(os.Stderr, "roundtrip is not PASS: %s (%d failures)\n", rt.Status, rt.Summary.Failures)
			if !cfg.DryRun {
				fatal(fmt.Errorf("refusing to import before every bank round-trips byte-exactly"))
			}
		}
	}
	res, err := importer.Build(corp)
	if err != nil {
		fatal(err)
	}
	written, err := importer.WriteImport(cfg, res, importer.WriteOptions{DryRun: cfg.DryRun, OverwriteUnreviewed: overwrite})
	if err != nil {
		fatal(err)
	}
	if !cfg.DryRun {
		// A source import must leave every record UNREVIEWED with a null
		// real_en. This guard is deliberately strict; routine validation is a
		// separate contract that accepts an edited tree.
		vrep, err := importer.ValidateFreshImport(cfg, corp)
		if err != nil {
			fatal(err)
		}
		if vrep.Status != "PASS" {
			fatal(fmt.Errorf("fresh import validation failed: %v", vrep.Errors))
		}
		if err := importer.WriteManifestAndReports(cfg, res, rt, inv, corp); err != nil {
			fatal(err)
		}
		if rt != nil {
			if err := importer.WriteLedger(cfg, inv, rt); err != nil {
				fatal(err)
			}
		}
		if err := importer.WriteRuntimeConsumerMap(cfg); err != nil {
			fatal(err)
		}
	}
	fmt.Printf("shards: %d  records: %d  jp: %d  official_en: %d  real_en: %d\n",
		res.Manifest.Counts.Files, res.Manifest.Counts.Records, res.Manifest.Counts.JP, res.Manifest.Counts.OfficialEN, res.Manifest.Counts.RealEN)
	fmt.Printf("alignment: aligned=%d jp_only=%d en_only=%d both_empty=%d\n",
		res.Totals.Aligned, res.Totals.JPOnly, res.Totals.ENOnly, res.Totals.BothEmpty)
	fmt.Printf("usage: asset_referenced=%d unverified=%d  speaker: candidate=%d unknown=%d\n",
		res.Totals.UsageAssetReferenced, res.Totals.UsageUnverified, res.Totals.SpeakerCandidate, res.Totals.SpeakerUnknown)
	fmt.Printf("manifest sha256: %s\n", shaBytes(res.ManifestBytes))
	fmt.Printf("dry-run: %v  shards considered: %d\n", cfg.DryRun, len(written))
	if cfg.DryRun {
		for _, p := range written {
			fmt.Printf("  would write translations/%s (%d bytes)\n", p, len(res.Shards[p]))
		}
	}
}

func runValidate(cfg importer.Config, freshImport bool) {
	corp, err := importer.LoadCorpus(cfg)
	if err != nil {
		fatal(err)
	}
	var rep *importer.ValidationReport
	if freshImport {
		rep, err = importer.ValidateFreshImport(cfg, corp)
	} else {
		rep, err = importer.ValidateOutput(cfg, corp)
	}
	if err != nil {
		fatal(err)
	}
	if !cfg.DryRun {
		if err := writeReport(cfg, "validation_report.json", rep); err != nil {
			fatal(err)
		}
	}
	fmt.Printf("validation: %s  shards=%d records=%d jp=%d official_en=%d real_en=%d\n",
		rep.Status, rep.Counts["shards"], rep.Counts["records"], rep.Counts["jp"], rep.Counts["official_en"], rep.Counts["real_en"])
	fmt.Printf("checks: %v\n", rep.Checks)
	for _, e := range rep.Errors {
		fmt.Println("  ERROR:", e)
	}
	if rep.Status != "PASS" {
		os.Exit(1)
	}
}

func runHash(args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("hash needs at least one path"))
	}
	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			fatal(err)
		}
		if !info.IsDir() {
			sum, n, err := importer.HashFile(arg)
			if err != nil {
				fatal(err)
			}
			fmt.Printf("%s  %d  %s\n", sum, n, filepath.ToSlash(arg))
			continue
		}
		var files []string
		err = filepath.WalkDir(arg, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if !d.IsDir() {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			fatal(err)
		}
		sort.Strings(files)
		for _, p := range files {
			sum, n, err := importer.HashFile(p)
			if err != nil {
				fatal(err)
			}
			fmt.Printf("%s  %d  %s\n", sum, n, filepath.ToSlash(p))
		}
	}
}

func runDoctor(cfg importer.Config) {
	fmt.Printf("go: %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if p, err := os.Getwd(); err == nil {
		fmt.Printf("cwd: %s\n", filepath.Base(p))
	}
	if _, err := os.Stat(cfg.SourceRoot); err == nil {
		fmt.Printf("source-root: present\n")
	} else {
		fmt.Printf("source-root: MISSING\n")
	}
	if p, err := lookPath("zstd"); err == nil {
		fmt.Printf("zstd: %s\n", filepath.Base(p))
	} else {
		fmt.Printf("zstd: MISSING (roundtrip still works; recompress check skipped)\n")
	}
	if inv, err := importer.Inventory(cfg); err == nil {
		fmt.Printf("inventory: %d files, jp_msbt=%d en_msbt=%d\n", inv.FileCount, inv.Counts["jp_msbt"], inv.Counts["en_msbt"])
	}
}

func lookPath(name string) (string, error) {
	for _, dir := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("not found")
}

func writeReport(cfg importer.Config, name string, v any) error {
	if err := os.MkdirAll(cfg.ReportsDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(cfg.ReportsDir, name)
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(buf.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func shaBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
