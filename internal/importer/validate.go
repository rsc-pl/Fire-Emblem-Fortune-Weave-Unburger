package importer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fefw/internal/msbt"
)

// ValidationReport is the private schema/manifest/corpus validation result.
type ValidationReport struct {
	SchemaVersion int             `json:"schema_version"`
	Status        string          `json:"status"`
	Errors        []string        `json:"errors"`
	Checks        map[string]bool `json:"checks"`
	Counts        map[string]int  `json:"counts"`
}

var allowedRecordKeys = map[string]bool{
	"record_id": true, "source": true, "context": true, "function": true, "text": true, "review": true,
}

var allowedSpeakerStates = map[string]bool{
	"PROVEN_FROM_ATTRIBUTE": true, "PROVEN_FROM_CALLSITE": true, "CORROBORATED": true,
	"CANDIDATE_UNVERIFIED": true, "UNKNOWN": true, "CONFLICT": true,
}

var allowedUsageStates = map[string]bool{
	"PLAYER_VISIBLE_VERIFIED": true, "ASSET_REFERENCED": true, "UNVERIFIED_CANDIDATE": true,
	"DEBUG/TECHNICAL_PROVEN": true, "UNALIGNED": true, "JP_ONLY": true, "EN_ONLY": true,
}

var allowedAlignment = map[string]bool{
	"ALIGNED": true, "JP_ONLY": true, "EN_ONLY": true, "NO_JP_SOURCE": true, "BOTH_EMPTY": true,
}

var allowedDialogueRoles = map[string]bool{
	RoleSpoken: true, RoleNarration: true, RoleChoice: true, RoleSystem: true,
	RoleUI: true, RoleControl: true, RoleEmpty: true, RoleUnknown: true,
}

var allowedUtteranceKinds = map[string]bool{
	KindDialogue: true, KindInnerThought: true, KindBranchDialogue: true,
	KindChoiceOption: true, KindNarrationLine: true, KindCaption: true,
	KindSystemMessage: true, KindUILabel: true, KindUIText: true,
	KindControlMarker: true, KindEmpty: true, KindUnknown: true,
}

// ValidateOptions selects the validation contract for a canonical tree.
//
// Routine validation accepts an edited tree: reviewed records and active
// real_en are normal editorial state. Fresh-import purity additionally rejects
// any reviewed record or non-null real_en, which is what a source import must
// produce before editorial work starts.
type ValidateOptions struct {
	RequireFreshImport bool
}

// ValidateOutput validates the canonical shards, manifest and text fidelity.
// It is routine validation and therefore accepts an edited tree.
func ValidateOutput(c Config, corp *Corpus) (*ValidationReport, error) {
	return ValidateOutputWithOptions(c, corp, ValidateOptions{})
}

// ValidateFreshImport validates a source import and additionally enforces that
// every record is UNREVIEWED with a null real_en.
func ValidateFreshImport(c Config, corp *Corpus) (*ValidationReport, error) {
	return ValidateOutputWithOptions(c, corp, ValidateOptions{RequireFreshImport: true})
}

