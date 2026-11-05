package importer

import (
	"encoding/json"
	"testing"

	"fefw/internal/msbt"
)

func p01cBank(labels, texts []string, attrs [][]msbt.AttrValue) Bank {
	raw := make([][]byte, len(texts))
	tok := make([]string, len(texts))
	for i := range texts {
		raw[i] = []byte(texts[i])
		tok[i] = texts[i]
	}
	f := &msbt.File{TXT2: &msbt.TXT2{Strings: raw}}
	s := &Side{Locale: "JPja", AssetPath: "Msg/JPja/T.msbt.zst", File: f, Labels: map[uint32]string{}, AliasCount: map[uint32]int{}, Text: tok, Attrs: attrs}
	for i, l := range labels {
		s.Labels[uint32(i)] = l
		s.AliasCount[uint32(i)] = 1
	}
	return Bank{Name: "T", JP: s, EN: s}
}

func rowAttrs(pid, mpid, event, pv string) []msbt.AttrValue {
	var row []msbt.AttrValue
	if pid != "" {
		row = append(row, msbt.AttrValue{Name: "PID", Type: 8, String: pid})
	}
	if mpid != "" {
		row = append(row, msbt.AttrValue{Name: "MPID", Type: 8, String: mpid})
	}
	if event != "" {
		row = append(row, msbt.AttrValue{Name: "Event", Type: 8, String: event})
	}
	if pv != "" {
		row = append(row, msbt.AttrValue{Name: "PartVoice", Type: 9, EnumName: pv, Enum: 1})
	}
	return row
}

func TestP01CChoiceOptionWithPIDIsNotASpeaker(t *testing.T) {
	labels := []string{"MID_EV_001", "MID_EV_002"}
	texts := []string{"<NCA>はい", "<NCB>いいえ"}
	attrs := [][]msbt.AttrValue{rowAttrs("レダ", "", "キャラモーション再生(PID_レダ, 同意)", ""), rowAttrs("", "", "", "")}
	cl := ClassifyP01CRecord(p01cBank(labels, texts, attrs), 0, "story", "", false)
	if cl.Role != RoleChoice || cl.Kind != KindChoiceOption {
		t.Fatalf("choice not classified: %+v", cl)
	}
	sp := ResolveSpeakerP01C(cl.Role, cl.Kind, "レダ", "レダ", "", "キャラモーション再生(PID_レダ, 同意)", "", map[string]bool{"レダ": true}, "a", "b")
	if sp.State != "UNKNOWN" || sp.Applicable {
		t.Fatalf("choice speaker promoted: %+v", sp)
	}
	if sp.Reason != "NOT_APPLICABLE_CHOICE" {
		t.Fatalf("reason=%s", sp.Reason)
	}
}

func TestP01CBranchReplyKeepsSpeaker(t *testing.T) {
	labels := []string{"MID_EV_004"}
	texts := []string{"<ACA>やっぱな。"}
	attrs := [][]msbt.AttrValue{rowAttrs("ナジャ", "", "キャラモーション再生(PID_ナジャ, 同意)", "")}
	cl := ClassifyP01CRecord(p01cBank(labels, texts, attrs), 0, "story", "", false)
	if cl.Role != RoleSpoken || cl.Kind != KindBranchDialogue || cl.BranchPhase != "REPLY" || cl.BranchOption != "A" {
		t.Fatalf("branch reply misclassified: %+v", cl)
	}
}

func TestP01CControlSystemCaptionNarrationEmpty(t *testing.T) {
	labels := []string{"L0", "L1", "L2", "L3", "L4"}
	texts := []string{"<TH>", "<DLG>移動してください", "<TLP>試合情報", "", "その日の記憶は赤。"}
	bank := p01cBank(labels, texts, [][]msbt.AttrValue{{}, {}, {}, {}, {}})
	cases := []struct {
		idx  int
		role string
		kind string
	}{
		{0, RoleControl, KindControlMarker},
		{1, RoleSystem, KindSystemMessage},
		{2, RoleNarration, KindCaption},
		{3, RoleEmpty, KindEmpty},
	}
	for _, c := range cases {
		cl := ClassifyP01CRecord(bank, c.idx, "story", "", false)
		if cl.Role != c.role || cl.Kind != c.kind {
			t.Fatalf("idx %d: got %s/%s want %s/%s (%s)", c.idx, cl.Role, cl.Kind, c.role, c.kind, cl.Reason)
		}
	}
	// Inside a validated narration block the visible prose is NARRATION.
	cl := ClassifyP01CRecord(bank, 4, "story", "", true)
	if cl.Role != RoleNarration || cl.Kind != KindNarrationLine {
		t.Fatalf("narration block line: %+v", cl)
	}
}

