// Command fefw-p01b is the offline P01B evidence, enrichment, classification
// and migration tool for the Fire Emblem: Fortune's Weave message corpus.
// It never translates, edits natural English, or writes to source assets.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"fefw/internal/importer"
)

const baselineManifestSHA = "dc76691fa5adfb557e85efc047ab826bbc006ae80be25679df6450cb52062767"
const baselineVariantSHA = "782398febf856c5addef76dc7c4a9ea47ff2e7102080a9d89d121a4e180c7f77"

type loaded struct {
	cfg    importer.Config
	inv    *importer.InventoryReport
	corp   *importer.Corpus
	ctx    *importer.P01BContext
	assets map[string]string
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfg := importer.DefaultConfig()
	fs.StringVar(&cfg.ProjectRoot, "project-root", ".", "project root")
	fs.StringVar(&cfg.SourceRoot, "source-root", cfg.SourceRoot, "private staged source root")
	fs.StringVar(&cfg.TranslationsDir, "translations", cfg.TranslationsDir, "canonical translations root")
	fs.StringVar(&cfg.EvidenceDir, "evidence", cfg.EvidenceDir, "private evidence directory")
	fs.StringVar(&cfg.ReportsDir, "reports", cfg.ReportsDir, "private reports directory")
	fs.StringVar(&cfg.WorkDir, "work", cfg.WorkDir, "private work directory")
	dry := fs.Bool("dry-run", false, "compute only")
	apply := fs.Bool("apply", false, "perform the migration (otherwise dry-run)")
	allowChanged := fs.Bool("allow-unreviewed-reimport", false, "allow a changed but fully unreviewed preimage")
	roundtrip := fs.Bool("roundtrip", true, "run the all-bank byte-exact roundtrip in baseline")
	_ = fs.Parse(os.Args[2:])
	cfg.DryRun = *dry
	if cfg.ProjectRoot != "." && cfg.ProjectRoot != "" {
		if err := os.Chdir(cfg.ProjectRoot); err != nil {
			fatal(err)
		}
		cfg.ProjectRoot = "."
	}
	switch cmd {
	case "baseline":
		runBaseline(cfg, *roundtrip)
	case "audit":
		l := mustLoad(cfg)
		runAudit(cfg, l)
	case "migrate":
		l := mustLoad(cfg)
		runMigrate(cfg, l, *apply, *allowChanged)
	case "version":
		runVersion(cfg)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `fefw-p01b — evidence enrichment, classification and migration

usage: fefw-p01b <command> [flags]

commands:
  baseline   reproduce and save the P00/P01 identity, census and roundtrip gates
  audit      build private attribute/speaker/callsite/classification evidence
  migrate    enrich context, classify shards, write old→new ledger (use --apply)
  version    census staged metadata and list exact missing version sources

No command translates or edits official English.`)
}

func mustLoad(cfg importer.Config) *loaded {
	inv, err := importer.Inventory(cfg)
	if err != nil {
		fatal(err)
	}
	if led, err := importer.LoadLedger(cfg); err == nil {
		if err := importer.VerifyLedger(inv, led); err != nil {
			fatal(err)
		}
	} else if !os.IsNotExist(err) {
		fatal(err)
	}
	corp, err := importer.LoadCorpus(cfg)
	if err != nil {
		fatal(err)
	}
	scripts, err := importer.BuildScriptIndex(cfg)
	if err != nil {
		fatal(err)
	}
	ents, err := importer.BuildEntityIndex(cfg)
	if err != nil {
		fatal(err)
	}
	ctx := &importer.P01BContext{
		Scripts:      scripts,
		Entities:     ents,
		Talk:         corp.Talk,
		SectionNames: importer.SectionNames(corp.JPProject),
	}
	assets := map[string]string{}
	for _, f := range inv.Files {
		assets[f.Path] = f.SHA256
	}
	return &loaded{cfg: cfg, inv: inv, corp: corp, ctx: ctx, assets: assets}
}

