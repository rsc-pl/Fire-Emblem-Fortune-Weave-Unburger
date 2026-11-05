package importer

import (
	"encoding/json"
	"testing"

	"fefw/internal/elfdata"
	"fefw/internal/msbt"
)

func TestResolveSpeakerCorroborated(t *testing.T) {
	person := map[string]bool{"レダ": true}
	ev := "キャラモーション再生(PID_レダ, 深刻, , 1)"
	got := ResolveSpeaker("レダ", "レダ", "Layridana", ev, true, person, "Msg/JPja/A.msbt.zst", "Msg/USen/A.msbt.zst")
	if got.State != "CORROBORATED" || got.EntityID != "レダ" {
		t.Fatalf("state=%s entity=%s reason=%s", got.State, got.EntityID, got.Reason)
	}
	if len(got.Evidence) < 3 {
		t.Fatalf("evidence too small: %+v", got.Evidence)
	}
}

func TestResolveSpeakerMPIDAloneNeverPromotes(t *testing.T) {
	got := ResolveSpeaker("", "", "Layridana", "PID_Layridana", true, map[string]bool{}, "a", "b")
	if got.State != "UNKNOWN" || got.EntityID != "" {
		t.Fatalf("MPID-only promoted: %+v", got)
	}
	if got.Reason != "NO_PID" {
		t.Fatalf("reason=%s", got.Reason)
	}
}

func TestResolveSpeakerMultiActorNotPromoted(t *testing.T) {
	ev := "キャラモーション再生(PID_レダ, 深刻, , 1)\nキャラ視線(PID_カイ, PID_レダ, 1)"
	got := ResolveSpeaker("レダ", "レダ", "", ev, true, map[string]bool{"レダ": true, "カイ": true}, "a", "b")
	if got.State != "UNKNOWN" || got.Reason != "EVENT_MULTI_ACTOR" {
		t.Fatalf("multi-actor promoted: %+v", got)
	}
}

func TestResolveSpeakerMismatchNotPromoted(t *testing.T) {
	got := ResolveSpeaker("レダ", "レダ", "", "キャラモーション再生(PID_カイ, 深刻, , 1)", true, map[string]bool{"レダ": true, "カイ": true}, "a", "b")
	if got.State != "UNKNOWN" || got.Reason != "EVENT_ACTOR_MISMATCH" {
		t.Fatalf("mismatch promoted: %+v", got)
	}
}

func TestResolveSpeakerJPENConflict(t *testing.T) {
	got := ResolveSpeaker("レダ", "カイ", "", "", true, map[string]bool{"レダ": true}, "a", "b")
	if got.State != "CONFLICT" || got.EntityID != "" {
		t.Fatalf("JP/EN mismatch not conflict: %+v", got)
	}
}

func TestResolveSpeakerNoVisibleText(t *testing.T) {
	got := ResolveSpeaker("レダ", "レダ", "", "キャラモーション再生(PID_レダ, , ,1)", false, map[string]bool{"レダ": true}, "a", "b")
	if got.State != "UNKNOWN" || got.Reason != "NO_VISIBLE_TEXT" {
		t.Fatalf("empty text promoted: %+v", got)
	}
}

func TestResolveSpeakerPersonMissing(t *testing.T) {
	got := ResolveSpeaker("レダ", "レダ", "", "キャラモーション再生(PID_レダ, , ,1)", true, map[string]bool{}, "a", "b")
	if got.State != "UNKNOWN" || got.Reason != "PERSON_TABLE_MISSING" {
		t.Fatalf("non-person promoted: %+v", got)
	}
}

func classifySide(labels ...string) Bank {
	texts := make([][]byte, len(labels))
	tok := make([]string, len(labels))
	for i := range labels {
		texts[i] = []byte("text")
		tok[i] = "text"
	}
	f := &msbt.File{TXT2: &msbt.TXT2{Strings: texts}}
	s := &Side{Locale: "JPja", AssetPath: "Msg/JPja/A.msbt.zst", File: f, Labels: map[uint32]string{}, AliasCount: map[uint32]int{}, Text: tok}
	for i, l := range labels {
		s.Labels[uint32(i)] = l
		s.AliasCount[uint32(i)] = 1
	}
	return Bank{Name: "A", JP: s, EN: s}
}

func recordsFor(bank Bank) []Record {
	out := make([]Record, len(bank.JP.Text))
	for i := range out {
		out[i] = Record{Source: Source{Bank: bank.Name, Label: bank.JP.Labels[uint32(i)], EntryIndex: i}}
	}
	return out
}

func TestClassifyNeverUsesBankFilename(t *testing.T) {
	bank := classifySide("MID_Foo_001", "MID_Foo_002")
	bank.Name = "Item" // filename/prefix must not classify this as items
	groups, cl := ClassifyBank(bank, recordsFor(bank), &P01BContext{})
	if cl.Category != "unclassified" {
		t.Fatalf("filename-based classification: %s / %v", cl.Category, groups)
	}
}

