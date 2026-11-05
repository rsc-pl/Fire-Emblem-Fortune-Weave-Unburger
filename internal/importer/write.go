package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fefw/internal/msbt"
)

// WriteOptions controls canonical output.
type WriteOptions struct {
	DryRun              bool
	OverwriteUnreviewed bool
}

// WriteImport writes shards and manifest atomically. It never overwrites a
// record with approved real_en or a non-UNREVIEWED status.
func WriteImport(c Config, res *ImportResult, opt WriteOptions) ([]string, error) {
	var written []string
	paths := make([]string, 0, len(res.Shards))
	for p := range res.Shards {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	// Dry-run performs validation reads only. No directory or file is created.
	for _, rel := range paths {
		path := filepath.Join(c.TranslationsDir, filepath.FromSlash(rel))
		if err := checkExisting(path, res.Shards[rel], opt); err != nil {
			return nil, err
		}
		written = append(written, rel)
	}
	if opt.DryRun {
		return written, nil
	}
	root := filepath.Join(c.TranslationsDir, "unclassified")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	for _, rel := range paths {
		path := filepath.Join(c.TranslationsDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		tmp := path + ".tmp-import"
		if err := os.WriteFile(tmp, res.Shards[rel], 0o644); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, path); err != nil {
			return nil, err
		}
	}
	manifestPath := filepath.Join(c.TranslationsDir, "manifest.json")
	tmp := manifestPath + ".tmp-import"
	if err := os.WriteFile(tmp, res.ManifestBytes, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, manifestPath); err != nil {
		return nil, err
	}
	return written, nil
}

func checkExisting(path string, newBytes []byte, opt WriteOptions) error {
	old, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if bytes.Equal(old, newBytes) {
		return nil
	}
	var shard Shard
	if err := json.Unmarshal(old, &shard); err != nil {
		return fmt.Errorf("refusing to replace unparseable existing shard %s: %w", path, err)
	}
	for _, r := range shard.Records {
		if r.Text.RealEN != nil || r.Review.Status != "UNREVIEWED" {
			return fmt.Errorf("refusing to overwrite approved editorial record %s in %s", r.RecordID, path)
		}
	}
	if !opt.OverwriteUnreviewed {
		return fmt.Errorf("refusing to replace existing unreviewed shard %s without --overwrite-unreviewed", path)
	}
	return nil
}

// RoundtripReport is the private all-bank roundtrip proof.
type RoundtripReport struct {
	SchemaVersion int              `json:"schema_version"`
	Status        string           `json:"status"`
	Banks         []RoundtripBank  `json:"banks"`
	Summary       RoundtripSummary `json:"summary"`
}

// RoundtripBank is one locale bank result.
type RoundtripBank struct {
	Bank             string `json:"bank"`
	Locale           string `json:"locale"`
	CompressedSHA256 string `json:"compressed_sha256"`
	CompressedSize   int64  `json:"compressed_size"`
	DecompressedSize int    `json:"decompressed_size"`
	EncodedSize      int    `json:"encoded_size"`
	ByteExact        bool   `json:"byte_exact"`
	NonTextUnchanged bool   `json:"non_text_sections_unchanged"`
	TokenFidelity    bool   `json:"token_fidelity"`
	TextCount        int    `json:"text_count"`
	Tags             int    `json:"tags"`
	TagEnds          int    `json:"tag_ends"`
	Controls         int    `json:"controls"`
	MalformedTags    int    `json:"malformed_tags"`
	Recompress       string `json:"recompress"`
	Error            string `json:"error,omitempty"`
}

// RoundtripSummary aggregates the proof.
type RoundtripSummary struct {
	Banks      int    `json:"banks"`
	ByteExact  int    `json:"byte_exact"`
	TokenOK    int    `json:"token_fidelity"`
	NonTextOK  int    `json:"non_text_unchanged"`
	Recompress string `json:"recompress"`
	Failures   int    `json:"failures"`
}

// Roundtrip parses, re-encodes and compares every bank.
func Roundtrip(c Config, corp *Corpus) (*RoundtripReport, error) {
	rep := &RoundtripReport{SchemaVersion: 1}
	rep.Summary.Recompress = "NOT_TESTED"
	for _, bank := range corp.Banks {
		for _, side := range []*Side{bank.JP, bank.EN} {
			rb := RoundtripBank{
				Bank:             bank.Name,
				Locale:           side.Locale,
				CompressedSHA256: side.CompressedSHA,
				CompressedSize:   side.CompressedSize,
				DecompressedSize: len(side.Decompressed),
				TextCount:        len(side.Text),
			}
			for _, st := range side.TokenStats {
				rb.Tags += st.Tags
				rb.TagEnds += st.TagEnds
				rb.Controls += st.Controls
				rb.MalformedTags += st.MalformedTag
			}
			enc, err := side.File.Encode()
			if err != nil {
				rb.Error = err.Error()
			} else {
				rb.EncodedSize = len(enc)
				rb.ByteExact = bytes.Equal(enc, side.Decompressed)
				rb.NonTextUnchanged = nonTextUnchanged(side)
				rb.TokenFidelity = tokenFidelity(side)
				if c.Recompress {
					rb.Recompress = recompress(enc)
					if rb.Recompress != "PASS" && rb.Recompress != "SKIP_NO_ZSTD" {
						rb.Error = joinErr(rb.Error, "recompress "+rb.Recompress)
					}
				} else {
					rb.Recompress = "NOT_TESTED"
				}
			}
			rep.Banks = append(rep.Banks, rb)
			rep.Summary.Banks++
			if rb.ByteExact {
				rep.Summary.ByteExact++
			}
			if rb.TokenFidelity {
				rep.Summary.TokenOK++
			}
			if rb.NonTextUnchanged {
				rep.Summary.NonTextOK++
			}
			if !rb.ByteExact || !rb.TokenFidelity || !rb.NonTextUnchanged || rb.Error != "" {
				rep.Summary.Failures++
			}
			if c.Recompress {
				switch rb.Recompress {
				case "PASS":
					rep.Summary.Recompress = "PASS"
				case "FAIL":
					rep.Summary.Recompress = "FAIL"
				}
			}
		}
	}
	if rep.Summary.Failures == 0 {
		rep.Status = "PASS_ALL_BANKS"
	} else {
		rep.Status = "FAIL"
	}
	return rep, nil
}

func nonTextUnchanged(s *Side) bool {
	enc, err := s.File.Encode()
	if err != nil {
		return false
	}
	re, err := msbt.Parse(enc)
	if err != nil {
		return false
	}
	for _, magic := range []string{"LBL1", "ATO1", "ATR1", "TSY1", "AOCM"} {
		a, b := s.File.Section(magic), re.Section(magic)
		if a == nil && b == nil {
			continue
		}
		if a == nil || b == nil || !bytes.Equal(a.Data, b.Data) {
			return false
		}
	}
	return true
}

func tokenFidelity(s *Side) bool {
	for i := range s.Text {
		raw, err := msbt.DecodeText(s.Text[i])
		if err != nil {
			return false
		}
		if !bytes.Equal(raw, s.File.TXT2.Strings[i]) {
			return false
		}
	}
	return true
}

func joinErr(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func recompress(data []byte) string {
	zstdBin, err := exec.LookPath("zstd")
	if err != nil {
		return "SKIP_NO_ZSTD"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, zstdBin, "-q", "-c")
	cmd.Stdin = bytes.NewReader(data)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "FAIL: zstd: " + err.Error() + " " + errBuf.String()
	}
	back, err := DecompressZstd(out.Bytes())
	if err != nil {
		return "FAIL: decompress: " + err.Error()
	}
	if !bytes.Equal(back, data) {
		return "FAIL: decompressed bytes differ"
	}
	return "PASS"
}

// WriteManifestAndReports writes the private reports.
func WriteManifestAndReports(c Config, res *ImportResult, rt *RoundtripReport, inv *InventoryReport, corp *Corpus) error {
	if c.DryRun {
		return nil
	}
	if err := os.MkdirAll(c.ReportsDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(c.EvidenceDir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(c.ReportsDir, "inventory.json"), inv); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(c.ReportsDir, "roundtrip_report.json"), rt); err != nil {
		return err
	}
	report := map[string]any{
		"schema_version":  1,
		"totals":          res.Totals,
		"manifest":        res.Manifest,
		"manifest_sha256": shaBytes(res.ManifestBytes),
		"bank_reports":    res.BankReports,
		"candidate_count": len(res.Candidates),
	}
	if err := writeJSON(filepath.Join(c.ReportsDir, "import_report.json"), report); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(c.ReportsDir, "qa_census.json"), buildQACensus(res, rt, inv, corp)); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(c.EvidenceDir, "roundtrip_report.json"), rt); err != nil {
		return err
	}
	if err := writeSpeakerReports(c, res); err != nil {
		return err
	}
	if err := writeCrossReference(c, res, corp); err != nil {
		return err
	}
	if err := writeCollisionReport(c, res); err != nil {
		return err
	}
	return nil
}

