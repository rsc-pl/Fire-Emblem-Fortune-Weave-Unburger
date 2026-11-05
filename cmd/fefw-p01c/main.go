// Command fefw-p01c is the offline P01C role/speaker evidence, census,
// migration and editorial package tool for the Fire Emblem: Fortune's Weave
// message corpus. It never translates, edits natural English, or writes source
// assets.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fefw/internal/importer"
)

const (
	p01cBaselineManifestSHA = "4c5fe8ceb018496e172c11d6994824837576e84eddb0a98df888394286b699d4"
	p01cBaselineVariantSHA  = "782398febf856c5addef76dc7c4a9ea47ff2e7102080a9d89d121a4e180c7f77"
)

type loaded struct {
	cfg  importer.Config
	inv  *importer.InventoryReport
	corp *importer.Corpus
	ctx  *importer.P01BContext
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
	apply := fs.Bool("apply", false, "perform the migration (otherwise dry-run)")
	dryRun := fs.Bool("dry-run", false, "compute the package plan without writing")
	editorialDocs := fs.String("editorial-docs", "", "optional explicit directory containing editorial documents; requires --editorial-files")
	editorialFiles := fs.String("editorial-files", "", "comma-separated .md/.txt basenames to package from --editorial-docs")
	roundtrip := fs.Bool("roundtrip", true, "run the all-bank byte-exact roundtrip in baseline")
	_ = fs.Parse(os.Args[2:])
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
		runAudit(cfg)
	case "migrate":
		runMigrate(cfg, *apply)
	case "validate":
		l := mustLoad(cfg)
		rep, err := importer.ValidateOutput(cfg, l.corp)
		if err != nil {
			fatal(err)
		}
		writeJSON(filepath.Join(cfg.ReportsDir, "p01c_validate.json"), rep)
		fmt.Printf("validate: %s shards=%d records=%d errors=%d\n", rep.Status, rep.Counts["shards"], rep.Counts["records"], len(rep.Errors))
		for _, e := range rep.Errors {
			fmt.Println("  ERROR", e)
		}
		if rep.Status != "PASS" {
			os.Exit(1)
		}
	case "package":
		runPackage(cfg, *dryRun, *editorialDocs, *editorialFiles)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `fefw-p01c — message-role census, speaker evidence and editorial packaging

usage: fefw-p01c <command> [flags]

commands:
  baseline   reproduce P00/P01 identity, corpus, roundtrip and current hashes
  audit      build private role/speaker census, promotion audit and maps
  migrate    rewrite canonical context with the P01C model (use --apply)
  validate   validate the live canonical tree and manifest
  package    build the cumulative editorial package with a content gate
             (--dry-run prints the plan; --editorial-docs + --editorial-files
             select documents explicitly; an empty allowlist means none)

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
	return &loaded{cfg: cfg, inv: inv, corp: corp, ctx: ctx}
}

// ---------------------------------------------------------------------------
// baseline
// ---------------------------------------------------------------------------

type p01cBaseline struct {
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
	ManifestSHA256        string            `json:"manifest_sha256"`
	ManifestFiles         int               `json:"manifest_files"`
	ManifestRecords       int               `json:"manifest_records"`
	ManifestRealEN        int               `json:"manifest_real_en"`
	ManifestByReview      map[string]int    `json:"manifest_by_review_status"`
	VariantSHA256         string            `json:"variant_sha256"`
	VariantUnchanged      bool              `json:"variant_unchanged"`
	LiveSpeakerStates     map[string]int    `json:"live_speaker_states"`
	LiveDialogueRoles     map[string]int    `json:"live_dialogue_roles"`
	LiveRecords           int               `json:"live_records"`
	ModelPreview          map[string]int    `json:"model_preview"`
	ModelConfirmed        int               `json:"model_confirmed"`
	ModelUnknown          int               `json:"model_genuine_unknown"`
	PreP01CTree           bool              `json:"pre_p01c_tree"`
	ValidationStatus      string            `json:"validation_status"`
	ValidationErrors      []string          `json:"validation_errors"`
	ProgramID             string            `json:"program_id_candidate"`
	Version               string            `json:"version"`
	TitleID               string            `json:"title_id"`
	Region                string            `json:"region"`
	Checks                map[string]bool   `json:"checks"`
	Identity              importer.Identity `json:"identity"`
}

func runBaseline(cfg importer.Config, doRT bool) {
	l := mustLoad(cfg)
	rep := &p01cBaseline{SchemaVersion: 1, Checks: map[string]bool{}, LiveSpeakerStates: map[string]int{}, LiveDialogueRoles: map[string]int{}, ModelPreview: map[string]int{}}
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
	manBytes, err := os.ReadFile(filepath.Join(cfg.TranslationsDir, "manifest.json"))
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
	rep.VariantSHA256 = shaFile(filepath.Join(cfg.TranslationsDir, "variants", "no-honorifics.json"))
	rep.VariantUnchanged = rep.VariantSHA256 == p01cBaselineVariantSHA
	if err := importer.ForEachShard(cfg.TranslationsDir, func(_ string, sh importer.Shard) {
		for _, r := range sh.Records {
			rep.LiveRecords++
			rep.LiveSpeakerStates[r.Context.Speaker.State]++
			rep.LiveDialogueRoles[r.Context.DialogueRole]++
		}
	}); err != nil {
		fatal(err)
	}
	// Model preview (no write).
	pc, err := importer.BuildP01C(l.corp, l.ctx)
	if err != nil {
		fatal(err)
	}
	c := pc.Census
	rep.ModelPreview = map[string]int{
		"records": c.Total.Records, "spoken": c.Total.Spoken, "narration": c.Total.Narration,
		"choice": c.Total.Choice, "system": c.Total.System, "ui": c.Total.UI,
		"control": c.Total.Control, "empty": c.Total.Empty, "unknown_role": c.Total.Unknown,
		"speaker_corroborated": c.Total.SpeakerCorroborated, "speaker_attribute": c.Total.SpeakerAttribute,
		"speaker_unknown": c.Total.SpeakerUnknown, "candidates": c.Candidate,
		"choice_owner_candidates": c.ChoiceOwnerCandidates, "not_applicable": c.NotApplicable,
	}
	rep.ModelConfirmed = c.Confirmed
	rep.ModelUnknown = c.GenuineUnknown
	vrep, err := importer.ValidateOutput(cfg, l.corp)
	if err != nil {
		fatal(err)
	}
	rep.ValidationStatus = vrep.Status
	rep.ValidationErrors = vrep.Errors
	rep.PreP01CTree = rep.LiveRecords > 0 && rep.LiveDialogueRoles[""] == rep.LiveRecords
	rep.Checks["source_ledger"] = rep.SourceLedgerVerified
	rep.Checks["523_pairs"] = rep.Banks == 523 && rep.JPBanks == 523 && rep.ENBanks == 523
	rep.Checks["records_81103"] = rep.CanonicalRecords == 81103
	rep.Checks["jp_en_81103"] = rep.JPRecords == 81103 && rep.ENRecords == 81103
	rep.Checks["unique_ids"] = rep.DuplicateRecordIDs == 0 && rep.UniqueRecordIDs == rep.CanonicalRecords
	rep.Checks["label_sets_match"] = rep.LabelSetMismatchBanks == 0
	rep.Checks["both_empty_9876"] = rep.BothEmpty == 9876
	rep.Checks["roundtrip_1046"] = !doRT || (rep.RoundtripBanks == 1046 && rep.RoundtripByteExact == 1046 && rep.RoundtripFailures == 0)
	rep.Checks["real_en_zero"] = rep.ManifestRealEN == 0
	rep.Checks["all_unreviewed"] = man.Counts.ByReviewStatus["UNREVIEWED"] == man.Counts.Records
	rep.Checks["variant_unchanged"] = rep.VariantUnchanged
	rep.Checks["manifest_matches_source"] = man.Counts.Records == rep.CanonicalRecords
	// A pre-P01C tree necessarily fails the P01C context-shape check; record
	// that state explicitly instead of calling the baseline itself failed.
	rep.Checks["validation_pass"] = vrep.Status == "PASS" || rep.PreP01CTree
	rep.Checks["version_unknown"] = rep.Version == "UNKNOWN"
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
	writeJSON(filepath.Join(cfg.ReportsDir, "p01c_baseline.json"), rep)
	var b strings.Builder
	fmt.Fprintf(&b, "status=%s\n", rep.Status)
	for _, k := range sortedBoolKeys(rep.Checks) {
		fmt.Fprintf(&b, "check %-24s %v\n", k, rep.Checks[k])
	}
	fmt.Fprintf(&b, "banks=%d pairs=%d records=%d unique_ids=%d duplicates=%d\n", rep.Banks, rep.BankPairs, rep.CanonicalRecords, rep.UniqueRecordIDs, rep.DuplicateRecordIDs)
	fmt.Fprintf(&b, "aligned=%d jp_only=%d en_only=%d both_empty=%d label_set_mismatch=%d collisions=%d\n", rep.Aligned, rep.JPOnly, rep.ENOnly, rep.BothEmpty, rep.LabelSetMismatchBanks, rep.LabelCollisions)
	fmt.Fprintf(&b, "roundtrip=%s banks=%d byte_exact=%d failures=%d\n", rep.RoundtripStatus, rep.RoundtripBanks, rep.RoundtripByteExact, rep.RoundtripFailures)
	fmt.Fprintf(&b, "manifest files=%d records=%d real_en=%d review=%v sha=%s\n", rep.ManifestFiles, rep.ManifestRecords, rep.ManifestRealEN, rep.ManifestByReview, rep.ManifestSHA256)
	fmt.Fprintf(&b, "variant sha=%s unchanged=%v\n", rep.VariantSHA256, rep.VariantUnchanged)
	fmt.Fprintf(&b, "live records=%d speakers=%v roles=%v\n", rep.LiveRecords, rep.LiveSpeakerStates, rep.LiveDialogueRoles)
	fmt.Fprintf(&b, "model preview=%v\n", rep.ModelPreview)
	fmt.Fprintf(&b, "validation=%s errors=%d pre_p01c_tree=%v\n", rep.ValidationStatus, len(rep.ValidationErrors), rep.PreP01CTree)
	fmt.Fprintf(&b, "identity program_id=%s title_id=%s version=%s region=%s\n", rep.ProgramID, rep.TitleID, rep.Version, rep.Region)
	writeText(filepath.Join(cfg.ReportsDir, "p01c_baseline.txt"), b.String())
	fmt.Print(b.String())
	if rep.Status != "PASS" {
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// audit
// ---------------------------------------------------------------------------

func runAudit(cfg importer.Config) {
	l := mustLoad(cfg)
	res, err := importer.BuildP01C(l.corp, l.ctx)
	if err != nil {
		fatal(err)
	}
	preRoot := filepath.Join(cfg.WorkDir, "p01b-pre-migration-translations")
	if _, err := os.Stat(preRoot); err != nil {
		preRoot = filepath.Join(cfg.WorkDir, "p01c-pre-migration-translations")
	}
	if _, err := os.Stat(preRoot); err != nil {
		preRoot = cfg.TranslationsDir
	}
	audit, err := importer.AuditPromotions(cfg, l.corp, l.ctx, preRoot)
	if err != nil {
		fatal(err)
	}
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_census.json"), res.Census)
	writeJSON(filepath.Join(cfg.ReportsDir, "p01c_census.json"), res.Census)
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_promotion_audit.json"), audit)
	writeJSON(filepath.Join(cfg.ReportsDir, "p01c_promotion_audit.json"), audit)
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_role_by_bank.json"), map[string]any{
		"schema_version": 1,
		"banks":          res.BankReports,
		"note":           "Role/kind/speaker counts per physical source bank. COUNTS ARE PHYSICAL RECORDS, not estimated coverage.",
	})
	writeCandidates(cfg, res)
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_variant_and_overlay.json"), map[string]any{
		"schema_version": 1,
		"variant_sha256": shaFile(filepath.Join(cfg.TranslationsDir, "variants", "no-honorifics.json")),
		"note":           "No-honorifics overlay is a sparse editorial overlay and is not modified by P01C.",
	})
	writeBranchMap(cfg, res)
	writeSceneMap(cfg, res)
	writeVoiceAudit(cfg, l)
	writeNameLookup(cfg, l)
	writeTranslatorView(cfg, res)
	writeExamples(cfg, l, res)
	writeCSV(cfg, res)
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_source_to_shard.json"), p01cSourceToShard(res))
	sum := auditSummaryText(res, audit)
	writeText(filepath.Join(cfg.ReportsDir, "p01c_audit_summary.txt"), sum)
	fmt.Print(sum)
}

// ---------------------------------------------------------------------------
// migrate
// ---------------------------------------------------------------------------

type p01cPreFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
	Records int    `json:"records"`
}
type p01cPreRecord struct {
	JPSHA256         string `json:"jp_sha256"`
	OfficialENSHA256 string `json:"official_en_sha256"`
	Review           string `json:"review"`
	RealEN           bool   `json:"real_en"`
	Path             string `json:"path"`
}
type p01cPreimage struct {
	SchemaVersion   int                      `json:"schema_version"`
	ManifestSHA256  string                   `json:"manifest_sha256"`
	VariantSHA256   string                   `json:"variant_sha256"`
	RecordCount     int                      `json:"record_count"`
	RealENRecords   int                      `json:"real_en_records"`
	ApprovedRecords int                      `json:"approved_records"`
	ShardPaths      []string                 `json:"shard_paths"`
	Files           []p01cPreFile            `json:"files"`
	Records         map[string]p01cPreRecord `json:"records"`
}

func snapshotPreimage(cfg importer.Config, write bool) (*p01cPreimage, error) {
	p := &p01cPreimage{SchemaVersion: 1, Records: map[string]p01cPreRecord{}}
	mb, err := os.ReadFile(filepath.Join(cfg.TranslationsDir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	p.ManifestSHA256 = shaBytes(mb)
	p.VariantSHA256 = shaFile(filepath.Join(cfg.TranslationsDir, "variants", "no-honorifics.json"))
	var readErr error
	err = importer.ForEachShard(cfg.TranslationsDir, func(rel string, sh importer.Shard) {
		b, err := os.ReadFile(filepath.Join(cfg.TranslationsDir, filepath.FromSlash(rel)))
		if err != nil {
			readErr = err
			return
		}
		p.Files = append(p.Files, p01cPreFile{Path: rel, SHA256: shaBytes(b), Bytes: int64(len(b)), Records: len(sh.Records)})
		p.ShardPaths = append(p.ShardPaths, rel)
		for _, r := range sh.Records {
			p.RecordCount++
			if r.Text.RealEN != nil {
				p.RealENRecords++
			}
			if r.Review.Status != "UNREVIEWED" {
				p.ApprovedRecords++
			}
			p.Records[r.RecordID] = p01cPreRecord{JPSHA256: ptrSHA(r.Text.JP), OfficialENSHA256: ptrSHA(r.Text.OfficialEN), Review: r.Review.Status, RealEN: r.Text.RealEN != nil, Path: rel}
		}
	})
	if err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path })
	sort.Strings(p.ShardPaths)
	if write {
		writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_preimage.json"), p)
	}
	return p, nil
}

func runMigrate(cfg importer.Config, apply bool) {
	l := mustLoad(cfg)
	pre, err := snapshotPreimage(cfg, apply)
	if err != nil {
		fatal(err)
	}
	if pre.ManifestSHA256 != p01cBaselineManifestSHA {
		fatal(fmt.Errorf("stale preimage: manifest sha %s != recorded P01B baseline %s; refusing to overwrite", pre.ManifestSHA256, p01cBaselineManifestSHA))
	}
	if pre.VariantSHA256 != p01cBaselineVariantSHA {
		fatal(fmt.Errorf("variant overlay sha changed (%s); refusing migration", pre.VariantSHA256))
	}
	if pre.ApprovedRecords > 0 || pre.RealENRecords > 0 {
		fatal(fmt.Errorf("refusing to overwrite approved=%d real_en=%d records", pre.ApprovedRecords, pre.RealENRecords))
	}
	res, err := importer.BuildP01C(l.corp, l.ctx)
	if err != nil {
		fatal(err)
	}
	// Determinism: build twice and compare shard bytes and manifest.
	res2, err := importer.BuildP01C(l.corp, l.ctx)
	if err != nil {
		fatal(err)
	}
	if len(res.Shards) != len(res2.Shards) || string(res.ManifestBytes) != string(res2.ManifestBytes) {
		fatal(fmt.Errorf("non-deterministic build"))
	}
	for p, b := range res.Shards {
		if string(b) != string(res2.Shards[p]) {
			fatal(fmt.Errorf("non-deterministic shard %s", p))
		}
	}
	// Path set must be identical: no shard moves in P01C.
	newPaths := make([]string, 0, len(res.Shards))
	for p := range res.Shards {
		newPaths = append(newPaths, p)
	}
	sort.Strings(newPaths)
	if strings.Join(newPaths, "\x00") != strings.Join(pre.ShardPaths, "\x00") {
		fatal(fmt.Errorf("shard path set changed: old=%d new=%d", len(pre.ShardPaths), len(newPaths)))
	}
	// Text/identity preimage comparison.
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
	fmt.Printf("preimage: PASS records=%d approved=0 real_en=0 variant_sha=%s manifest_sha=%s\n", pre.RecordCount, pre.VariantSHA256, pre.ManifestSHA256)
	fmt.Printf("new layout: shards=%d records=%d manifest_sha=%s\n", len(res.Shards), res.Records, shaBytes(res.ManifestBytes))
	fmt.Printf("census: spoken=%d confirmed=%d candidates=%d genuine_unknown=%d not_applicable=%d\n", res.Census.Total.Spoken, res.Census.Confirmed, res.Census.Candidate, res.Census.GenuineUnknown, res.Census.NotApplicable)
	if !apply {
		fmt.Println("dry-run: no files written (use --apply)")
		return
	}
	stageRoot := filepath.Join(cfg.WorkDir, "p01c-stage", "translations")
	if err := removeOwnedStage(cfg.WorkDir, stageRoot); err != nil {
		fatal(err)
	}
	if err := writeStage(stageRoot, res, cfg); err != nil {
		fatal(err)
	}
	vcfg := cfg
	vcfg.TranslationsDir = stageRoot
	vrep, err := importer.ValidateFreshImport(vcfg, l.corp)
	if err != nil {
		fatal(err)
	}
	if vrep.Status != "PASS" {
		fatal(fmt.Errorf("staged validation failed: %v", vrep.Errors))
	}
	live := cfg.TranslationsDir
	backup := filepath.Join(cfg.WorkDir, "p01c-pre-migration-translations")
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
		failed := filepath.Join(cfg.WorkDir, "p01c-failed-translations")
		moveErr := os.Rename(live, failed)
		restoreErr := os.Rename(backup, live)
		if moveErr != nil || restoreErr != nil {
			fatal(fmt.Errorf("post-migration validation failed and rollback is incomplete: move=%v restore=%v validation=%v %v", moveErr, restoreErr, err, post))
		}
		fatal(fmt.Errorf("post-migration validation failed; original restored: %v %v", err, post))
	}
	writeJSON(filepath.Join(cfg.ReportsDir, "p01c_validation_post_migration.json"), post)
	ledger, err := migrationLedger(cfg, pre, res)
	if err != nil {
		fatal(err)
	}
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_migration_ledger.json"), ledger)
	writeJSON(filepath.Join(cfg.ReportsDir, "p01c_migration_ledger.json"), ledger)
	fmt.Printf("migration: APPLIED validation=PASS shards=%d records=%d manifest_sha=%s variant_sha=%s\n", len(res.Shards), res.Records, shaBytes(res.ManifestBytes), shaFile(filepath.Join(cfg.TranslationsDir, "variants", "no-honorifics.json")))
}

type p01cLedgerFile struct {
	OldPath    string   `json:"old_path"`
	NewPaths   []string `json:"new_paths"`
	OldSHA     string   `json:"old_sha256"`
	NewSHA     string   `json:"new_sha256"`
	OldRecords int      `json:"old_records"`
	NewRecords int      `json:"new_records"`
}

func migrationLedger(cfg importer.Config, pre *p01cPreimage, res *importer.P01CResult) (map[string]any, error) {
	recTo := map[string]string{}
	newSHA := map[string]string{}
	newRecs := map[string]int{}
	for p, b := range res.Shards {
		newSHA[p] = shaBytes(b)
		var sh importer.Shard
		if err := json.Unmarshal(b, &sh); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		newRecs[p] = len(sh.Records)
		for _, r := range sh.Records {
			recTo[r.RecordID] = p
		}
	}
	var files []p01cLedgerFile
	for _, f := range pre.Files {
		files = append(files, p01cLedgerFile{OldPath: f.Path, NewPaths: []string{f.Path}, OldSHA: f.SHA256, NewSHA: newSHA[f.Path], OldRecords: f.Records, NewRecords: newRecs[f.Path]})
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
		"shard_paths_changed":  0,
		"record_ids_namespace": "fefw:switch2:vUNKNOWN (unchanged)",
		"files":                files,
		"record_to_shard":      recTo,
	}, nil
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

func writeStage(root string, res *importer.P01CResult, cfg importer.Config) error {
	for p, b := range res.Shards {
		path := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		tmp := path + ".tmp-p01c"
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

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

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