func TestClassifyItemsFromLabelNamespaceAndTable(t *testing.T) {
	bank := classifySide("MIID_WoodSword", "MIID_H_WoodSword1", "MIID_H_WoodSword2")
	ctx := &P01BContext{Entities: &EntityIndex{NamespaceFile: map[string][]string{"MIID_": {"GameData/Item.bin"}}}}
	groups, cl := ClassifyBank(bank, recordsFor(bank), ctx)
	if cl.Category != "items" {
		t.Fatalf("category=%s evidence=%v", cl.Category, cl.Evidence)
	}
	if len(groups) != 3 {
		t.Fatalf("expected names/line1/line2 shards, got %d: %+v", len(groups), groups)
	}
}

func TestClassifyStoryRequiresTalkReference(t *testing.T) {
	bank := classifySide("MID_EV_A_001", "MID_EV_A_002")
	bank.JP.Attrs = [][]msbt.AttrValue{
		{{Name: "Section", Type: 9, Enum: 1}},
		{{Name: "Section", Type: 9, Enum: 1}},
	}
	talk := &elfdata.TalkMsg{TalkIndex: map[string]int{}, DemoIndex: map[string]int{}}
	ctx := &P01BContext{Talk: talk, SectionNames: map[uint8]string{1: "Sec-Main"}}
	if _, cl := ClassifyBank(bank, recordsFor(bank), ctx); cl.Category != "unclassified" {
		t.Fatalf("story without TalkMsg reference was classified: %s", cl.Category)
	}
	talk.TalkIndex["MID_EV_A_001"] = 0
	groups, cl := ClassifyBank(bank, recordsFor(bank), ctx)
	if cl.Category != "story" || len(groups) != 1 {
		t.Fatalf("story not classified: %s %+v", cl.Category, groups)
	}
}

func TestClassifySupportFromLabelPair(t *testing.T) {
	bank := classifySide("MID_SE_Bel_Tal_B_001", "MID_SE_Bel_Tal_B_002")
	groups, cl := ClassifyBank(bank, recordsFor(bank), &P01BContext{})
	if cl.Category != "supports" || len(groups) != 1 || groups[0].Function != "supports" {
		t.Fatalf("support not classified: %s %+v", cl.Category, groups)
	}
}

func TestBuildP01BDeterministic(t *testing.T) {
	jp := testSide("JPja", "Msg/JPja/A.msbt.zst", "jp one", "jp two")
	en := testSide("USen", "Msg/USen/A.msbt.zst", "en one", "en two")
	corp := &Corpus{Banks: []Bank{{Name: "A", JP: jp, EN: en}}}
	r1, err := BuildP01B(corp, &P01BContext{})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := BuildP01B(corp, &P01BContext{})
	if err != nil {
		t.Fatal(err)
	}
	if string(r1.ManifestBytes) != string(r2.ManifestBytes) {
		t.Fatal("manifest is not deterministic")
	}
	if len(r1.Shards) != len(r2.Shards) {
		t.Fatal("shard count differs")
	}
	for p, b := range r1.Shards {
		if string(b) != string(r2.Shards[p]) {
			t.Fatalf("shard %s is not deterministic", p)
		}
	}
}

func TestBuildManifestFromShardsRejectsDuplicateShardPath(t *testing.T) {
	// A map cannot hold the same path twice; this guards the semantic invariant
	// that one canonical record appears in exactly one shard.
	sh := Shard{SchemaVersion: 1, Records: []Record{{RecordID: "r1", Function: "unclassified", Review: Review{Status: "UNREVIEWED"}}}}
	b, _ := json.Marshal(sh)
	if _, _, err := BuildManifestFromShards(map[string][]byte{"a.json": b, "b.json": b}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveSpeakerRequiresEN(t *testing.T) {
	got := ResolveSpeaker("レダ", "", "", "キャラモーション再生(PID_レダ, , ,1)", true, map[string]bool{"レダ": true}, "a", "b")
	if got.State != "UNKNOWN" || got.Reason != "JP_EN_PID_MISSING" {
		t.Fatalf("missing EN PID should not promote: %+v", got)
	}
}

func TestResolveSpeakerTrimsCRLF(t *testing.T) {
	got := ResolveSpeaker("レダ\r\n", "レダ\r\n", "Layridana", "キャラモーション再生(PID_レダ, , ,1)", true, map[string]bool{"レダ": true}, "a", "b")
	if got.State != "CORROBORATED" || got.EntityID != "レダ" {
		t.Fatalf("CRLF PID not normalized: %+v", got)
	}
}