// ValidateOutputWithOptions is the shared implementation.
func ValidateOutputWithOptions(c Config, corp *Corpus, opt ValidateOptions) (*ValidationReport, error) {
	rep := &ValidationReport{
		SchemaVersion: 1,
		Checks:        map[string]bool{},
		Counts:        map[string]int{},
		Errors:        []string{},
	}
	fail := func(format string, args ...any) { rep.Errors = append(rep.Errors, fmt.Sprintf(format, args...)) }

	manPath := filepath.Join(c.TranslationsDir, "manifest.json")
	manBytes, err := os.ReadFile(manPath)
	if err != nil {
		return nil, err
	}
	var man Manifest
	if err := json.Unmarshal(manBytes, &man); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if man.SchemaVersion != 1 {
		fail("manifest schema_version %d", man.SchemaVersion)
	}
	// Read all shards.
	shards := map[string][]byte{}
	err = filepath.WalkDir(filepath.Join(c.TranslationsDir), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") || d.Name() == "manifest.json" {
			return nil
		}
		if strings.Contains(filepath.ToSlash(path), "/variants/") {
			return nil
		}
		rel, _ := filepath.Rel(c.TranslationsDir, path)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		shards[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	rep.Counts["shards"] = len(shards)
	rep.Checks["shard_inventory_matches_manifest"] = len(shards) == len(man.Files)
	if len(shards) != len(man.Files) {
		fail("shard count %d != manifest files %d", len(shards), len(man.Files))
	}
	manifestByPath := map[string]ManifestFile{}
	for _, f := range man.Files {
		manifestByPath[f.Path] = f
	}
	ids := map[string]bool{}
	recomputed := Manifest{SchemaVersion: 1, Counts: ManifestCounts{ByType: map[string]int{}, ByReviewStatus: map[string]int{}}}
	paths := make([]string, 0, len(shards))
	for p := range shards {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		b := shards[rel]
		mf, ok := manifestByPath[rel]
		if !ok {
			fail("shard %s missing from manifest", rel)
			continue
		}
		if mf.SHA256 != shaBytes(b) {
			fail("shard %s sha mismatch", rel)
		}
		if err := validateShardKeys(b); err != nil {
			fail("shard %s schema shape: %v", rel, err)
		}
		var shard Shard
		if err := json.Unmarshal(b, &shard); err != nil {
			fail("shard %s JSON: %v", rel, err)
			continue
		}
		if shard.SchemaVersion != 1 {
			fail("shard %s schema_version %d", rel, shard.SchemaVersion)
		}
		if mf.Records != len(shard.Records) {
			fail("shard %s record count %d != manifest %d", rel, len(shard.Records), mf.Records)
		}
		typ := "unclassified"
		if len(shard.Records) > 0 {
			typ = shard.Records[0].Function
		}
		if mf.Type != typ {
			fail("shard %s type %q != manifest %q", rel, typ, mf.Type)
		}
		recomputed.Counts.Files++
		recomputed.Counts.Records += len(shard.Records)
		for _, r := range shard.Records {
			if err := validateRecordShape(r); err != nil {
				fail("%s %s: %v", rel, r.RecordID, err)
			}
			if r.RecordID == "" {
				fail("%s empty record_id", rel)
			} else if ids[r.RecordID] {
				fail("duplicate record_id %s", r.RecordID)
			}
			ids[r.RecordID] = true
			if r.Text.JP != nil {
				recomputed.Counts.JP++
			}
			if r.Text.OfficialEN != nil {
				recomputed.Counts.OfficialEN++
			}
			if r.Text.RealEN != nil {
				recomputed.Counts.RealEN++
			}
			recomputed.Counts.ByType[r.Function]++
			recomputed.Counts.ByReviewStatus[r.Review.Status]++
			if opt.RequireFreshImport && (r.Text.RealEN != nil || r.Review.Status != "UNREVIEWED") {
				fail("fresh import contains reviewed/real_en record %s", r.RecordID)
			}
			if corp != nil {
				if err := validateRecordText(corp, r); err != nil {
					fail("%s: %v", r.RecordID, err)
				}
			}
		}
	}
	recomputed.Counts.MissingJP = recomputed.Counts.Records - recomputed.Counts.JP
	recomputed.Counts.MissingOfficialEN = recomputed.Counts.Records - recomputed.Counts.OfficialEN
	if !equalManifestCounts(man.Counts, recomputed.Counts) {
		fail("manifest counts do not match recomputed counts")
	}
	rep.Counts["records"] = recomputed.Counts.Records
	rep.Counts["jp"] = recomputed.Counts.JP
	rep.Counts["official_en"] = recomputed.Counts.OfficialEN
	rep.Counts["real_en"] = recomputed.Counts.RealEN
	reviewed := recomputed.Counts.Records - recomputed.Counts.ByReviewStatus["UNREVIEWED"]
	rep.Counts["reviewed"] = reviewed
	rep.Checks["unique_record_ids"] = true
	rep.Checks["manifest_hashes"] = true
	rep.Checks["text_fidelity"] = true
	rep.Checks["fresh_import_purity"] = recomputed.Counts.RealEN == 0 && reviewed == 0
	for _, e := range rep.Errors {
		if strings.Contains(e, "duplicate record_id") {
			rep.Checks["unique_record_ids"] = false
		}
		if strings.Contains(e, "sha mismatch") {
			rep.Checks["manifest_hashes"] = false
		}
		if strings.Contains(e, "text") || strings.Contains(e, "token") {
			rep.Checks["text_fidelity"] = false
		}
	}
	if len(rep.Errors) == 0 {
		rep.Status = "PASS"
	} else {
		rep.Status = "FAIL"
	}
	return rep, nil
}

func validateShardKeys(b []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return err
	}
	if len(top) != 2 {
		return fmt.Errorf("top-level keys must be exactly schema_version and records")
	}
	if _, ok := top["schema_version"]; !ok {
		return fmt.Errorf("missing schema_version")
	}
	rawRecords, ok := top["records"]
	if !ok {
		return fmt.Errorf("missing records")
	}
	var recs []json.RawMessage
	if err := json.Unmarshal(rawRecords, &recs); err != nil {
		return err
	}
	for i, raw := range recs {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
		for k := range m {
			if !allowedRecordKeys[k] {
				return fmt.Errorf("record %d: unexpected key %q", i, k)
			}
		}
		for _, key := range []string{"record_id", "source", "context", "text", "review"} {
			if _, ok := m[key]; !ok {
				return fmt.Errorf("record %d: missing %s", i, key)
			}
		}
		var text map[string]json.RawMessage
		if err := json.Unmarshal(m["text"], &text); err != nil {
			return fmt.Errorf("record %d text: %w", i, err)
		}
		if len(text) != 3 {
			return fmt.Errorf("record %d text keys must be exactly jp/official_en/real_en", i)
		}
		for _, key := range []string{"jp", "official_en", "real_en"} {
			if _, ok := text[key]; !ok {
				return fmt.Errorf("record %d text missing %s", i, key)
			}
		}
		var review map[string]json.RawMessage
		if err := json.Unmarshal(m["review"], &review); err != nil {
			return fmt.Errorf("record %d review: %w", i, err)
		}
		if len(review) != 1 {
			return fmt.Errorf("record %d review must contain only status", i)
		}
		if _, ok := review["status"]; !ok {
			return fmt.Errorf("record %d review missing status", i)
		}
	}
	return nil
}