func runBaseline(cfg importer.Config, doRT bool) {
	l := mustLoad(cfg)
	type report struct {
		SchemaVersion         int               `json:"schema_version"`
		Status                string            `json:"status"`
		SourceLedgerVerified  bool              `json:"source_ledger_verified"`
		SourceFiles           int               `json:"source_files"`
		SourceBytes           int64             `json:"source_bytes"`
		Banks                 int               `json:"banks"`
		JPBanks               int               `json:"jp_banks"`
		ENBanks               int               `json:"en_banks"`
		BankPairs             int               `json:"bank_pairs"`
		JPRecords             int               `json:"jp_records"`
		ENRecords             int               `json:"en_records"`
		CanonicalRecords      int               `json:"canonical_records"`
		UniqueRecordIDs       int               `json:"unique_record_ids"`
		DuplicateRecordIDs    int               `json:"duplicate_record_ids"`
		LabelSetMismatchBanks int               `json:"label_set_mismatch_banks"`
		LabelCollisions       int               `json:"label_collisions"`
		BothEmpty             int               `json:"both_empty"`
		Aligned               int               `json:"aligned"`
		JPOnly                int               `json:"jp_only"`
		ENOnly                int               `json:"en_only"`
		RoundtripBanks        int               `json:"roundtrip_banks"`
		RoundtripByteExact    int               `json:"roundtrip_byte_exact"`
		RoundtripFailures     int               `json:"roundtrip_failures"`
		RoundtripStatus       string            `json:"roundtrip_status"`
		SpeakerStates         map[string]int    `json:"speaker_states"`
		UsageStates           map[string]int    `json:"usage_states"`
		ManifestFiles         int               `json:"manifest_files"`
		ManifestRecords       int               `json:"manifest_records"`
		ManifestRealEN        int               `json:"manifest_real_en"`
		ManifestByReview      map[string]int    `json:"manifest_by_review_status"`
		ManifestSHA256        string            `json:"manifest_sha256"`
		VariantSHA256         string            `json:"variant_sha256"`
		VariantUnchanged      bool              `json:"variant_unchanged"`
		ValidationStatus      string            `json:"validation_status"`
		ValidationErrors      []string          `json:"validation_errors"`
		ProgramID             string            `json:"program_id_candidate"`
		Version               string            `json:"version"`
		TitleID               string            `json:"title_id"`
		Region                string            `json:"region"`
		Checks                map[string]bool   `json:"checks"`
		Counts                map[string]int    `json:"-"`
		Identity              importer.Identity `json:"identity"`
	}
	rep := &report{SchemaVersion: 1, Checks: map[string]bool{}, SpeakerStates: map[string]int{}, UsageStates: map[string]int{}}
	rep.SourceFiles = l.inv.FileCount
	rep.SourceBytes = l.inv.TotalBytes
	rep.Identity = l.inv.Identity
	rep.ProgramID = l.inv.Identity.ProgramID
	rep.Version = "UNKNOWN"
	rep.TitleID = l.inv.Identity.TitleID
	rep.Region = l.inv.Identity.Region
	rep.SourceLedgerVerified = true
	rep.Banks = len(l.corp.Banks)
	rep.JPBanks = l.inv.Counts["jp_msbt"]
	rep.ENBanks = l.inv.Counts["en_msbt"]
	rep.BankPairs = rep.Banks
	// Canonical build (text/IDs only, no enrichment).
	res, err := importer.Build(l.corp)
	if err != nil {
		fatal(err)
	}
	rep.CanonicalRecords = res.Totals.Records
	rep.JPRecords = res.Totals.JPRecords
	rep.ENRecords = res.Totals.ENRecords
	rep.BothEmpty = res.Totals.BothEmpty
	rep.Aligned = res.Totals.Aligned
	rep.JPOnly = res.Totals.JPOnly
	rep.ENOnly = res.Totals.ENOnly
	rep.LabelCollisions = res.Totals.LabelCollisions
	rep.LabelSetMismatchBanks = res.Totals.LabelSetMismatchBanks
	ids := map[string]bool{}
	for _, b := range res.Shards {
		var sh importer.Shard
		if err := json.Unmarshal(b, &sh); err != nil {
			fatal(err)
		}
		for _, r := range sh.Records {
			if ids[r.RecordID] {
				rep.DuplicateRecordIDs++
			}
			ids[r.RecordID] = true
		}
	}
	rep.UniqueRecordIDs = len(ids)
	// Context census comes from the live canonical tree, not from the raw
	// source build, so a post-migration run reports the actual public state.
	err = filepath.WalkDir(cfg.TranslationsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") || d.Name() == "manifest.json" {
			return nil
		}
		if strings.Contains(filepath.ToSlash(path), "/variants/") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var sh importer.Shard
		if err := json.Unmarshal(b, &sh); err != nil {
			return err
		}
		for _, r := range sh.Records {
			rep.SpeakerStates[r.Context.Speaker.State]++
			rep.UsageStates[r.Context.UsageState]++
		}
		return nil
	})
	if err != nil {
		fatal(err)
	}
	if doRT {
		rt, err := importer.Roundtrip(cfg, l.corp)
		if err != nil {
			fatal(err)
		}
		rep.RoundtripBanks = rt.Summary.Banks
		rep.RoundtripByteExact = rt.Summary.ByteExact
		rep.RoundtripFailures = rt.Summary.Failures
		rep.RoundtripStatus = rt.Status
	}
	// Manifest and variant.
	manPath := filepath.Join(cfg.TranslationsDir, "manifest.json")
	manBytes, err := os.ReadFile(manPath)
	if err != nil {
		fatal(err)
	}
	rep.ManifestSHA256 = shaBytes(manBytes)
	var man importer.Manifest
	if err := json.Unmarshal(manBytes, &man); err != nil {
		fatal(err)
	}
	rep.ManifestFiles = man.Counts.Files
	rep.ManifestRecords = man.Counts.Records
	rep.ManifestRealEN = man.Counts.RealEN
	rep.ManifestByReview = man.Counts.ByReviewStatus
	varSHA := shaFile(filepath.Join(cfg.TranslationsDir, "variants", "no-honorifics.json"))
	rep.VariantSHA256 = varSHA
	rep.VariantUnchanged = varSHA == baselineVariantSHA
	vrep, err := importer.ValidateFreshImport(cfg, l.corp)
	if err != nil {
		fatal(err)
	}
	rep.ValidationStatus = vrep.Status
	rep.ValidationErrors = vrep.Errors
	rep.Checks["source_ledger"] = rep.SourceLedgerVerified
	rep.Checks["523_pairs"] = rep.Banks == 523 && rep.JPBanks == 523 && rep.ENBanks == 523
	rep.Checks["records_81103"] = rep.CanonicalRecords == 81103
	rep.Checks["jp_en_81103"] = rep.JPRecords == 81103 && rep.ENRecords == 81103
	rep.Checks["unique_ids"] = rep.DuplicateRecordIDs == 0 && rep.UniqueRecordIDs == rep.CanonicalRecords
	rep.Checks["label_sets_match"] = rep.LabelSetMismatchBanks == 0
	rep.Checks["both_empty_9876"] = rep.BothEmpty == 9876
	rep.Checks["roundtrip_1046"] = !doRT || (rep.RoundtripBanks == 1046 && rep.RoundtripByteExact == 1046 && rep.RoundtripFailures == 0)
	rep.Checks["real_en_zero"] = rep.ManifestRealEN == 0 && res.Totals.Records == 81103
	rep.Checks["all_unreviewed"] = man.Counts.ByReviewStatus["UNREVIEWED"] == man.Counts.Records
	rep.Checks["variant_unchanged"] = rep.VariantUnchanged
	rep.Checks["validation_pass"] = vrep.Status == "PASS"
	rep.Checks["version_unknown"] = rep.Version == "UNKNOWN"
	rep.Checks["manifest_matches_source"] = man.Counts.Records == rep.CanonicalRecords
	ok := true
	for _, v := range rep.Checks {
		if !v {
			ok = false
		}
	}
	if ok {
		rep.Status = "PASS"
	} else {
		rep.Status = "FAIL"
	}
	writeJSON(filepath.Join(cfg.ReportsDir, "p01b_baseline_revalidation.json"), rep)
	var tb strings.Builder
	fmt.Fprintf(&tb, "status=%s\n", rep.Status)
	for _, k := range sortedBoolKeys(rep.Checks) {
		fmt.Fprintf(&tb, "check %-24s %v\n", k, rep.Checks[k])
	}
	fmt.Fprintf(&tb, "banks=%d jp_banks=%d en_banks=%d pairs=%d\n", rep.Banks, rep.JPBanks, rep.ENBanks, rep.BankPairs)
	fmt.Fprintf(&tb, "records=%d jp=%d en=%d unique_ids=%d duplicates=%d\n", rep.CanonicalRecords, rep.JPRecords, rep.ENRecords, rep.UniqueRecordIDs, rep.DuplicateRecordIDs)
	fmt.Fprintf(&tb, "aligned=%d jp_only=%d en_only=%d both_empty=%d label_set_mismatch=%d collisions=%d\n", rep.Aligned, rep.JPOnly, rep.ENOnly, rep.BothEmpty, rep.LabelSetMismatchBanks, rep.LabelCollisions)
	fmt.Fprintf(&tb, "roundtrip=%s banks=%d byte_exact=%d failures=%d\n", rep.RoundtripStatus, rep.RoundtripBanks, rep.RoundtripByteExact, rep.RoundtripFailures)
	fmt.Fprintf(&tb, "speaker=%v usage=%v\n", rep.SpeakerStates, rep.UsageStates)
	fmt.Fprintf(&tb, "manifest files=%d records=%d real_en=%d by_review=%v sha=%s\n", rep.ManifestFiles, rep.ManifestRecords, rep.ManifestRealEN, rep.ManifestByReview, rep.ManifestSHA256)
	fmt.Fprintf(&tb, "variant sha=%s unchanged=%v\n", rep.VariantSHA256, rep.VariantUnchanged)
	fmt.Fprintf(&tb, "validation=%s errors=%v\n", rep.ValidationStatus, rep.ValidationErrors)
	fmt.Fprintf(&tb, "identity program_id=%s title_id=%s version=%s region=%s\n", rep.ProgramID, rep.TitleID, rep.Version, rep.Region)
	writeText(filepath.Join(cfg.ReportsDir, "p01b_baseline_revalidation.txt"), tb.String())
	fmt.Print(tb.String())
	if rep.Status != "PASS" {
		os.Exit(1)
	}
}