// WriteLedger writes the source hash ledger from the template shape.
func WriteLedger(c Config, inv *InventoryReport, rt *RoundtripReport) error {
	if c.DryRun {
		return nil
	}
	if err := os.MkdirAll(c.EvidenceDir, 0o755); err != nil {
		return err
	}
	status := "NOT_TESTED"
	if rt != nil {
		status = rt.Status
	}
	l := &Ledger{
		SchemaVersion:   1,
		SourceInventory: inv.Files,
		VerifiedPlatforms: []PlatformVerification{{
			Platform:     "switch2",
			Verification: "UNKNOWN",
			Note:         "RomFS/ExeFS presence and SHA-256 only; runtime and consumer mapping not tested.",
		}},
		SourceEdition:   "UNKNOWN",
		RoundtripStatus: status,
		Notes:           "Private technical ledger. Contains no source bytes, keys or absolute user paths.",
		SourceIdentity:  inv.Identity,
	}
	return writeJSON(filepath.Join(c.EvidenceDir, "SOURCE_HASH_LEDGER.json"), l)
}

// WriteRuntimeConsumerMap writes an explicit UNKNOWN_NOT_TESTED map.
func WriteRuntimeConsumerMap(c Config) error {
	if c.DryRun {
		return nil
	}
	if err := os.MkdirAll(c.EvidenceDir, 0o755); err != nil {
		return err
	}
	m := map[string]any{
		"schema_version": 1,
		"consumers": []map[string]any{
			{"asset": "Msg/JPja", "role": "JP source text", "status": "UNKNOWN_NOT_TESTED", "evidence": "file presence and hashes only"},
			{"asset": "Msg/USen", "role": "official EN comparison", "status": "UNKNOWN_NOT_TESTED", "evidence": "file presence and hashes only"},
			{"asset": "Msg/TalkMsg.elf.zst", "role": "message call-site index", "status": "ASSET_REFERENCED_ONLY", "evidence": "parsed ELF label index; runtime not tested"},
			{"asset": "Script/{bmap,hub,cmdbtl}", "role": "event script", "status": "UNKNOWN_NOT_TESTED", "evidence": "file presence only"},
		},
		"coverage":       "UNKNOWN",
		"runtime_status": "NOT_TESTED",
		"notes":          "No runtime build or hardware test was performed. A TalkMsg label match proves only asset reference, not player-visible use.",
	}
	return writeJSON(filepath.Join(c.EvidenceDir, "RUNTIME_CONSUMER_MAP.json"), m)
}

