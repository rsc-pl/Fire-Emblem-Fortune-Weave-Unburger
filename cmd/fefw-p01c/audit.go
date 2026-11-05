package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"fefw/internal/importer"
)

// p01cRecordIndex indexes built records by id and by bank+label+index.
type p01cRecordIndex struct {
	ByID   map[string]importer.Record
	ByKey  map[string]importer.Record
	Bank   map[string]importer.Bank
	Corpus *importer.Corpus
}

func buildRecordIndex(res *importer.P01CResult, corp *importer.Corpus) *p01cRecordIndex {
	x := &p01cRecordIndex{ByID: map[string]importer.Record{}, ByKey: map[string]importer.Record{}, Bank: map[string]importer.Bank{}, Corpus: corp}
	if corp != nil {
		for _, b := range corp.Banks {
			x.Bank[b.Name] = b
		}
	}
	for _, shb := range res.Shards {
		var sh importer.Shard
		if json.Unmarshal(shb, &sh) != nil {
			continue
		}
		for _, r := range sh.Records {
			x.ByID[r.RecordID] = r
			x.ByKey[fmt.Sprintf("%s\x00%s\x00%d", r.Source.Bank, r.Source.Label, r.Source.EntryIndex)] = r
		}
	}
	return x
}

func (x *p01cRecordIndex) find(bank, label string, idx int) (importer.Record, bool) {
	r, ok := x.ByKey[fmt.Sprintf("%s\x00%s\x00%d", bank, label, idx)]
	return r, ok
}

func writeCandidates(cfg importer.Config, res *importer.P01CResult) {
	var b strings.Builder
	b.WriteString("record_id\tbank\tentry_index\tentity_id\tentity_namespace\trole\tkind\tstate\treason\tpromotion\n")
	for _, c := range res.Candidates {
		fmt.Fprintf(&b, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.RecordID, c.Bank, c.Index, c.EntityID, c.EntityNS, c.Role, c.Kind, c.State, c.Reason, c.Promotion)
	}
	writeText(filepath.Join(cfg.EvidenceDir, "p01c_speaker_candidates.tsv"), b.String())
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_speaker_candidates.json"), map[string]any{
		"schema_version": 1,
		"candidates":     res.Candidates,
		"note":           "These rows are never public speaker values. CHOICE_OWNER_NOT_SPEAKER rows document the audited counterexample class where a choice option carries the interlocutor PID.",
	})
}

type p01cBranchGroup struct {
	Bank     string              `json:"bank"`
	Group    string              `json:"authored_group"`
	SceneRef *string             `json:"scene_ref"`
	Prompts  map[string][]string `json:"prompts"`
	Replies  map[string][]string `json:"replies"`
	Records  int                 `json:"records"`
	FirstID  string              `json:"first_record_id"`
	LastID   string              `json:"last_record_id"`
}

func writeBranchMap(cfg importer.Config, res *importer.P01CResult) {
	groups := map[string]*p01cBranchGroup{}
	order := []string{}
	for _, shb := range res.Shards {
		var sh importer.Shard
		_ = json.Unmarshal(shb, &sh)
		for _, r := range sh.Records {
			if r.Context.BranchTag == nil || r.Context.AuthoredGroup == nil {
				continue
			}
			key := r.Source.Bank + "\x00" + *r.Context.AuthoredGroup
			g := groups[key]
			if g == nil {
				g = &p01cBranchGroup{Bank: r.Source.Bank, Group: *r.Context.AuthoredGroup, Prompts: map[string][]string{}, Replies: map[string][]string{}, FirstID: r.RecordID, SceneRef: r.Context.SceneRef}
				groups[key] = g
				order = append(order, key)
			}
			g.Records++
			g.LastID = r.RecordID
			opt := ""
			if r.Context.BranchOption != nil {
				opt = *r.Context.BranchOption
			}
			if r.Context.BranchPhase != nil && *r.Context.BranchPhase == "PROMPT" {
				g.Prompts[opt] = append(g.Prompts[opt], r.RecordID)
			} else {
				g.Replies[opt] = append(g.Replies[opt], r.RecordID)
			}
		}
	}
	sort.Strings(order)
	out := make([]*p01cBranchGroup, 0, len(order))
	for _, k := range order {
		out = append(out, groups[k])
	}
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_branch_map.json"), map[string]any{
		"schema_version": 1,
		"groups":         out,
		"note":           "Branch adjacency is reconstructed from the authored leading markers only: <NCx>/<ICx> are choice prompts (PROMPT, option letter A/B/C) and <ACx> are post-choice replies (REPLY). No filename or slot order is used to infer chronology.",
	})
}