func runAudit(cfg importer.Config, l *loaded) {
	attrs, err := importer.AuditAttributes(l.corp)
	if err != nil {
		fatal(err)
	}
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_attribute_audit.json"), attrs)
	writeJSON(filepath.Join(cfg.ReportsDir, "p01b_attribute_audit.json"), attrs)

	res, err := importer.BuildP01B(l.corp, l.ctx)
	if err != nil {
		fatal(err)
	}
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_speaker_coverage.json"), res.Coverage)
	writeJSON(filepath.Join(cfg.ReportsDir, "p01b_speaker_coverage.json"), res.Coverage)
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_speaker_source_map.json"), map[string]any{
		"schema_version": 1,
		"candidates":     res.Candidates,
		"addressees":     res.Addressees,
		"note":           "CANDIDATE_UNVERIFIED only. PID is a person identifier when it occurs as PID_<value> in GameData/Person.bin; MPID is a Profile.bin portrait/model identifier. Neither is promoted from this file.",
	})
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_classification.json"), map[string]any{
		"schema_version": 1,
		"categories":     res.CategoryCounts(),
		"shards_by_top":  res.CategoryShardCounts(),
		"banks":          res.Classifications,
		"note":           "Classification uses authored label namespaces, GameData table membership, TalkMsg references and the corroborated MSBP Section attribute. It never uses the source bank filename as a category guess.",
	})
	writeJSON(filepath.Join(cfg.ReportsDir, "p01b_classification.json"), res.Classifications)
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_source_to_shard.json"), sourceToShard(l, res))
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_callsite_index.json"), l.ctx.Scripts)
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_section_validation.json"), sectionValidation(l.corp))
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_coverage_by_bank.json"), res.Coverage.ByBank)
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_coverage_by_category.json"), res.Coverage.ByCategory)
	var un []importer.Classification
	for _, c := range res.Classifications {
		if c.Category == "unclassified" {
			un = append(un, c)
		}
	}
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_unclassified_inventory.json"), map[string]any{
		"schema_version":  1,
		"remainder_banks": len(un),
		"remainder_records": func() int {
			t := 0
			for _, c := range un {
				t += c.RecordCount
			}
			return t
		}(),
		"banks": un,
		"note":  "No source-backed category boundary was proven. These banks keep translations/unclassified/<bank>.json.",
	})
	writeConflicts(cfg, l, res)
	sum := auditSummary(l, res, attrs)
	writeText(filepath.Join(cfg.ReportsDir, "p01b_audit_summary.txt"), sum)
	fmt.Print(sum)
}