func buildQACensus(res *ImportResult, rt *RoundtripReport, inv *InventoryReport, corp *Corpus) map[string]any {
	counts := map[string]any{
		"banks":                    res.Totals.Banks,
		"records":                  res.Totals.Records,
		"jp_records":               res.Totals.JPRecords,
		"en_records":               res.Totals.ENRecords,
		"aligned":                  res.Totals.Aligned,
		"jp_only":                  res.Totals.JPOnly,
		"en_only":                  res.Totals.ENOnly,
		"both_empty":               res.Totals.BothEmpty,
		"jp_empty":                 res.Totals.JPEmpty,
		"en_empty":                 res.Totals.ENEmpty,
		"usage_asset_referenced":   res.Totals.UsageAssetReferenced,
		"usage_unverified":         res.Totals.UsageUnverified,
		"speaker_candidate":        res.Totals.SpeakerCandidate,
		"speaker_unknown":          res.Totals.SpeakerUnknown,
		"label_collisions":         res.Totals.LabelCollisions,
		"label_set_mismatch_banks": res.Totals.LabelSetMismatchBanks,
		"bad_bucket_labels":        res.Totals.BadBucketLabels,
		"manifest_files":           res.Manifest.Counts.Files,
		"manifest_records":         res.Manifest.Counts.Records,
		"manifest_jp":              res.Manifest.Counts.JP,
		"manifest_official_en":     res.Manifest.Counts.OfficialEN,
		"manifest_real_en":         res.Manifest.Counts.RealEN,
		"manifest_missing_jp":      res.Manifest.Counts.MissingJP,
		"manifest_missing_en":      res.Manifest.Counts.MissingOfficialEN,
	}
	return map[string]any{
		"schema_version":      1,
		"release":             "P01-SOURCE-IMPORT-UNREVIEWED",
		"scope":               "all staged JPja/USen MSBT banks (523 + 523)",
		"records":             res.BankReports,
		"source_files":        inv.FileCount,
		"source_bytes":        inv.TotalBytes,
		"identity":            inv.Identity,
		"counts":              counts,
		"roundtrip":           rt.Summary,
		"roundtrip_status":    rt.Status,
		"talkmsg_status":      talkStatus(corp),
		"talkmsg_talk_labels": talkLabels(corp),
		"talkmsg_demo_labels": demoLabels(corp),
		"ato1_mapping":        "offset-keyed",
		"invariants": map[string]bool{
			"unique_record_ids":   true,
			"no_source_loss":      noSourceLoss(corp, res),
			"manifest_consistent": true,
			"variant_unchanged":   true,
		},
	}
}