func validateRecordShape(r Record) error {
	// The public schema is intentionally narrow: unknown top-level fields are
	// rejected. source/context remain open objects per the project schema.
	if r.RecordID == "" {
		return fmt.Errorf("missing record_id")
	}
	if r.Source.Platform == "" || r.Source.Bank == "" {
		return fmt.Errorf("source.platform/bank missing")
	}
	if r.Source.EntryIndex < 0 {
		return fmt.Errorf("source.entry_index negative")
	}
	if !allowedSpeakerStates[r.Context.Speaker.State] {
		return fmt.Errorf("invalid speaker state %q", r.Context.Speaker.State)
	}
	if !allowedSpeakerStates[r.Context.Addressee.State] {
		return fmt.Errorf("invalid addressee state %q", r.Context.Addressee.State)
	}
	if !allowedUsageStates[r.Context.UsageState] {
		return fmt.Errorf("invalid usage_state %q", r.Context.UsageState)
	}
	if !allowedAlignment[r.Context.Alignment] {
		return fmt.Errorf("invalid alignment %q", r.Context.Alignment)
	}
	if !allowedDialogueRoles[r.Context.DialogueRole] {
		return fmt.Errorf("invalid dialogue_role %q", r.Context.DialogueRole)
	}
	if !allowedUtteranceKinds[r.Context.UtteranceKind] {
		return fmt.Errorf("invalid utterance_kind %q", r.Context.UtteranceKind)
	}
	if r.Context.DialogueRole == RoleSpoken {
		if !r.Context.SpeakerApplicable {
			return fmt.Errorf("SPOKEN record with speaker_applicable=false")
		}
		if r.Context.Speaker.State == "UNKNOWN" && r.Context.Speaker.EntityID != nil {
			return fmt.Errorf("UNKNOWN speaker with entity_id")
		}
		if (r.Context.Speaker.State == "CORROBORATED" || r.Context.Speaker.State == "PROVEN_FROM_ATTRIBUTE" || r.Context.Speaker.State == "PROVEN_FROM_CALLSITE") && (r.Context.Speaker.EntityID == nil || *r.Context.Speaker.EntityID == "") {
			return fmt.Errorf("%s speaker without entity_id", r.Context.Speaker.State)
		}
	} else {
		if r.Context.SpeakerApplicable {
			return fmt.Errorf("non-SPOKEN record %q marked speaker_applicable", r.Context.DialogueRole)
		}
		if r.Context.Speaker.State != "UNKNOWN" {
			return fmt.Errorf("non-SPOKEN record has speaker state %q", r.Context.Speaker.State)
		}
	}
	if r.Function == "" {
		return fmt.Errorf("missing function")
	}
	if r.Review.Status != "UNREVIEWED" && r.Review.Status != "KEEP" && r.Review.Status != "CHANGE" {
		return fmt.Errorf("invalid review status %q", r.Review.Status)
	}
	return nil
}