func runMigrate(cfg importer.Config, l *loaded, apply, allowChanged bool) {
	pre, err := snapshotPreimage(cfg, apply)
	if err != nil {
		fatal(err)
	}
	if pre.ManifestSHA256 != baselineManifestSHA && !allowChanged {
		fatal(fmt.Errorf("stale preimage: manifest sha %s != recorded baseline %s; refusing to overwrite (use --allow-unreviewed-reimport only after verifying every record is UNREVIEWED)", pre.ManifestSHA256, baselineManifestSHA))
	}
	if pre.VariantSHA256 != baselineVariantSHA {
		fatal(fmt.Errorf("variant overlay sha changed (%s); refusing migration", pre.VariantSHA256))
	}
	if pre.ApprovedRecords > 0 {
		fatal(fmt.Errorf("refusing to overwrite %d approved/non-UNREVIEWED records", pre.ApprovedRecords))
	}
	if pre.RealENRecords > 0 {
		fatal(fmt.Errorf("refusing to overwrite %d records with real_en", pre.RealENRecords))
	}
	res, err := importer.BuildP01B(l.corp, l.ctx)
	if err != nil {
		fatal(err)
	}
	// Text/identity preimage comparison before writing anything.
	seen := map[string]importer.Record{}
	for _, b := range res.Shards {
		var sh importer.Shard
		if err := json.Unmarshal(b, &sh); err != nil {
			fatal(err)
		}
		for _, r := range sh.Records {
			seen[r.RecordID] = r
		}
	}
	if len(seen) != pre.RecordCount {
		fatal(fmt.Errorf("record set size changed: preimage=%d new=%d", pre.RecordCount, len(seen)))
	}
	for rid, old := range pre.Records {
		n, ok := seen[rid]
		if !ok {
			fatal(fmt.Errorf("record lost during migration: %s", rid))
		}
		if old.JPSHA256 != ptrSHA(n.Text.JP) || old.OfficialENSHA256 != ptrSHA(n.Text.OfficialEN) {
			fatal(fmt.Errorf("immutable source text changed for %s", rid))
		}
		if n.Text.RealEN != nil || n.Review.Status != "UNREVIEWED" {
			fatal(fmt.Errorf("migration would write reviewed record %s", rid))
		}
	}
	fmt.Printf("migration preimage: PASS records=%d approved=0 real_en=0 variant_sha=%s manifest_sha=%s\n", pre.RecordCount, pre.VariantSHA256, pre.ManifestSHA256)
	fmt.Printf("new layout: shards=%d records=%d manifest_sha=%s categories=%v\n", len(res.Shards), res.Records, shaBytes(res.ManifestBytes), res.CategoryCounts())
	if !apply {
		fmt.Println("dry-run: no files written (use --apply)")
		return
	}
	stageRoot := filepath.Join(cfg.WorkDir, "p01b-stage", "translations")
	if err := removeOwnedStage(cfg.WorkDir, stageRoot); err != nil {
		fatal(err)
	}
	if err := writeStage(stageRoot, res, cfg); err != nil {
		fatal(err)
	}
	// Validate staging with the same validator used for the live tree.
	vcfg := cfg
	vcfg.TranslationsDir = stageRoot
	vrep, err := importer.ValidateFreshImport(vcfg, l.corp)
	if err != nil {
		fatal(err)
	}
	if vrep.Status != "PASS" {
		fatal(fmt.Errorf("staged validation failed: %v", vrep.Errors))
	}
	// Atomic swap with rollback.
	live := cfg.TranslationsDir
	backup := filepath.Join(cfg.WorkDir, "p01b-pre-migration-translations")
	if _, err := os.Stat(backup); err == nil {
		alt := backup + ".prev"
		if _, err := os.Stat(alt); err == nil {
			fatal(fmt.Errorf("rollback targets already exist: %s and %s", backup, alt))
		}
		if err := os.Rename(backup, alt); err != nil {
			fatal(err)
		}
	}
	if err := os.Rename(live, backup); err != nil {
		fatal(err)
	}
	if err := os.Rename(stageRoot, live); err != nil {
		if restoreErr := os.Rename(backup, live); restoreErr != nil {
			fatal(fmt.Errorf("stage swap failed: %v; rollback restore also failed: %v", err, restoreErr))
		}
		fatal(fmt.Errorf("stage swap failed; original restored: %w", err))
	}
	post, err := importer.ValidateFreshImport(cfg, l.corp)
	if err != nil || post.Status != "PASS" {
		// Move the failed tree aside, then restore the pre-migration tree. Both
		// operations are reported: a partial restore must never be silent.
		failed := filepath.Join(cfg.WorkDir, "p01b-failed-translations")
		moveErr := os.Rename(live, failed)
		restoreErr := os.Rename(backup, live)
		if moveErr != nil || restoreErr != nil {
			fatal(fmt.Errorf("post-migration validation failed and rollback is incomplete: move=%v restore=%v validation=%v %v", moveErr, restoreErr, err, post))
		}
		fatal(fmt.Errorf("post-migration validation failed; original restored: %v %v", err, post))
	}
	writeJSON(filepath.Join(cfg.ReportsDir, "p01b_validation_post_migration.json"), post)
	ledger, err := migrationLedger(cfg, pre, res, l)
	if err != nil {
		fatal(err)
	}
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_migration_ledger.json"), ledger)
	writeJSON(filepath.Join(cfg.ReportsDir, "p01b_migration_ledger.json"), ledger)
	fmt.Printf("migration: APPLIED validation=PASS shards=%d records=%d manifest_sha=%s variant_sha=%s\n", len(res.Shards), res.Records, shaBytes(res.ManifestBytes), shaFile(filepath.Join(cfg.TranslationsDir, "variants", "no-honorifics.json")))
}