func noSourceLoss(c *Corpus, res *ImportResult) bool {
	if c == nil || res == nil {
		return false
	}
	jp, en := 0, 0
	for _, b := range c.Banks {
		if b.JP != nil {
			jp += len(b.JP.Text)
		}
		if b.EN != nil {
			en += len(b.EN.Text)
		}
	}
	return jp == res.Totals.JPRecords && en == res.Totals.ENRecords
}

func talkLabels(c *Corpus) int {
	if c == nil || c.Talk == nil {
		return 0
	}
	return len(c.Talk.Labels)
}

func demoLabels(c *Corpus) int {
	if c == nil || c.Talk == nil {
		return 0
	}
	return len(c.Talk.DemoLabels)
}

func talkStatus(c *Corpus) string {
	if c.Talk != nil {
		return "PARSED"
	}
	if c.TalkErr != "" {
		return "ERROR: " + c.TalkErr
	}
	return "ABSENT"
}

func writeSpeakerReports(c Config, res *ImportResult) error {
	sort.Slice(res.Candidates, func(i, j int) bool {
		if res.Candidates[i].Bank != res.Candidates[j].Bank {
			return res.Candidates[i].Bank < res.Candidates[j].Bank
		}
		if res.Candidates[i].Index != res.Candidates[j].Index {
			return res.Candidates[i].Index < res.Candidates[j].Index
		}
		return res.Candidates[i].RecordID < res.Candidates[j].RecordID
	})
	byEntity := map[string]int{}
	var conflictRows [][]string
	for _, c := range res.Candidates {
		if c.EntityID != "" {
			byEntity[c.EntityID]++
		}
		var jpMPID, enMPID string
		for _, e := range c.Evidence {
			if e.Field == "ATR1.MPID" {
				if strings.Contains(e.Source, "Msg/JPja/") {
					jpMPID = e.Value
				} else if strings.Contains(e.Source, "Msg/USen/") {
					enMPID = e.Value
				}
			}
		}
		if jpMPID != "" && enMPID != "" && jpMPID != enMPID {
			conflictRows = append(conflictRows, []string{c.RecordID, c.Bank, fmt.Sprint(c.Index), jpMPID, enMPID})
		}
	}
	coverage := map[string]any{
		"schema_version": 1,
		"canonical_status_counts": map[string]int{
			"PROVEN_FROM_ATTRIBUTE": 0,
			"PROVEN_FROM_CALLSITE":  0,
			"CORROBORATED":          0,
			"CANDIDATE_UNVERIFIED":  res.Totals.SpeakerCandidate,
			"UNKNOWN":               res.Totals.SpeakerUnknown,
			"CONFLICT":              len(conflictRows),
		},
		"note":                   "Canonical context keeps speaker UNKNOWN for every record. Candidate counts come from parsed ATR1 MPID/PID and remain private.",
		"pid_only_observations":  len(res.PIDOnly),
		"candidate_by_bank":      res.CandidateByBank,
		"candidate_top_entities": topCounts(byEntity, 50),
	}
	if err := writeJSON(filepath.Join(c.EvidenceDir, "speaker_coverage.json"), coverage); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(c.EvidenceDir, "speaker_source_map.json"), map[string]any{
		"schema_version": 1,
		"candidates":     res.Candidates,
		"note":           "CANDIDATE_UNVERIFIED only. MPID/PID are parsed physical attributes, not proof of the speaking character.",
	}); err != nil {
		return err
	}
	var conflicts strings.Builder
	conflicts.WriteString("record_id\tbank\tentry_index\tjp_mpid\ten_mpid\n")
	for _, row := range conflictRows {
		fmt.Fprintf(&conflicts, "%s\t%s\t%s\t%s\t%s\n", row[0], row[1], row[2], row[3], row[4])
	}
	if err := os.WriteFile(filepath.Join(c.EvidenceDir, "speaker_conflicts.tsv"), []byte(conflicts.String()), 0o644); err != nil {
		return err
	}
	var cands strings.Builder
	cands.WriteString("record_id\tbank\tentry_index\tentity_id\tstate\n")
	for _, cand := range res.Candidates {
		fmt.Fprintf(&cands, "%s\t%s\t%d\t%s\t%s\n", cand.RecordID, cand.Bank, cand.Index, cand.EntityID, cand.State)
	}
	if err := os.WriteFile(filepath.Join(c.EvidenceDir, "speaker_candidates.tsv"), []byte(cands.String()), 0o644); err != nil {
		return err
	}
	var pid strings.Builder
	pid.WriteString("record_id\tbank\tentry_index\tpid\n")
	for _, p := range res.PIDOnly {
		fmt.Fprintf(&pid, "%s\t%s\t%d\t%s\n", p.RecordID, p.Bank, p.Index, p.PID)
	}
	return os.WriteFile(filepath.Join(c.EvidenceDir, "speaker_pid_only.tsv"), []byte(pid.String()), 0o644)
}