func TestP01CNarrationBlockDetection(t *testing.T) {
	labels := []string{"MID_NR_00", "MID_NR_00_001", "MID_NR_00_002", "MID_EV_01", "MID_EV_01_001"}
	texts := []string{"<SH>", "赤い炎。", "街が燃える。", "<TH>", "ここは仮想空間だ。"}
	bank := p01cBank(labels, texts, [][]msbt.AttrValue{{}, {}, {}, {}, rowAttrs("ゼノン", "", "", "")})
	nar := p01cNarrationBlock(bank)
	if !nar[1] || !nar[2] {
		t.Fatalf("narration block not marked: %v", nar)
	}
	if nar[3] || nar[4] {
		t.Fatalf("block leaked past <TH>: %v", nar)
	}
}

func TestP01CUIvsSpokenByPersonID(t *testing.T) {
	labels := []string{"MIID_WoodSword", "MID_Gmap_CaptureAnimalFailed_Jun_001"}
	texts := []string{"木の剣", "なんと、汚らわしい……"}
	attrs := [][]msbt.AttrValue{{}, rowAttrs("ジュラ", "", "", "")}
	bank := p01cBank(labels, texts, attrs)
	if cl := ClassifyP01CRecord(bank, 0, "items", "", false); cl.Role != RoleUI {
		t.Fatalf("item label not UI: %+v", cl)
	}
	cl := ClassifyP01CRecord(bank, 1, "ui", "", false)
	if cl.Role != RoleSpoken || cl.Kind != KindDialogue {
		t.Fatalf("person-attributed UI reaction not spoken: %+v", cl)
	}
}

func TestP01CInnerThoughtFromPartVoice(t *testing.T) {
	labels := []string{"MID_SE_A_S_010"}
	texts := []string{"（……手のかかる兄だ）"}
	attrs := [][]msbt.AttrValue{rowAttrs("イシュマール", "", "", "Thought1")}
	cl := ClassifyP01CRecord(p01cBank(labels, texts, attrs), 0, "supports", "Thought1", false)
	if cl.Role != RoleSpoken || cl.Kind != KindInnerThought {
		t.Fatalf("thought not classified: %+v", cl)
	}
}

func TestP01CAuthoredGroupAndMarker(t *testing.T) {
	if got := AuthoredGroup("MID_EV30_004_m01_012"); got != "MID_EV30_004_m01" {
		t.Fatalf("group=%s", got)
	}
	if got := AuthoredGroup("MIID_WoodSword"); got != "MIID_WoodSword" {
		t.Fatalf("group changed a label without a numeric slot: %s", got)
	}
	if got := LeadingMarker("  <NCA>はい"); got != "NCA" {
		t.Fatalf("marker=%s", got)
	}
}

func TestP01CSpeakerAttributeAndCorroboration(t *testing.T) {
	person := map[string]bool{"レダ": true}
	base := ResolveSpeakerP01C(RoleSpoken, KindDialogue, "レダ", "レダ", "", "", "", person, "a", "b")
	if base.State != "PROVEN_FROM_ATTRIBUTE" || base.EntityID != "レダ" {
		t.Fatalf("attribute evidence not promoted: %+v", base)
	}
	corr := ResolveSpeakerP01C(RoleSpoken, KindDialogue, "レダ", "レダ", "", "キャラモーション再生(PID_レダ, 深刻)", "", person, "a", "b")
	if corr.State != "CORROBORATED" {
		t.Fatalf("motion not corroborated: %+v", corr)
	}
}

func TestP01CSoleOtherActorStillSpeaker(t *testing.T) {
	person := map[string]bool{"ディートリヒ": true, "エスメラルダ": true}
	ev := "キャラ装備解除(PID_エスメラルダ)\nキャラモーション再生(PID_エスメラルダ, ,EventIdleLoop ,0.1)"
	sp := ResolveSpeakerP01C(RoleSpoken, KindDialogue, "ディートリヒ", "ディートリヒ", "", ev, "", person, "a", "b")
	if sp.State != "PROVEN_FROM_ATTRIBUTE" || sp.EntityID != "ディートリヒ" {
		t.Fatalf("staged-listener counterexample rejected: %+v", sp)
	}
	if sp.Reason != "PID_PERSON_TABLE_STAGED_OTHER_ACTOR_IS_LISTENER" {
		t.Fatalf("reason=%s", sp.Reason)
	}
}

func TestP01CMultiOtherActorStillSpeaker(t *testing.T) {
	person := map[string]bool{"レダ": true, "カイ": true, "セオドラ": true}
	ev := "キャラモーション再生(PID_カイ, 深刻)\nキャラ視線(PID_セオドラ, PID_カイ)"
	sp := ResolveSpeakerP01C(RoleSpoken, KindDialogue, "レダ", "レダ", "", ev, "", person, "a", "b")
	if sp.State != "PROVEN_FROM_ATTRIBUTE" || sp.EntityID != "レダ" {
		t.Fatalf("multi-actor speaker lost: %+v", sp)
	}
}

