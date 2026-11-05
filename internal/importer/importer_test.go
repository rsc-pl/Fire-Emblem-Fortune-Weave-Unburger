package importer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"fefw/internal/msbt"
)

func testSide(locale, asset string, texts ...string) *Side {
	raw := make([][]byte, len(texts))
	tok := make([]string, len(texts))
	stats := make([]msbt.TokenStats, len(texts))
	for i, s := range texts {
		raw[i] = []byte(s)
		tok[i] = s
	}
	f := &msbt.File{TXT2: &msbt.TXT2{Strings: raw}}
	s := &Side{
		Locale:        locale,
		AssetPath:     asset,
		CompressedSHA: "00",
		File:          f,
		Labels:        map[uint32]string{},
		AliasCount:    map[uint32]int{},
		Text:          tok,
		TokenStats:    stats,
	}
	for i := range texts {
		s.Labels[uint32(i)] = "L" + string(rune('A'+i))
		s.AliasCount[uint32(i)] = 1
	}
	return s
}

func TestBuildBankRecordsAligned(t *testing.T) {
	jp := testSide("JPja", "Msg/JPja/A.msbt.zst", "jp")
	en := testSide("USen", "Msg/USen/A.msbt.zst", "en")
	records, rep, _, _, err := BuildBankRecords(Bank{Name: "A", JP: jp, EN: en}, nil, NewIDRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || rep.Aligned != 1 {
		t.Fatalf("records=%d aligned=%d", len(records), rep.Aligned)
	}
	r := records[0]
	if r.Context.Alignment != "ALIGNED" {
		t.Fatalf("alignment=%s", r.Context.Alignment)
	}
	if r.Text.JP == nil || *r.Text.JP != "jp" || r.Text.OfficialEN == nil || *r.Text.OfficialEN != "en" {
		t.Fatalf("text fields wrong: %+v", r.Text)
	}
	if r.Context.Speaker.State != "UNKNOWN" {
		t.Fatalf("speaker=%s", r.Context.Speaker.State)
	}
	if len(r.Context.Speaker.Evidence) != 0 {
		t.Fatalf("speaker evidence not empty")
	}
	if r.Text.RealEN != nil || r.Review.Status != "UNREVIEWED" {
		t.Fatalf("initial review fields wrong")
	}
	corp := &Corpus{Banks: []Bank{{Name: "A", JP: jp, EN: en}}}
	if err := validateRecordText(corp, r); err != nil {
		t.Fatalf("text fidelity: %v", err)
	}
}

func TestBuildBankRecordsLanguageOnly(t *testing.T) {
	jp := testSide("JPja", "Msg/JPja/B.msbt.zst", "jp")
	en := testSide("USen", "Msg/USen/B.msbt.zst", "en")
	// Make the label differ, so exact label+index alignment must not happen.
	jp.Labels[0] = "JP_LABEL"
	en.Labels[0] = "EN_LABEL"
	records, rep, _, _, err := BuildBankRecords(Bank{Name: "B", JP: jp, EN: en}, nil, NewIDRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || rep.JPOnly != 1 || rep.ENOnly != 1 {
		t.Fatalf("records=%d jp_only=%d en_only=%d", len(records), rep.JPOnly, rep.ENOnly)
	}
	for _, r := range records {
		if r.Context.Alignment == "ALIGNED" {
			t.Fatalf("positional alignment used for %s", r.RecordID)
		}
	}
}

func TestDuplicateRecordIDsRejected(t *testing.T) {
	jp := testSide("JPja", "Msg/JPja/C.msbt.zst", "jp")
	en := testSide("USen", "Msg/USen/C.msbt.zst", "en")
	ids := NewIDRegistry()
	bank := Bank{Name: "C", JP: jp, EN: en}
	if _, _, _, _, err := BuildBankRecords(bank, nil, ids); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := BuildBankRecords(bank, nil, ids); err == nil {
		t.Fatal("expected duplicate record_id error")
	}
}

func TestVerifyLedgerRejectsDifferentSourceBuild(t *testing.T) {
	inv := &InventoryReport{Files: []FileEntry{{Path: "a", Size: 10, SHA256: "new"}}}
	led := &Ledger{SourceInventory: []FileEntry{{Path: "a", Size: 10, SHA256: "old"}}}
	if err := VerifyLedger(inv, led); err == nil {
		t.Fatal("expected unverified source change error")
	}
	led.SourceInventory[0].SHA256 = "new"
	if err := VerifyLedger(inv, led); err != nil {
		t.Fatalf("identical source should pass: %v", err)
	}
}

func TestValidateRejectsStaleHash(t *testing.T) {
	dir := t.TempDir()
	shard := Shard{SchemaVersion: 1, Records: []Record{}}
	b, _ := json.Marshal(shard)
	if err := os.MkdirAll(filepath.Join(dir, "unclassified"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unclassified", "A.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	man := Manifest{
		SchemaVersion: 1,
		Counts:        ManifestCounts{ByType: map[string]int{}, ByReviewStatus: map[string]int{}},
		Files:         []ManifestFile{{Path: "unclassified/A.json", SHA256: "deadbeef", Records: 0, Type: "unclassified"}},
	}
	mb, _ := json.Marshal(man)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := ValidateOutput(Config{TranslationsDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "FAIL" {
		t.Fatalf("expected FAIL, got %s", rep.Status)
	}
}

func validationFixtureRecord(id string, real *string, status string) Record {
	return Record{
		RecordID: id,
		Source:   Source{Platform: "switch2", Bank: "A", EntryIndex: 0},
		Context: Context{
			Speaker:           EntityRef{State: "UNKNOWN"},
			Addressee:         EntityRef{State: "UNKNOWN"},
			UsageState:        "UNVERIFIED_CANDIDATE",
			Alignment:         "ALIGNED",
			DialogueRole:      RoleNarration,
			UtteranceKind:     KindNarrationLine,
			SpeakerApplicable: false,
		},
		Function: "unclassified",
		Text:     Text{JP: strp("jp"), OfficialEN: strp("official"), RealEN: real},
		Review:   Review{Status: status},
	}
}

func writeValidationFixture(t *testing.T, records []Record) (string, Config) {
	t.Helper()
	dir := t.TempDir()
	shard := Shard{SchemaVersion: 1, Records: records}
	b, err := marshalDeterministic(shard)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "unclassified"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unclassified", "A.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	_, manBytes, err := BuildManifestFromShards(map[string][]byte{"unclassified/A.json": b})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, Config{TranslationsDir: dir}
}

func TestValidateRoutineAcceptsEditedTree(t *testing.T) {
	dir, cfg := writeValidationFixture(t, []Record{validationFixtureRecord("edited", strp("authored"), "CHANGE")})
	rep, err := ValidateOutput(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "PASS" {
		t.Fatalf("routine validation rejected an edited tree: %v", rep.Errors)
	}
	if rep.Checks["fresh_import_purity"] {
		t.Fatal("edited tree reported as fresh-import pure")
	}
	if rep.Counts["real_en"] != 1 || rep.Counts["reviewed"] != 1 {
		t.Fatalf("counts wrong: %+v", rep.Counts)
	}
	fresh, err := ValidateFreshImport(Config{TranslationsDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "FAIL" {
		t.Fatal("fresh-import validation accepted an edited tree")
	}
}

func TestValidateFreshImportAcceptsPristineTree(t *testing.T) {
	_, cfg := writeValidationFixture(t, []Record{validationFixtureRecord("fresh", nil, "UNREVIEWED")})
	rep, err := ValidateFreshImport(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "PASS" {
		t.Fatalf("fresh-import validation rejected a pristine tree: %v", rep.Errors)
	}
	if !rep.Checks["fresh_import_purity"] {
		t.Fatal("fresh tree not reported as pure")
	}
}

func TestValidateRoutineStillRejectsCorruptManifest(t *testing.T) {
	dir, cfg := writeValidationFixture(t, []Record{validationFixtureRecord("ok", nil, "UNREVIEWED")})
	manPath := filepath.Join(dir, "manifest.json")
	b, err := os.ReadFile(manPath)
	if err != nil {
		t.Fatal(err)
	}
	var man Manifest
	if err := json.Unmarshal(b, &man); err != nil {
		t.Fatal(err)
	}
	man.Files[0].SHA256 = "deadbeef"
	b, _ = json.Marshal(man)
	if err := os.WriteFile(manPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := ValidateOutput(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "FAIL" {
		t.Fatal("corrupt manifest hash accepted")
	}
}