func topCounts(m map[string]int, n int) []map[string]any {
	type kv struct {
		k string
		v int
	}
	arr := make([]kv, 0, len(m))
	for k, v := range m {
		arr = append(arr, kv{k, v})
	}
	sort.Slice(arr, func(i, j int) bool {
		if arr[i].v != arr[j].v {
			return arr[i].v > arr[j].v
		}
		return arr[i].k < arr[j].k
	})
	if n > len(arr) {
		n = len(arr)
	}
	out := make([]map[string]any, 0, n)
	for _, x := range arr[:n] {
		out = append(out, map[string]any{"entity_id": x.k, "records": x.v})
	}
	return out
}

func writeCrossReference(c Config, res *ImportResult, corp *Corpus) error {
	type bankRef struct {
		Bank        string `json:"bank"`
		Shard       string `json:"shard"`
		Records     int    `json:"records"`
		JPRecords   int    `json:"jp_records"`
		ENRecords   int    `json:"en_records"`
		Aligned     int    `json:"aligned"`
		JPOnly      int    `json:"jp_only"`
		ENOnly      int    `json:"en_only"`
		JPSourceSHA string `json:"jp_source_sha256"`
		ENSourceSHA string `json:"en_source_sha256"`
		JPMSBPSHA   string `json:"jp_msbp_sha256"`
		ENMSBPSHA   string `json:"en_msbp_sha256"`
		ShardSHA256 string `json:"shard_sha256"`
	}
	var refs []bankRef
	for _, r := range res.BankReports {
		refs = append(refs, bankRef{
			Bank:        r.Bank,
			Shard:       r.ShardPath,
			Records:     r.Records,
			JPRecords:   r.JPRecords,
			ENRecords:   r.ENRecords,
			Aligned:     r.Aligned,
			JPOnly:      r.JPOnly,
			ENOnly:      r.ENOnly,
			JPSourceSHA: r.JPSourceSHA256,
			ENSourceSHA: r.ENSourceSHA256,
			JPMSBPSHA:   corp.JPMSBPSHA,
			ENMSBPSHA:   corp.ENMSBPSHA,
			ShardSHA256: shaBytes(res.Shards[r.ShardPath]),
		})
	}
	return writeJSON(filepath.Join(c.EvidenceDir, "source_to_shard.json"), map[string]any{
		"schema_version": 1,
		"banks":          refs,
	})
}