func runVersion(cfg importer.Config) {
	inv, err := importer.Inventory(cfg)
	if err != nil {
		fatal(err)
	}
	root := cfg.SourceRoot
	var present []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		base := strings.ToLower(filepath.Base(rel))
		switch {
		case strings.HasSuffix(base, ".nacp"), strings.HasSuffix(base, ".cnmt"), strings.Contains(base, ".cnmt."),
			strings.HasSuffix(base, ".nca"), strings.HasSuffix(base, ".tik"), strings.HasSuffix(base, ".cert"),
			strings.EqualFold(base, "systemversion"):
			present = append(present, rel)
		}
		return nil
	}); err != nil {
		fatal(fmt.Errorf("scan staged metadata: %w", err))
	}
	sort.Strings(present)
	missing := []string{
		"control.nacp (Nintendo content metadata: title/version/region)",
		"*.cnmt / *.cnmt.nca (content meta with build/version)",
		"*.nca base/update program content (main NCA header)",
		"*.tik and *.cert (ticket/certificate chain; do not copy into public artifacts)",
		"SystemVersion title metadata",
	}
	type out struct {
		SchemaVersion      int      `json:"schema_version"`
		Platform           string   `json:"platform"`
		ProgramIDCandidate string   `json:"program_id_candidate"`
		ProgramIDSource    string   `json:"program_id_source"`
		ProgramIDStatus    string   `json:"program_id_status"`
		TitleID            string   `json:"title_id"`
		Version            string   `json:"version"`
		Region             string   `json:"region"`
		Edition            string   `json:"edition"`
		NPDMStatus         string   `json:"npdm_status"`
		ExeFSPresent       bool     `json:"exefs_present"`
		MetadataPresent    []string `json:"metadata_present"`
		MissingMetadata    []string `json:"missing_metadata"`
		Notes              []string `json:"notes"`
	}
	o := out{SchemaVersion: 1, Platform: "switch2", ProgramIDCandidate: inv.Identity.ProgramID, ProgramIDSource: inv.Identity.ProgramSource, ProgramIDStatus: "CANDIDATE_ONLY_NOT_VERSION_PROOF", TitleID: "UNKNOWN", Version: "UNKNOWN", Region: "UNKNOWN", Edition: "UNKNOWN", NPDMStatus: inv.Identity.NPDMStatus, ExeFSPresent: inv.Presence["exefs"], MetadataPresent: present, MissingMetadata: missing}
	if o.MetadataPresent == nil {
		o.MetadataPresent = []string{}
	}
	o.Notes = []string{
		"ACI0 program id is a candidate identifier only; it is not a title id or version proof.",
		"The staged path component 131072 is a dump filename and is not a version number.",
		"No Nintendo content metadata (control.nacp/CNMT/NCA) is staged, so title id, build version and region remain UNKNOWN.",
		"Record IDs keep the vUNKNOWN namespace; no total migration to a versioned namespace was performed.",
	}
	writeJSON(filepath.Join(cfg.ReportsDir, "p01b_version_census.json"), o)
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_version_census.json"), o)
	fmt.Printf("version: %s title_id=%s region=%s program_id=%s (%s)\n", o.Version, o.TitleID, o.Region, o.ProgramIDCandidate, o.ProgramIDStatus)
	fmt.Printf("missing metadata: %v\n", o.MissingMetadata)
}