type p01cSceneGroup struct {
	Bank       string         `json:"bank"`
	Group      string         `json:"authored_group"`
	SceneRef   *string        `json:"scene_ref"`
	Records    int            `json:"records"`
	ByRole     map[string]int `json:"by_role"`
	FirstID    string         `json:"first_record_id"`
	LastID     string         `json:"last_record_id"`
	FirstIndex int            `json:"first_entry_index"`
	LastIndex  int            `json:"last_entry_index"`
}

func writeSceneMap(cfg importer.Config, res *importer.P01CResult) {
	groups := map[string]*p01cSceneGroup{}
	order := []string{}
	for _, shb := range res.Shards {
		var sh importer.Shard
		_ = json.Unmarshal(shb, &sh)
		for _, r := range sh.Records {
			if r.Context.AuthoredGroup == nil {
				continue
			}
			key := r.Source.Bank + "\x00" + *r.Context.AuthoredGroup
			g := groups[key]
			if g == nil {
				g = &p01cSceneGroup{Bank: r.Source.Bank, Group: *r.Context.AuthoredGroup, SceneRef: r.Context.SceneRef, ByRole: map[string]int{}, FirstID: r.RecordID, FirstIndex: r.Source.EntryIndex}
				groups[key] = g
				order = append(order, key)
			}
			g.Records++
			g.ByRole[r.Context.DialogueRole]++
			g.LastID = r.RecordID
			g.LastIndex = r.Source.EntryIndex
		}
	}
	sort.Strings(order)
	out := make([]*p01cSceneGroup, 0, len(order))
	for _, k := range order {
		out = append(out, groups[k])
	}
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_scene_map.json"), map[string]any{
		"schema_version": 1,
		"groups":         out,
		"note":           "authored_group is the authored label prefix after removing the trailing numeric slot. It is source text, not a game scene id. scene_ref is populated only when exactly one script asset references the label.",
	})
}

func writeVoiceAudit(cfg importer.Config, l *loaded) {
	partVoice := map[string]int{}
	sound := map[string]int{}
	soundBanks := map[string]int{}
	voiceAttrs := map[string]int{}
	for _, b := range l.corp.Banks {
		for i := range b.JP.Text {
			if i >= len(b.JP.Attrs) {
				continue
			}
			for _, v := range b.JP.Attrs[i] {
				switch v.Name {
				case "PartVoice":
					if v.Enum != 0 {
						partVoice[v.EnumName]++
					}
				case "Sound":
					if strings.TrimSpace(v.String) != "" {
						first := strings.SplitN(strings.TrimSpace(v.String), "(", 2)[0]
						sound[first]++
						soundBanks[b.Name]++
					}
				case "Voice_Type", "Voice_Timing", "Voice_File_Name", "VoicePlay_JP", "VoicePlay_EN", "Voice_Timing_EN":
					voiceAttrs[v.Name]++
				}
			}
		}
	}
	type kv struct {
		k string
		v int
	}
	var pv []kv
	for k, v := range partVoice {
		pv = append(pv, kv{k, v})
	}
	sort.Slice(pv, func(i, j int) bool { return pv[i].v > pv[j].v })
	var sd []kv
	for k, v := range sound {
		sd = append(sd, kv{k, v})
	}
	sort.Slice(sd, func(i, j int) bool { return sd[i].v > sd[j].v })
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_voice_audit.json"), map[string]any{
		"schema_version":            1,
		"part_voice":                pv,
		"sound_commands":            sd,
		"voice_attributes_nonempty": voiceAttrs,
		"notes": []string{
			"ATR1.PartVoice is present and is a semantic voice category (e.g. Thought1, Laughter, HubCallOut), not a clip filename.",
			"ATR1.Sound contains BGM/SE commands such as Play(BGM_...), not per-line voice clip identifiers.",
			"The declared Voice_Type/Voice_Timing/Voice_File_Name/VoicePlay_JP/VoicePlay_EN attributes are not stored in ATR1 in any bank.",
			"Sound/V_JP.bin and Sound/V_EN.bin use a non-text encoding whose record format is not proven; no voice-id-to-line binding is claimed.",
			"Therefore P01C does not promote a speaker from voice metadata. Audio remains a future evidence source, not a current proof.",
		},
	})
}