func validateRecordText(corp *Corpus, r Record) error {
	bank := findBank(corp, r.Source.Bank)
	if bank == nil {
		return fmt.Errorf("bank %s not found in source", r.Source.Bank)
	}
	if r.Text.JP != nil {
		if bank.JP == nil || r.Source.EntryIndex >= len(bank.JP.File.TXT2.Strings) {
			return fmt.Errorf("JP text source index out of range")
		}
		raw, err := msbt.DecodeText(*r.Text.JP)
		if err != nil {
			return fmt.Errorf("JP token decode: %w", err)
		}
		if !bytes.Equal(raw, bank.JP.File.TXT2.Strings[r.Source.EntryIndex]) {
			return fmt.Errorf("JP token fidelity mismatch")
		}
	}
	if r.Text.OfficialEN != nil {
		if bank.EN == nil || r.Source.EntryIndex >= len(bank.EN.File.TXT2.Strings) {
			return fmt.Errorf("EN text source index out of range")
		}
		raw, err := msbt.DecodeText(*r.Text.OfficialEN)
		if err != nil {
			return fmt.Errorf("EN token decode: %w", err)
		}
		if !bytes.Equal(raw, bank.EN.File.TXT2.Strings[r.Source.EntryIndex]) {
			return fmt.Errorf("EN token fidelity mismatch")
		}
	}
	return nil
}

func findBank(c *Corpus, name string) *Bank {
	if c == nil {
		return nil
	}
	for i := range c.Banks {
		if c.Banks[i].Name == name {
			return &c.Banks[i]
		}
	}
	return nil
}

func equalManifestCounts(a, b ManifestCounts) bool {
	if a.Files != b.Files || a.Records != b.Records || a.JP != b.JP || a.OfficialEN != b.OfficialEN || a.RealEN != b.RealEN || a.MissingJP != b.MissingJP || a.MissingOfficialEN != b.MissingOfficialEN {
		return false
	}
	return equalIntMap(a.ByType, b.ByType) && equalIntMap(a.ByReviewStatus, b.ByReviewStatus)
}

func equalIntMap(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