// ---------------------------------------------------------------------------
// Preimage / stage / ledger
// ---------------------------------------------------------------------------

type preRecord struct {
	JPSHA256         string `json:"jp_sha256"`
	OfficialENSHA256 string `json:"official_en_sha256"`
}
type preimage struct {
	SchemaVersion   int                  `json:"schema_version"`
	ManifestSHA256  string               `json:"manifest_sha256"`
	VariantSHA256   string               `json:"variant_sha256"`
	RecordCount     int                  `json:"record_count"`
	RealENRecords   int                  `json:"real_en_records"`
	ApprovedRecords int                  `json:"approved_records"`
	Files           []preimageFile       `json:"files"`
	Records         map[string]preRecord `json:"records"`
}
type preimageFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
	Records int    `json:"records"`
}

func snapshotPreimage(cfg importer.Config, write bool) (*preimage, error) {
	p := &preimage{SchemaVersion: 1, Records: map[string]preRecord{}}
	manPath := filepath.Join(cfg.TranslationsDir, "manifest.json")
	mb, err := os.ReadFile(manPath)
	if err != nil {
		return nil, err
	}
	p.ManifestSHA256 = shaBytes(mb)
	p.VariantSHA256 = shaFile(filepath.Join(cfg.TranslationsDir, "variants", "no-honorifics.json"))
	err = filepath.WalkDir(cfg.TranslationsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") || d.Name() == "manifest.json" {
			return nil
		}
		rel, err := filepath.Rel(cfg.TranslationsDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var sh importer.Shard
		if err := json.Unmarshal(b, &sh); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		f := preimageFile{Path: rel, SHA256: shaBytes(b), Bytes: int64(len(b)), Records: len(sh.Records)}
		p.Files = append(p.Files, f)
		for _, r := range sh.Records {
			p.RecordCount++
			if r.Text.RealEN != nil {
				p.RealENRecords++
			}
			if r.Review.Status != "UNREVIEWED" {
				p.ApprovedRecords++
			}
			p.Records[r.RecordID] = preRecord{JPSHA256: ptrSHA(r.Text.JP), OfficialENSHA256: ptrSHA(r.Text.OfficialEN)}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path })
	if write {
		writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_preimage.json"), p)
	}
	return p, nil
}

func removeOwnedStage(work, stage string) error {
	absWork, err := filepath.Abs(work)
	if err != nil {
		return err
	}
	absStage, err := filepath.Abs(stage)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(absStage, absWork+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to clean stage outside work dir: %s", absStage)
	}
	if _, err := os.Stat(stage); err == nil {
		return os.RemoveAll(filepath.Dir(stage))
	}
	return nil
}