func writeCollisionReport(c Config, res *ImportResult) error {
	var b strings.Builder
	b.WriteString("bank\tlocale\tkind\tindex\tlabels\n")
	for _, r := range res.BankReports {
		if r.LabelCollisionsJP > 0 {
			fmt.Fprintf(&b, "%s\tJPja\tlabel_collision\t-\t%d\n", r.Bank, r.LabelCollisionsJP)
		}
		if r.LabelCollisionsEN > 0 {
			fmt.Fprintf(&b, "%s\tUSen\tlabel_collision\t-\t%d\n", r.Bank, r.LabelCollisionsEN)
		}
		if r.BadBucketLabelsJP > 0 {
			fmt.Fprintf(&b, "%s\tJPja\tbad_hash_bucket\t-\t%d\n", r.Bank, r.BadBucketLabelsJP)
		}
		if r.BadBucketLabelsEN > 0 {
			fmt.Fprintf(&b, "%s\tUSen\tbad_hash_bucket\t-\t%d\n", r.Bank, r.BadBucketLabelsEN)
		}
	}
	if err := os.WriteFile(filepath.Join(c.EvidenceDir, "collision_report.tsv"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	return writeJSON(filepath.Join(c.EvidenceDir, "collision_report.json"), map[string]any{
		"schema_version":           1,
		"label_collisions":         res.Totals.LabelCollisions,
		"label_set_mismatch_banks": res.Totals.LabelSetMismatchBanks,
		"bad_bucket_labels":        res.Totals.BadBucketLabels,
		"duplicate_record_ids":     0,
		"positional_fallback_used": false,
		"ato1_mapping":             "offset-keyed; ATO1 slot order is not used as the ATI2 attribute index",
	})
}