func writeNameLookup(cfg importer.Config, l *loaded) {
	out := &importer.P01CNameLookup{SchemaVersion: 1, Persons: map[string]string{}, Note: "Names are the authenticated JP identifiers from GameData/Person.bin (PID_<value>). No romanisation or official-EN display name is invented. Raw entity_id remains canonical on the record."}
	if l.ctx != nil && l.ctx.Entities != nil {
		out.PersonSHA256 = l.ctx.Entities.PersonSHA256
		out.ProfileSHA256 = l.ctx.Entities.ProfileSHA256
		for id := range l.ctx.Entities.PersonIDs {
			out.Persons[id] = id
		}
		for id := range l.ctx.Entities.ProfileIDs {
			out.ProfileIDs = append(out.ProfileIDs, id)
		}
	}
	sort.Strings(out.ProfileIDs)
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_name_lookup.json"), out)
}

func writeTranslatorView(cfg importer.Config, res *importer.P01CResult) {
	cand := map[string]importer.P01CSpeakerCandidate{}
	for _, c := range res.Candidates {
		cand[c.RecordID] = c
	}
	var b strings.Builder
	b.WriteString("record_id\tfile\tbank\tlabel\tentry_index\tfunction\tdialogue_role\tutterance_kind\tspeaker_view\tspeaker_id\tcandidate_id\tcandidate_reason\tspeaker_applicable\tjp_preview\n")
	for _, path := range sortedShardPaths(res.Shards) {
		var sh importer.Shard
		_ = json.Unmarshal(res.Shards[path], &sh)
		for _, r := range sh.Records {
			view := "NOT_APPLICABLE"
			if r.Context.SpeakerApplicable {
				switch r.Context.Speaker.State {
				case "CORROBORATED", "PROVEN_FROM_ATTRIBUTE", "PROVEN_FROM_CALLSITE":
					view = "CONFIRMED"
				case "CONFLICT":
					view = "CONFLICT"
				default:
					if _, ok := cand[r.RecordID]; ok {
						view = "CANDIDATE"
					} else {
						view = "UNKNOWN"
					}
				}
			}
			sid, cid, creason := "", "", ""
			if r.Context.Speaker.EntityID != nil {
				sid = *r.Context.Speaker.EntityID
			}
			if c, ok := cand[r.RecordID]; ok {
				cid = c.EntityID
				creason = c.Reason
			}
			jp := ""
			if r.Text.JP != nil {
				jp = oneLine(*r.Text.JP)
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%v\t%s\n",
				r.RecordID, path, r.Source.Bank, r.Source.Label, r.Source.EntryIndex, r.Function,
				r.Context.DialogueRole, r.Context.UtteranceKind, view, sid, cid, creason, r.Context.SpeakerApplicable, truncRunes(jp, 150))
		}
	}
	writeText(filepath.Join(cfg.EvidenceDir, "p01c_translator_view.tsv"), b.String())
}