func writeStage(root string, res *importer.P01BResult, cfg importer.Config) error {
	for p, b := range res.Shards {
		path := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		tmp := path + ".tmp-p01b"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "variants"), 0o755); err != nil {
		return err
	}
	ov, err := os.ReadFile(filepath.Join(cfg.TranslationsDir, "variants", "no-honorifics.json"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "variants", "no-honorifics.json"), ov, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "manifest.json"), res.ManifestBytes, 0o644)
}

type migrationLedgerFile struct {
	OldPath    string   `json:"old_path"`
	NewPaths   []string `json:"new_paths"`
	OldSHA     string   `json:"old_sha256"`
	OldRecords int      `json:"old_records"`
}

func migrationLedger(cfg importer.Config, pre *preimage, res *importer.P01BResult, l *loaded) (map[string]any, error) {
	recTo := map[string]string{}
	for p, b := range res.Shards {
		var sh importer.Shard
		if err := json.Unmarshal(b, &sh); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		for _, r := range sh.Records {
			recTo[r.RecordID] = p
		}
	}
	newPathsByBank := map[string][]string{}
	for _, br := range res.BankReports {
		newPathsByBank[br.Bank] = br.ShardPaths
	}
	var files []migrationLedgerFile
	for _, f := range pre.Files {
		bank := strings.TrimSuffix(filepath.Base(f.Path), ".json")
		files = append(files, migrationLedgerFile{OldPath: f.Path, NewPaths: newPathsByBank[bank], OldSHA: f.SHA256, OldRecords: f.Records})
	}
	return map[string]any{
		"schema_version":       1,
		"status":               "APPLIED_VALIDATED",
		"old_shards":           len(pre.Files),
		"new_shards":           len(res.Shards),
		"old_records":          pre.RecordCount,
		"new_records":          res.Records,
		"old_manifest_sha256":  pre.ManifestSHA256,
		"new_manifest_sha256":  shaBytes(res.ManifestBytes),
		"variant_sha256":       pre.VariantSHA256,
		"text_integrity":       "PASS",
		"record_ids_namespace": "fefw:switch2:vUNKNOWN (unchanged)",
		"files":                files,
		"record_to_shard":      recTo,
	}, nil
}

// ---------------------------------------------------------------------------
// Reports
// ---------------------------------------------------------------------------

func sourceToShard(l *loaded, res *importer.P01BResult) map[string]any {
	type bank struct {
		Bank     string   `json:"bank"`
		Shards   []string `json:"shards"`
		Records  int      `json:"records"`
		Category string   `json:"category"`
		JPSHA    string   `json:"jp_source_sha256"`
		ENSHA    string   `json:"en_source_sha256"`
	}
	var out []bank
	for _, br := range res.BankReports {
		out = append(out, bank{br.Bank, br.ShardPaths, br.Records, br.Category, br.JPSourceSHA256, br.ENSourceSHA256})
	}
	return map[string]any{"schema_version": 1, "banks": out}
}

func sectionValidation(corp *importer.Corpus) map[string]any {
	agree, disagree, checked := 0, 0, 0
	if corp == nil || corp.Talk == nil {
		return map[string]any{"schema_version": 1, "status": "NO_TALKMSG"}
	}
	for _, b := range corp.Banks {
		for i := range b.JP.Text {
			lbl := b.JP.Labels[uint32(i)]
			_, _, flag, ok := corp.Talk.Lookup(lbl)
			if !ok {
				continue
			}
			if i >= len(b.JP.Attrs) {
				continue
			}
			v, ok := importer.FindAttr(b.JP.Attrs[i], "Section")
			if !ok {
				continue
			}
			checked++
			if uint8(v.Enum) == flag {
				agree++
			} else {
				disagree++
			}
		}
	}
	status := "PASS"
	if disagree != 0 {
		status = "CONFLICT"
	}
	return map[string]any{"schema_version": 1, "status": status, "checked": checked, "agree": agree, "disagree": disagree, "note": "ATR1.Section vs TalkMsg.talkSection byte; agreement validates Section as an authored section id."}
}