func TestP01CNegativeSpeakerFixtures(t *testing.T) {
	person := map[string]bool{"レダ": true}
	// PID not in Person.bin
	if sp := ResolveSpeakerP01C(RoleSpoken, KindDialogue, "謎", "謎", "", "", "", person, "a", "b"); sp.State != "UNKNOWN" || sp.Reason != "PID_NOT_IN_PERSON_TABLE" {
		t.Fatalf("non-person promoted: %+v", sp)
	}
	// JP/EN disagreement
	if sp := ResolveSpeakerP01C(RoleSpoken, KindDialogue, "レダ", "カイ", "", "", "", person, "a", "b"); sp.State != "CONFLICT" {
		t.Fatalf("JP/EN mismatch not conflict: %+v", sp)
	}
	// MPID only
	if sp := ResolveSpeakerP01C(RoleSpoken, KindDialogue, "", "", "Layridana", "", "", person, "a", "b"); sp.State != "UNKNOWN" || sp.Reason != "NO_PID_MPID_ONLY" || sp.EntityID != "" {
		t.Fatalf("MPID promoted: %+v", sp)
	}
	// No PID, no MPID
	if sp := ResolveSpeakerP01C(RoleSpoken, KindDialogue, "", "", "", "", "", person, "a", "b"); sp.Reason != "NO_PID" {
		t.Fatalf("no-PID changed: %+v", sp)
	}
}

func TestP01CDeterministicBuild(t *testing.T) {
	jp := testSide("JPja", "Msg/JPja/A.msbt.zst", "<NCA>はい", "本文", "<TH>")
	en := testSide("USen", "Msg/USen/A.msbt.zst", "<NCA>yes", "body", "<TH>")
	att := make([][]msbt.AttrValue, 3)
	att[0] = rowAttrs("レダ", "", "", "")
	att[1] = rowAttrs("レダ", "", "キャラモーション再生(PID_レダ, 同意)", "")
	jp.Attrs = att
	en.Attrs = att
	corp := &Corpus{Banks: []Bank{{Name: "A", JP: jp, EN: en}}, JPProject: nil}
	r1, err := BuildP01C(corp, &P01BContext{Entities: &EntityIndex{PersonIDs: map[string]bool{"レダ": true}}})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := BuildP01C(corp, &P01BContext{Entities: &EntityIndex{PersonIDs: map[string]bool{"レダ": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(r1.ManifestBytes) != string(r2.ManifestBytes) {
		t.Fatal("manifest not deterministic")
	}
	for p, b := range r1.Shards {
		if string(b) != string(r2.Shards[p]) {
			t.Fatalf("shard %s not deterministic", p)
		}
	}
	// Choice option with a PID must not be promoted in the built record.
	found := false
	for _, b := range r1.Shards {
		var sh Shard
		_ = json.Unmarshal(b, &sh)
		for _, r := range sh.Records {
			if r.Context.DialogueRole == RoleChoice {
				found = true
				if r.Context.Speaker.State != "UNKNOWN" || r.Context.SpeakerApplicable {
					t.Fatalf("choice promoted: %+v", r.Context.Speaker)
				}
			}
		}
	}
	if !found {
		t.Fatal("choice record not present in fixture")
	}
}

func TestP01CValidatorRejectsWrongApplicability(t *testing.T) {
	id := "レダ"
	bad1 := Record{RecordID: "r1", Source: Source{Platform: "switch2", Bank: "A", EntryIndex: 0}, Function: "story", Review: Review{Status: "UNREVIEWED"},
		Context: Context{Speaker: EntityRef{State: "CORROBORATED", EntityID: &id, Evidence: []Evidence{}}, Addressee: EntityRef{State: "UNKNOWN"}, UsageState: "UNVERIFIED_CANDIDATE", Alignment: "ALIGNED", DialogueRole: RoleChoice, UtteranceKind: KindChoiceOption, SpeakerApplicable: false}}
	if err := validateRecordShape(bad1); err == nil {
		t.Fatal("non-spoken record with promoted speaker was accepted")
	}
	bad2 := Record{RecordID: "r2", Source: Source{Platform: "switch2", Bank: "A", EntryIndex: 0}, Function: "story", Review: Review{Status: "UNREVIEWED"},
		Context: Context{Speaker: EntityRef{State: "UNKNOWN"}, Addressee: EntityRef{State: "UNKNOWN"}, UsageState: "UNVERIFIED_CANDIDATE", Alignment: "ALIGNED", DialogueRole: RoleSpoken, UtteranceKind: KindDialogue, SpeakerApplicable: false}}
	if err := validateRecordShape(bad2); err == nil {
		t.Fatal("SPOKEN record with speaker_applicable=false was accepted")
	}
}