func sortedShardPaths(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// p01cExample is one cited positive/negative case.
type p01cExample struct {
	Outcome     string `json:"outcome"`
	Case        string `json:"case"`
	RecordID    string `json:"record_id"`
	Bank        string `json:"bank"`
	Label       string `json:"label"`
	Index       int    `json:"entry_index"`
	Function    string `json:"function"`
	Role        string `json:"dialogue_role"`
	Kind        string `json:"utterance_kind"`
	Speaker     string `json:"speaker_state"`
	EntityID    string `json:"entity_id"`
	PID         string `json:"source_pid"`
	MPID        string `json:"source_mpid"`
	EventDigest string `json:"event_digest"`
	JPExcerpt   string `json:"jp_excerpt"`
	Callsite    string `json:"callsite"`
	Rationale   string `json:"rationale"`
}

func writeExamples(cfg importer.Config, l *loaded, res *importer.P01CResult) {
	idx := buildRecordIndex(res, l.corp)
	var out []p01cExample
	add := func(outcome, name, bank, label string, i int, rationale string) {
		r, ok := idx.find(bank, label, i)
		if !ok {
			out = append(out, p01cExample{Outcome: outcome, Case: name, Bank: bank, Label: label, Index: i, Rationale: "SOURCE RECORD NOT FOUND"})
			return
		}
		pid, mpid, event := "", "", ""
		if b, ok := idx.Bank[bank]; ok && b.JP != nil && i < len(b.JP.Attrs) {
			for _, v := range b.JP.Attrs[i] {
				switch v.Name {
				case "PID":
					pid = strings.TrimSpace(v.String)
				case "MPID":
					mpid = strings.TrimSpace(v.String)
				case "Event":
					event = oneLine(v.String)
				}
			}
		}
		callsite := ""
		if l.corp.Talk != nil {
			if kind, n, flag, ok := l.corp.Talk.Lookup(label); ok {
				callsite = fmt.Sprintf("TalkMsg:%s#%d sec=%d", kind, n, flag)
			}
		}
		if l.ctx != nil && l.ctx.Scripts != nil {
			if paths := l.ctx.Scripts.DistinctPaths(label); len(paths) > 0 {
				if callsite != "" {
					callsite += "; "
				}
				callsite += "script:" + strings.Join(paths, ",")
			}
		}
		jp := ""
		if r.Text.JP != nil {
			jp = oneLine(*r.Text.JP)
		}
		sid := ""
		if r.Context.Speaker.EntityID != nil {
			sid = *r.Context.Speaker.EntityID
		}
		out = append(out, p01cExample{
			Outcome: outcome, Case: name, RecordID: r.RecordID, Bank: bank, Label: label, Index: i,
			Function: r.Function, Role: r.Context.DialogueRole, Kind: r.Context.UtteranceKind,
			Speaker: r.Context.Speaker.State, EntityID: sid, PID: pid, MPID: mpid,
			EventDigest: truncRunes(event, 220), JPExcerpt: truncRunes(jp, 160), Callsite: callsite, Rationale: rationale,
		})
	}
	// Confirmed / promoted examples.
	add("SUCCESS", "support_sole_actor_motion", "SE_Ism_Naz_AS", "MID_SE_Ism_Naz_A_008", 8, "PID ナジャ; Event performs キャラモーション再生(PID_ナジャ); line is Naja's reply in the Ishmael/Naja support.")
	add("SUCCESS", "support_thought_pid", "SE_Ism_Naz_AS", "MID_SE_Ism_Naz_A_029", 29, "PID イシュマール; the parenthetical line is Ishmael's inner thought and the Event also performs a motion on the same PID.")
	add("SUCCESS", "story_pid_with_motion", "3_ISM_c02", "MID_EV30_004_m01_002", 13, "PID コウカ; Event performs a motion on コウカ; the line is Kouka's explanation.")
	add("SUCCESS", "staged_listener_counterexample", "1_DIT_c02", "MID_EV12_005_m01_015", 110, "PID ディートリヒ while the Event only removes エスメラルダ's equipment and points the camera at her; the text ('これは……ブレーダッドの紋章？') is Dietrich examining the emblem. Staging actor is the listener, not the speaker.")
	add("SUCCESS", "staged_listener_counterexample_2", "0_ISM_c02", "MID_EV00_008_m01_016", 41, "PID コウカ while the Event animates トロイア; the text introduces トロイア in third person ('トロイア様だ。この方も…'), so the speaking actor cannot be the staged actor.")
	add("SUCCESS", "staged_listener_counterexample_3", "1_BEL_c09", "MID_EV15_057_m01_017", 165, "PID シンラ while the Event stages キリーク; the line is Shinra's threat to Kirik, not Kirik's own line.")
	add("SUCCESS", "camera_on_other_actor", "1_DIT_c02", "MID_EV12_005_m01_008", 103, "PID エスメラルダ; the only Event command is a camera on ディートリヒ, yet the line is Esmeralda addressing him.")
	add("SUCCESS", "tutorial_dialogue", "0_ISM_Battle", "MID_SC00_00_m01_001", 1, "PID ゼノン; in-battle tutorial line spoken by Zenon.")
	add("SUCCESS", "branch_reply", "0_ISM_Battle", "MID_SC00_00_m01_008", 8, "<ACA> branch reply; PID ゼノン; the authored branch marker keeps the responder identity.")
	add("SUCCESS", "ui_reaction_line", "Gmap_CaptureAnimal", "MID_Gmap_CaptureAnimalFailed_Jun_001", 6, "PID ジュラ; a one-line character reaction displayed by the map system, not a menu label.")
	add("SUCCESS", "support_named_person", "SE_Ism_Naz_AS", "MID_SE_Ism_Naz_A_018", 18, "PID ナジャ; support line carries a named person ID and matches the authored turn.")
	// Refused / ambiguous examples.
	add("REFUSED", "choice_option_without_pid", "3_ISM_c02", "MID_EV30_004_m01_012", 24, "Authored <NCA> choice option. No PID and no Event actor; a choice is not protagonist speech and is NOT_APPLICABLE for speaker.")
	add("REFUSED", "choice_option_with_interlocutor_pid", "Scout", "MID_LGV_Scout_Negotiation_SP_Question01_Yan_m01_003", 847, "Authored <ICA> choice option carrying PID ヨーカイ. Counterexample: the PID is the dialogue owner/interlocutor, not the chooser. Never promoted.")
	add("REFUSED", "system_tutorial_message", "0_ISM_Battle", "MID_SC00_00_e04_001", 28, "Authored <DLG> tutorial message about cursor movement. SYSTEM, not a character utterance.")
	add("REFUSED", "talk_header_marker", "0_ISM_Battle", "MID_SC00_00_m01", 0, "Authored <TH> marker only; no visible text. CONTROL, not a speaker slot.")
	add("REFUSED", "event_dialogue_header", "0_ISM_c01", "MID_EV00_000_m01", 0, "Authored <EDH> marker only. CONTROL.")
	add("REFUSED", "narration_after_scene_header", "1_KAI_c02", "MID_NR11_00_m01", 142, "Authored <SH> scene header followed by a validated narration block; all records in the block have no PID. NARRATION.")
	add("REFUSED", "narration_line", "1_LED_c01", "MID_NR14_00_m01_001", 1, "Record inside the <SH> narration block; visible prose, no PID, no Event actor. NARRATION, not a character utterance.")
	add("REFUSED", "unnamed_balloon_line", "LG_1LED_c07", "MID_LGA_1LED_c07_B01_m01", 43, "Authored <BLN> balloon line with no PID and only a portrait MPID. Spoken form, but the speaker is genuinely UNKNOWN; MPID is not promoted.")
	add("REFUSED", "movie_crowd_adlib_placeholder", "MV11_08", "MID_MV11_08_m005", 4, "Movie subtitle placeholder '（アドリブ）' with no PID; a genuine UNKNOWN speaker, not a narration claim.")
	add("REFUSED", "caption_card", "1_DIT_c06", "MID_EV12_028_m01_013", 47, "Authored <TLP> telop/caption card listing match information. NARRATION/CAPTION, not spoken dialogue.")
	add("REFUSED", "empty_physical_slot", "3_ISM_c02", "MID_EV30_004_m01_005", 16, "Physical slot with empty JP text. EMPTY, not 'unused' and not a speaker record.")
	add("REFUSED", "ambiguous_no_pid_story_line", "1_DIT_Battle", "MID_SC12_00_m05_001", 12, "Story/battle line with no PID and no Event actor. The role may be narration, a mob line or an unattributed character line, but the speaker is not proven, so the record stays UNKNOWN rather than being guessed from the previous speaker.")
	add("REFUSED", "ambiguous_mob_line", "SideStory_Talimun", "MID_SCXX_05_02_m01_009", 45, "Spoken-form treasure line with no PID and no Event actor. Likely a mob/bandit, but identity is not proven; stays UNKNOWN.")
	add("REFUSED", "ui_label_start_battle", "System_Dungeon", "MID_Dungeon_Start", 0, "UI label 'START' in a system bank. UI, not dialogue.")
	add("REFUSED", "unclassified_sup_ui", "SUP_11_Kai", "MID_SUP_1KAI_c01_LG11_001_m01", 0, "Support-menu action label in an unclassified SUP bank. UI label, not a spoken line.")
	add("REFUSED", "unknown_consumer_label", "System_Bmap", "MID_RewindMsg_BattleStart", 103, "Short label in an unclassified system bank with no proven consumer role. UNKNOWN role, deliberately not called unused.")
	add("REFUSED", "mpid_only_portrait", "1_ANH_c07", "MID_EV18_037_m01_026", 92, "PID empty, MPID=Orpheus. The portrait/model id is not promoted; the line stays UNKNOWN even though the text names Orpheus.")
	add("REFUSED", "ui_item_help_line", "Item", "MIID_H_WoodSword1", 1, "Item help text. UI, not dialogue.")
	add("SUCCESS", "narrative_style_line_with_pid", "0_ISM_c01", "MID_EV00_000_m01_001", 1, "The JP prose reads like narration, but ATR1.PID=フォトナ and the Event stages Photona; the line is her observation, so the attribute resolves an otherwise ambiguous narrative-style line.")
	add("SUCCESS", "support_branch_reply", "SE_Ism_Naz_AS", "MID_SE_Ism_Naz_A_004", 4, "<ACA> post-choice reply in the Ishmael/Naja support; PID ナジャ and Event motion on her. Branch replies keep the responder PID.")
	add("SUCCESS", "movie_subtitle_pid", "MV11_04", "MID_MV11_04_m003", 3, "Movie subtitle line with PID カイ; the subtitle carries the same person identifier as dialogue, so the subtitle speaker is confirmed.")
	add("SUCCESS", "system_voice_line", "Stage_Watch", "MID_SV_StageWatch_Ism_m01_01", 0, "MID_SV system-voice line with PID イシュマール; the text is a character utterance, not a voice-clip filename.")
	add("SUCCESS", "levelup_quote", "Levelup", "MID_LevelUp_Ism0M_00", 0, "Level-up quote with PID イシュマール; the record is a character utterance even though it is displayed by the level-up widget.")
	add("SUCCESS", "death_quote", "Die", "MID_DIE_SCXX_00_Kai_001", 289, "Death quote with PID カイ; the line is Kai's dying speech.")
	writeJSON(filepath.Join(cfg.EvidenceDir, "p01c_examples.json"), map[string]any{
		"schema_version": 1,
		"examples":       out,
		"note":           "Every example cites the physical record id, authored label/index, source fields and callsite evidence. REFUSED means the speaker is not promoted, not that the text is unused.",
	})
}

func writeCSV(cfg importer.Config, res *importer.P01CResult) {
	var b strings.Builder
	b.WriteString("function,records,spoken,narration,choice,system,ui,control,empty,unknown_role,confirmed,genuine_unknown,not_applicable\n")
	var keys []string
	for k := range res.Census.ByFunction {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := res.Census.ByFunction[k]
		fmt.Fprintf(&b, "%s,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d\n", k, v.Records, v.Spoken, v.Narration, v.Choice, v.System, v.UI, v.Control, v.Empty, v.Unknown,
			v.SpeakerCorroborated+v.SpeakerAttribute, v.SpeakerUnknown, v.Records-v.Spoken)
	}
	writeText(filepath.Join(cfg.EvidenceDir, "p01c_census_by_function.csv"), b.String())
	var bb strings.Builder
	bb.WriteString("bank,function,records,spoken,narration,choice,system,ui,control,empty,unknown_role,confirmed,genuine_unknown,not_applicable\n")
	var banks []string
	for k := range res.Census.ByBank {
		banks = append(banks, k)
	}
	sort.Strings(banks)
	funcByBank := map[string]string{}
	for _, br := range res.BankReports {
		funcByBank[br.Bank] = br.Category
	}
	for _, k := range banks {
		v := res.Census.ByBank[k]
		fmt.Fprintf(&bb, "%s,%s,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d\n", k, funcByBank[k], v.Records, v.Spoken, v.Narration, v.Choice, v.System, v.UI, v.Control, v.Empty, v.Unknown,
			v.SpeakerCorroborated+v.SpeakerAttribute, v.SpeakerUnknown, v.Records-v.Spoken)
	}
	writeText(filepath.Join(cfg.EvidenceDir, "p01c_census_by_bank.csv"), bb.String())
}

func p01cSourceToShard(res *importer.P01CResult) map[string]any {
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

func auditSummaryText(res *importer.P01CResult, audit *importer.P01CPromotionAudit) string {
	var b strings.Builder
	c := res.Census
	fmt.Fprintf(&b, "records=%d shards=%d\n", res.Records, len(res.Shards))
	fmt.Fprintf(&b, "roles: spoken=%d narration=%d choice=%d system=%d ui=%d control=%d empty=%d unknown=%d\n",
		c.Total.Spoken, c.Total.Narration, c.Total.Choice, c.Total.System, c.Total.UI, c.Total.Control, c.Total.Empty, c.Total.Unknown)
	fmt.Fprintf(&b, "dialogue_denominator=%d confirmed=%d (corroborated=%d attribute=%d) candidates=%d choice_owner_candidates=%d genuine_unknown=%d not_applicable=%d conflicts=%d\n",
		c.DialogueDenominator, c.Confirmed, c.Total.SpeakerCorroborated, c.Total.SpeakerAttribute, c.Candidate, c.ChoiceOwnerCandidates, c.GenuineUnknown, c.NotApplicable, c.Total.SpeakerConflict)
	fmt.Fprintf(&b, "promotion audit: pre=%d false_positive_role=%d kept_corroborated=%d downgraded=%d evidence=%v\n",
		audit.PrePromoted, audit.FalsePositive, audit.KeptCorroborated, audit.DowngradedToAttribute, audit.ByEvidence)
	var keys []string
	for k := range c.ByFunction {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(&b, "%-12s %7s %7s %7s %7s %7s %7s %7s %7s %7s %7s %7s\n", "function", "recs", "spoken", "narr", "choice", "system", "ui", "control", "empty", "unk-role", "conf", "unk-sp")
	for _, k := range keys {
		v := c.ByFunction[k]
		fmt.Fprintf(&b, "%-12s %7d %7d %7d %7d %7d %7d %7d %7d %7d %7d %7d\n", k, v.Records, v.Spoken, v.Narration, v.Choice, v.System, v.UI, v.Control, v.Empty, v.Unknown,
			v.SpeakerCorroborated+v.SpeakerAttribute, v.SpeakerUnknown)
	}
	return b.String()
}

func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", " / ")
}
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