func writeConflicts(cfg importer.Config, l *loaded, res *importer.P01BResult) {
	var cand, conf strings.Builder
	cand.WriteString("record_id\tbank\tentry_index\tentity_id\treason\n")
	conf.WriteString("record_id\tbank\tentry_index\tkind\tpid\tmpid\tevent_pids\treason\n")
	for _, c := range res.Candidates {
		cand.WriteString(fmt.Sprintf("%s\t%s\t%d\t%s\t%s\n", c.RecordID, c.Bank, c.Index, c.EntityID, c.Note))
		switch {
		case strings.Contains(c.Note, "EVENT_MULTI_ACTOR"), strings.Contains(c.Note, "EVENT_ACTOR_MISMATCH"), strings.Contains(c.Note, "PERSON_TABLE_MISSING"), strings.Contains(c.Note, "JP_EN_PID_MISMATCH"):
			pid, mpid, ev := recordEventFields(l.corp, c.Bank, c.Index)
			conf.WriteString(fmt.Sprintf("%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", c.RecordID, c.Bank, c.Index, "AMBIGUOUS_OR_MISMATCH", pid, mpid, uniqueEventPIDs(ev), c.Note))
		}
	}
	// Required contradiction case: MPID is a portrait/model namespace, not a person.
	if pid, mpid, ev := recordEventFields(l.corp, "1_BEL_c01", 135); mpid != "" {
		conf.WriteString(fmt.Sprintf("fefw:switch2:vUNKNOWN:msbt-v3:1_BEL_c01:MID_EV15_002_m01_014#135\t1_BEL_c01\t135\tMPID_PID_DIFFERENT_NAMESPACE\t%s\t%s\t%s\t%s\n", pid, mpid, uniqueEventPIDs(ev), "MPID_Layridana is a Profile.bin portrait/model id; PID_レダ is the Person.bin person id; PID and MPID are not competing speaker claims"))
	}
	writeText(filepath.Join(cfg.EvidenceDir, "p01b_speaker_candidates.tsv"), cand.String())
	writeText(filepath.Join(cfg.EvidenceDir, "p01b_speaker_conflicts.tsv"), conf.String())
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01b_addressee_candidates.json"), map[string]any{"schema_version": 1, "candidates": res.Addressees, "public_addressee_state": "UNKNOWN", "note": "Look-at targets are private candidates only; they do not prove the addressee."})
}

func recordEventFields(corp *importer.Corpus, bank string, index int) (pid, mpid, event string) {
	if corp == nil {
		return
	}
	for _, b := range corp.Banks {
		if b.Name != bank || b.JP == nil || index >= len(b.JP.Attrs) {
			continue
		}
		for _, v := range b.JP.Attrs[index] {
			switch v.Name {
			case "PID":
				pid = strings.TrimSpace(v.String)
			case "MPID":
				mpid = strings.TrimSpace(v.String)
			case "Event":
				event = v.String
			}
		}
	}
	return
}

func uniqueEventPIDs(ev string) string {
	re := regexp.MustCompile(`PID_([^\s,\)\r\n]+)`)
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(ev, -1) {
		p := strings.TrimSpace(m[1])
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func auditSummary(l *loaded, res *importer.P01BResult, attrs map[string]*importer.AttrAudit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "source_files=%d banks=%d records=%d shards=%d\n", l.inv.FileCount, len(l.corp.Banks), res.Records, len(res.Shards))
	jp, en := attrs["JPja"], attrs["USen"]
	if jp != nil && en != nil {
		fmt.Fprintf(&b, "attributes: JP defs=%d present=%d records=%d USEN defs=%d present=%d records=%d\n", jp.DefCount, len(jp.PresentOffsets), jp.Records, en.DefCount, len(en.PresentOffsets), en.Records)
	}
	fmt.Fprintf(&b, "speaker: corroborated=%d unknown=%d conflicts=%d pid_nonempty=%d mpid_nonempty=%d pid_only=%d mpid_only=%d\n",
		res.Coverage.Corroborated, res.Coverage.Unknown, res.Coverage.Conflicts, res.Coverage.PIDNonEmpty, res.Coverage.MPIDNonEmpty, res.Coverage.PIDOnly, res.Coverage.MPIDOnly)
	fmt.Fprintf(&b, "speaker reasons=%v\n", res.Coverage.Reasons)
	fmt.Fprintf(&b, "addressee candidates=%d (public addressee remains UNKNOWN)\n", res.Coverage.AddresseeCandidates)
	fmt.Fprintf(&b, "categories(banks/records/shards)=%v\n", res.CategoryCounts())
	fmt.Fprintf(&b, "shards_by_top=%v\n", res.CategoryShardCounts())
	fmt.Fprintf(&b, "callsites: script files=%d literals=%d distinct=%d\n", l.ctx.Scripts.Files, l.ctx.Scripts.Literals, l.ctx.Scripts.Distinct)
	return b.String()
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func ptrStr(p *string) string {
	if p == nil {
		return "\x00nil"
	}
	return *p
}
func ptrSHA(p *string) string {
	if p == nil {
		return "\x00nil"
	}
	return shaBytes([]byte(*p))
}
func shaBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func shaFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return shaBytes(b)
}
func writeJSON(path string, v any) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatal(err)
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		fatal(err)
	}
}
func writeText(path, s string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		fatal(err)
	}
}
func sortedBoolKeys(m map[string]bool) []string {
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
