package importer

import (
	"regexp"
	"sort"
	"strings"
)

// P01C extends P01B with an auditable message-role model and a revised
// speaker-evidence model. It is still offline, source-only analysis: no
// translation, no editorial decision and no runtime claim.
//
// Message roles
//
//	SPOKEN    an utterance attributed (or plausibly attributed) to a character
//	NARRATION narrative prose or a caption card, not a character utterance
//	CHOICE    a player-selectable option
//	SYSTEM    engine/tutorial/system message
//	UI        menu label, item/skill/quest/roster text
//	CONTROL   marker-only record with no visible text
//	EMPTY     no source text in the physical slot
//	UNKNOWN   visible text whose consumer role is not proven
//
// Utterance kinds refine SPOKEN/NARRATION/UI:
//
//	DIALOGUE_LINE, INNER_THOUGHT, BRANCH_DIALOGUE, CHOICE_OPTION,
//	NARRATION_LINE, CAPTION, SYSTEM_MESSAGE, UI_LABEL, UI_TEXT,
//	CONTROL_MARKER, EMPTY, UNKNOWN
const (
	RoleSpoken    = "SPOKEN"
	RoleNarration = "NARRATION"
	RoleChoice    = "CHOICE"
	RoleSystem    = "SYSTEM"
	RoleUI        = "UI"
	RoleControl   = "CONTROL"
	RoleEmpty     = "EMPTY"
	RoleUnknown   = "UNKNOWN"

	KindDialogue       = "DIALOGUE_LINE"
	KindInnerThought   = "INNER_THOUGHT"
	KindBranchDialogue = "BRANCH_DIALOGUE"
	KindChoiceOption   = "CHOICE_OPTION"
	KindNarrationLine  = "NARRATION_LINE"
	KindCaption        = "CAPTION"
	KindSystemMessage  = "SYSTEM_MESSAGE"
	KindUILabel        = "UI_LABEL"
	KindUIText         = "UI_TEXT"
	KindControlMarker  = "CONTROL_MARKER"
	KindEmpty          = "EMPTY"
	KindUnknown        = "UNKNOWN"
)

var p01cLeadMarkerRe = regexp.MustCompile(`^\s*<([A-Za-z0-9_]+)>`)

var p01cChoiceMarkers = map[string]bool{
	"NCA": true, "NCB": true, "NCC": true, "NCD": true,
	"ICA": true, "ICB": true, "ICC": true, "ICD": true,
}

var p01cBranchMarkers = map[string]bool{
	"ACA": true, "ACB": true, "ACC": true, "ACD": true,
}

var p01cControlMarkers = map[string]bool{
	"TH": true, "EDH": true, "SH": true, "RS": true, "TEDF": true,
}

var p01cDisplayMarkers = map[string]bool{
	"BLN": true, "RSDS": true,
}

var p01cDialogueFamilies = map[string]bool{
	"MID_LGV": true, "MID_LGA": true, "MID_SV": true, "MID_MV": true,
	"MID_DIE": true, "MID_RETREAT": true, "MID_GiftReaction": true,
	"MID_Likes": true, "MID_Strengths": true, "MID_LevelUp": true,
	"MID_Skill": true, "MID_Gmap": true, "MID_BD15": true, "MID_BD16": true,
	"MID_EV": true,
}

var p01cNarrationFamilies = map[string]bool{
	"MID_NR": true, "MID_END": true, "MID_LIB": true,
}

var p01cUIFamilies = map[string]bool{
	"MID_H": true, "MID_Sys": true, "MID_SysMenu": true, "MID_Menu": true,
	"MID_Tips": true, "MID_TUT": true, "MID_Stage": true, "MID_Clan": true,
	"MID_Map": true, "MID_Spot": true, "MID_BattleTitle": true,
	"MID_Config": true, "MID_Title": true, "MID_LG": true, "MID_SUP": true,
	"MID_Roster": true, "MID_Calendar": true, "MID_Guide": true,
	"MID_Articles": true, "MID_Arena": true, "MID_Shop": true,
	"MID_Meal": true, "MID_Scout": true, "MID_Lesson": true,
	"MID_Status": true, "MID_Option": true, "MID_Save": true,
	"MID_Transporter": true, "MID_LeyLine": true, "MID_Construction": true,
	"MID_DeliveryGuard": true, "MID_DeliveryAccident": true,
	"MID_GmapMenu": true, "MID_CmdMenu": true, "MID_CmdBtl": true,
	"MID_GmapMap": true, "MID_VandahlMenu": true, "MID_BanquetMenu": true,
	"MID_LegionDispatchMenu": true, "MID_WINRULE": true, "MID_LOSERULE": true,
	"MID_Rule": true, "MID_Facility": true,
	"MID_ItemMenu": true, "MID_StatusMenu": true, "MID_HubMenu": true,
	"MID_ProfileMenu": true, "MID_Library": true, "MID_HonorLv": true,
	"MID_DateTime": true, "MID_EventLocation": true, "MID_H_Spot": true,
	"MID_Skill": true, "MID_LevelUp": true,
}

// P01CClass is the deterministic role classification of one physical record.
type P01CClass struct {
	Role          string
	Kind          string
	Marker        string
	BranchTag     string
	BranchOption  string
	BranchPhase   string
	AuthoredGroup string
	Reason        string
	Evidence      []string
}

// LeadingMarker returns the authored <...> marker at the start of a string.
func LeadingMarker(text string) string {
	m := p01cLeadMarkerRe.FindStringSubmatch(text)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// AuthoredGroup strips a trailing numeric slot index from an authored label.
// It never invents a group: a label without a trailing numeric slot keeps its
// full value.
func AuthoredGroup(label string) string {
	if label == "" {
		return ""
	}
	i := strings.LastIndexByte(label, '_')
	if i <= 0 {
		return label
	}
	tail := label[i+1:]
	if tail == "" {
		return label
	}
	for _, r := range tail {
		if r < '0' || r > '9' {
			return label
		}
	}
	return label[:i]
}

func p01cFamily(label string) string {
	if label == "" {
		return ""
	}
	parts := strings.Split(label, "_")
	if len(parts) >= 2 {
		// Normalise the variable EV/SC/MV numeric families.
		switch {
		case strings.HasPrefix(parts[1], "EV"):
			return "MID_EV"
		case strings.HasPrefix(parts[1], "SC"):
			return "MID_SC"
		case strings.HasPrefix(parts[1], "MV"):
			return "MID_MV"
		case strings.HasPrefix(parts[1], "BD15"):
			return "MID_BD15"
		case strings.HasPrefix(parts[1], "BD16"):
			return "MID_BD16"
		}
		if parts[0] == "MID" {
			return "MID_" + parts[1]
		}
		return parts[0]
	}
	return label
}

// p01cNarrationBlock marks records that follow an authored <SH> scene-header
// marker until the next control/dialogue marker. P01C validated this rule on
// every <SH> block: all records are visible prose with no PID.
func p01cNarrationBlock(bank Bank) []bool {
	n := 0
	if bank.JP != nil {
		n = len(bank.JP.Text)
	}
	out := make([]bool, n)
	in := false
	for i := 0; i < n; i++ {
		t := strings.TrimSpace(bank.JP.Text[i])
		m := LeadingMarker(t)
		if m == "SH" {
			in = true
			continue
		}
		if p01cControlMarkers[m] && m != "SH" {
			in = false
		}
		if !in {
			continue
		}
		pid := ""
		if i < len(bank.JP.Attrs) {
			if v, ok := FindAttr(bank.JP.Attrs[i], "PID"); ok {
				pid = strings.TrimSpace(v.String)
			}
		}
		if pid != "" || p01cChoiceMarkers[m] || p01cBranchMarkers[m] || p01cDisplayMarkers[m] || m == "DLG" || m == "TLP" {
			// A dialogue/choice/caption record terminates the narration block.
			in = false
			continue
		}
		if VisibleText([]byte(bank.JP.Text[i])) {
			out[i] = true
		}
	}
	return out
}

func p01cTruthy(s string) bool { return strings.TrimSpace(s) != "" }

// ClassifyP01CRecord derives the role/kind of one physical record from source
// fields only.
func ClassifyP01CRecord(bank Bank, idx int, function string, partVoice string, narBlock bool) P01CClass {
	cl := P01CClass{Role: RoleUnknown, Kind: KindUnknown, Reason: "NO_SOURCE_ROLE_EVIDENCE"}
	if bank.JP == nil || idx < 0 || idx >= len(bank.JP.Text) {
		return cl
	}
	text := bank.JP.Text[idx]
	trimmed := strings.TrimSpace(text)
	marker := LeadingMarker(text)
	cl.Marker = marker
	cl.AuthoredGroup = AuthoredGroup(bank.JP.Labels[uint32(idx)])
	if marker != "" {
		cl.Evidence = append(cl.Evidence, "authored leading marker <"+marker+">")
	}
	pid := ""
	if idx < len(bank.JP.Attrs) {
		if v, ok := FindAttr(bank.JP.Attrs[idx], "PID"); ok {
			pid = NormalizePID(v.String)
		}
	}
	// Empty physical slot.
	if trimmed == "" {
		cl.Role, cl.Kind = RoleEmpty, KindEmpty
		cl.Reason = "JP_TEXT_EMPTY"
		return cl
	}
	visible := VisibleText([]byte(text))
	if !visible {
		cl.Role, cl.Kind = RoleControl, KindControlMarker
		cl.Reason = "CONTROL_MARKER_ONLY_NO_VISIBLE_TEXT"
		return cl
	}
	// Explicit authored markers.
	if p01cChoiceMarkers[marker] {
		cl.Role, cl.Kind = RoleChoice, KindChoiceOption
		cl.Reason = "AUTHORED_CHOICE_OPTION_MARKER_" + marker
		cl.BranchTag = marker
		cl.BranchOption = marker[len(marker)-1:]
		cl.BranchPhase = "PROMPT"
		if pid != "" {
			cl.Evidence = append(cl.Evidence, "ATR1.PID="+pid+" belongs to the dialogue owner, not the chooser; it is never promoted for a choice option")
		}
		return cl
	}
	if marker == "DLG" {
		cl.Role, cl.Kind = RoleSystem, KindSystemMessage
		cl.Reason = "AUTHORED_SYSTEM_MESSAGE_MARKER_DLG"
		return cl
	}
	if marker == "TLP" {
		cl.Role, cl.Kind = RoleNarration, KindCaption
		cl.Reason = "AUTHORED_CAPTION_MARKER_TLP"
		return cl
	}
	if p01cBranchMarkers[marker] {
		cl.Role, cl.Kind = RoleSpoken, KindBranchDialogue
		cl.Reason = "AUTHORED_POST_CHOICE_BRANCH_REPLY_" + marker
		cl.BranchTag = marker
		cl.BranchOption = marker[len(marker)-1:]
		cl.BranchPhase = "REPLY"
		addPartVoice(&cl, partVoice)
		return cl
	}
	if p01cDisplayMarkers[marker] {
		cl.Role, cl.Kind = RoleSpoken, KindDialogue
		cl.Reason = "AUTHORED_DISPLAY_MARKER_" + marker
		addPartVoice(&cl, partVoice)
		return cl
	}
	fam := p01cFamily(bank.JP.Labels[uint32(idx)])
	if narBlock || p01cNarrationFamilies[fam] {
		cl.Role, cl.Kind = RoleNarration, KindNarrationLine
		if narBlock {
			cl.Reason = "INSIDE_VALIDATED_SH_NARRATION_BLOCK"
		} else {
			cl.Reason = "AUTHORED_NARRATION_NAMESPACE_" + fam
		}
		return cl
	}
	// A person-attributed record is a character utterance regardless of the
	// shard family (e.g. level-up quotes and map reaction lines).
	if pid != "" {
		cl.Role, cl.Kind = RoleSpoken, KindDialogue
		cl.Reason = "ATR1_PID_PRESENT"
		cl.Evidence = append(cl.Evidence, "ATR1.PID="+pid)
		addPartVoice(&cl, partVoice)
		return cl
	}
	switch function {
	case "story", "supports":
		cl.Role, cl.Kind = RoleSpoken, KindDialogue
		cl.Reason = "DIALOGUE_SHARD_WITHOUT_ATTRIBUTED_PID"
		addPartVoice(&cl, partVoice)
		return cl
	case "items", "skills", "quests", "characters", "classes", "ui":
		cl.Role, cl.Kind = RoleUI, KindUIText
		if len([]rune(trimmed)) <= 24 {
			cl.Kind = KindUILabel
		}
		cl.Reason = "UI_SHARD_WITHOUT_PERSON_ATTRIBUTION"
		return cl
	case "other":
		if p01cDialogueFamilies[fam] {
			cl.Role, cl.Kind = RoleSpoken, KindDialogue
			cl.Reason = "AUTHORED_DIALOGUE_NAMESPACE_" + fam
			addPartVoice(&cl, partVoice)
			return cl
		}
		if p01cUIFamilies[fam] {
			cl.Role, cl.Kind = RoleUI, KindUIText
			if len([]rune(trimmed)) <= 24 {
				cl.Kind = KindUILabel
			}
			cl.Reason = "AUTHORED_UI_NAMESPACE_" + fam
			return cl
		}
		cl.Role, cl.Kind = RoleUnknown, KindUnknown
		cl.Reason = "OTHER_SHARD_NO_PROVEN_CONSUMER_ROLE"
		return cl
	default:
		if p01cDialogueFamilies[fam] {
			cl.Role, cl.Kind = RoleSpoken, KindDialogue
			cl.Reason = "AUTHORED_DIALOGUE_NAMESPACE_" + fam
			addPartVoice(&cl, partVoice)
			return cl
		}
		if p01cUIFamilies[fam] || p01cLooksLikeUILabel(bank.JP.Labels[uint32(idx)]) {
			cl.Role, cl.Kind = RoleUI, KindUIText
			if len([]rune(trimmed)) <= 24 {
				cl.Kind = KindUILabel
			}
			cl.Reason = "AUTHORED_UI_LABEL_PATTERN_IN_UNCLASSIFIED_SHARD"
			return cl
		}
		cl.Role, cl.Kind = RoleUnknown, KindUnknown
		cl.Reason = "UNCLASSIFIED_SHARD_NO_PROVEN_CONSUMER_ROLE"
		return cl
	}
}

// p01cUIFamilyPrefixes are authored identifier namespaces that belong to
// tables/UI rather than dialogue. They are used only for banks whose shard
// category is unclassified.
var p01cUIFamilyPrefixes = []string{
	"MIID_", "MJID_", "MSID_", "MPID_", "MATID_", "MCID_", "MSQID_", "MSSID_",
	"MLID_", "MTID_", "MRID_", "MID_SUP", "MID_Btl", "MID_Battle", "MID_H_",
}

var p01cUILabelMarkers = []string{
	"Menu_", "_Help", "Help_", "_Message", "_Confirm", "_Result", "_Title",
	"_Annotation", "_Attention", "_Header", "_Name", "_Select", "_Part",
	"_Caption", "_Telop", "_Footer", "_Body", "_Info",
}

func p01cLooksLikeUILabel(label string) bool {
	for _, p := range p01cUIFamilyPrefixes {
		if strings.HasPrefix(label, p) {
			return true
		}
	}
	for _, m := range p01cUILabelMarkers {
		if strings.Contains(label, m) {
			return true
		}
	}
	return false
}

func addPartVoice(cl *P01CClass, partVoice string) {
	switch partVoice {
	case "Thought1", "Thought2":
		cl.Kind = KindInnerThought
		cl.Reason += "+PARTVOICE_" + partVoice
		cl.Evidence = append(cl.Evidence, "ATR1.PartVoice="+partVoice)
	}
}

// ---------------------------------------------------------------------------
// Speaker evidence
// ---------------------------------------------------------------------------

// P01CActorEvidence describes the per-record Event actor relation. It is
// private analytical detail, not a runtime proof.
type P01CActorEvidence struct {
	EventPIDs      []string `json:"event_pids"`
	PIDInEvent     bool     `json:"pid_in_event"`
	SoleOtherPID   bool     `json:"sole_other_pid"`
	MultiOtherPID  bool     `json:"multi_other_pid"`
	PIDMotion      bool     `json:"pid_motion"`
	PIDLookOrPlace bool     `json:"pid_look_or_place"`
	BaseTalkStart  bool     `json:"base_talk_start_only"`
	NoEvent        bool     `json:"no_event"`
}

// AnalyzeEventActors extracts the PID set and the PID-specific command
// evidence from an ATR1.Event string.
func AnalyzeEventActors(pid, event string) P01CActorEvidence {
	var ev P01CActorEvidence
	if strings.TrimSpace(event) == "" {
		ev.NoEvent = true
		return ev
	}
	seen := map[string]bool{}
	for _, m := range p01bEventPIDRe.FindAllStringSubmatch(event, -1) {
		p := NormalizePID(m[1])
		if p != "" {
			seen[p] = true
		}
	}
	for p := range seen {
		ev.EventPIDs = append(ev.EventPIDs, p)
	}
	sort.Strings(ev.EventPIDs)
	if pid != "" {
		ev.PIDInEvent = seen[pid]
		ev.PIDMotion = strings.Contains(event, "キャラモーション再生( PID_"+pid) ||
			strings.Contains(event, "キャラモーション再生(PID_"+pid)
		ev.PIDLookOrPlace = strings.Contains(event, "キャラ視線(PID_"+pid) ||
			strings.Contains(event, "キャラ配置(PID_"+pid) ||
			strings.Contains(event, "キャラ再配置(PID_"+pid) ||
			strings.Contains(event, "キャラ配置調整(PID_"+pid)
	}
	ev.BaseTalkStart = strings.Contains(event, "拠点_会話開始")
	if !seen[pid] {
		switch len(seen) {
		case 1:
			ev.SoleOtherPID = true
		case 0:
			// no actor pid at all
		default:
			ev.MultiOtherPID = true
		}
	}
	return ev
}

// P01CSpeaker is the revised speaker resolution result.
type P01CSpeaker struct {
	State      string
	EntityID   string
	Reason     string
	Applicable bool
	Evidence   []Evidence
	Actors     P01CActorEvidence
}

// ResolveSpeakerP01C applies the P01C speaker contract:
//
//   - Only SPOKEN records are speaker-applicable; choice options, system
//     messages, captions, narration, UI and control markers are NOT_APPLICABLE.
//   - ATR1.PID is an authored Person.bin person identifier. P01C proves it is
//     the speaker for dialogue-shaped records on an identified subset, with
//     counterexamples: choice options carry the interlocutor's PID and are
//     therefore excluded; stage-direction Events can name a different single
//     actor (the listener) while ATR1.PID still names the speaker.
//   - When the same record's Event performs a motion/look/placement command on
//     the PID, the result is CORROBORATED; otherwise it is
//     PROVEN_FROM_ATTRIBUTE.
//   - Any JP/US EN PID disagreement is CONFLICT; PID not present in Person.bin
//     stays CANDIDATE (private); missing PID stays UNKNOWN.
func ResolveSpeakerP01C(role, kind, pid, enPID, mpid, event, partVoice string, personIDs map[string]bool, assetJP, assetEN string) P01CSpeaker {
	res := P01CSpeaker{State: "UNKNOWN", Evidence: []Evidence{}}
	res.Applicable = role == RoleSpoken
	if !res.Applicable {
		res.Reason = "NOT_APPLICABLE_" + role
		return res
	}
	pid = NormalizePID(pid)
	enPID = NormalizePID(enPID)
	mpid = NormalizePID(mpid)
	res.Actors = AnalyzeEventActors(pid, event)
	if pid == "" {
		if mpid != "" {
			res.Reason = "NO_PID_MPID_ONLY"
			res.Evidence = append(res.Evidence, Evidence{Source: assetJP, Field: "ATR1.MPID", Value: mpid, Note: "Profile.bin portrait/model identifier; never a speaker proof"})
			return res
		}
		res.Reason = "NO_PID"
		return res
	}
	if enPID != "" && enPID != pid {
		res.State = "CONFLICT"
		res.Reason = "JP_EN_PID_MISMATCH"
		res.Evidence = []Evidence{
			{Source: assetJP, Field: "ATR1.PID", Value: pid, Note: "JP person identifier"},
			{Source: assetEN, Field: "ATR1.PID", Value: enPID, Note: "US EN person identifier differs"},
		}
		return res
	}
	if personIDs != nil && !personIDs[pid] {
		res.Reason = "PID_NOT_IN_PERSON_TABLE"
		res.Evidence = append(res.Evidence, Evidence{Source: assetJP, Field: "ATR1.PID", Value: pid, Note: "value not found as PID_<value> in GameData/Person.bin"})
		return res
	}
	res.EntityID = pid
	res.Evidence = append(res.Evidence, Evidence{Source: assetJP, Field: "ATR1.PID", Value: pid, Note: "authored person identifier for this physical message slot; Person.bin contains PID_" + pid})
	if enPID != "" {
		res.Evidence = append(res.Evidence, Evidence{Source: assetEN, Field: "ATR1.PID", Value: enPID, Note: "US EN person identifier agrees"})
	}
	if kind == KindBranchDialogue {
		res.Evidence = append(res.Evidence, Evidence{Source: assetJP, Field: "text", Value: "branch reply", Note: "post-choice branch reply retains the responder PID"})
	}
	switch {
	case res.Actors.PIDMotion:
		res.State = "CORROBORATED"
		res.Reason = "PID_PERSON_TABLE_AND_EVENT_MOTION_ON_PID"
		res.Evidence = append(res.Evidence, Evidence{Source: assetJP, Field: "ATR1.Event", Value: "PID_" + pid, Note: "Event performs a motion command on the same person"})
	case res.Actors.PIDLookOrPlace:
		res.State = "PROVEN_FROM_ATTRIBUTE"
		res.Reason = "PID_PERSON_TABLE_EVENT_STAGES_PID"
		res.Evidence = append(res.Evidence, Evidence{Source: assetJP, Field: "ATR1.Event", Value: "PID_" + pid, Note: "Event stages the same person (look/placement) but does not perform a motion command on the line"})
	default:
		res.State = "PROVEN_FROM_ATTRIBUTE"
		res.Reason = "PID_PERSON_TABLE_DIALOGUE_LINE"
		if res.Actors.SoleOtherPID {
			res.Reason = "PID_PERSON_TABLE_STAGED_OTHER_ACTOR_IS_LISTENER"
			res.Evidence = append(res.Evidence, Evidence{Source: assetJP, Field: "ATR1.Event", Value: strings.Join(res.Actors.EventPIDs, ","), Note: "Event stages a different sole actor; audited counterexample class shows staging actor is the listener and ATR1.PID remains the speaker"})
		}
		if res.Actors.BaseTalkStart {
			res.Evidence = append(res.Evidence, Evidence{Source: assetJP, Field: "ATR1.Event", Value: "拠点_会話開始", Note: "hub conversation start; no PID-referencing performance command, so only the attribute is direct evidence"})
		}
	}
	return res
}

// P01CSpeakerCandidate is a private, non-promoted speaker observation.
type P01CSpeakerCandidate struct {
	RecordID  string     `json:"record_id"`
	Bank      string     `json:"bank"`
	Label     string     `json:"label"`
	Index     int        `json:"entry_index"`
	State     string     `json:"state"`
	EntityID  string     `json:"entity_id"`
	EntityNS  string     `json:"entity_namespace"`
	Role      string     `json:"dialogue_role"`
	Kind      string     `json:"utterance_kind"`
	Reason    string     `json:"reason"`
	Evidence  []Evidence `json:"evidence"`
	Promotion string     `json:"promotion"`
}

// BuildP01CCandidate creates a private candidate row from a non-promoted
// speaker resolution.
func BuildP01CCandidate(r Record, bank, label string, idx int, role, kind, pid, mpid, reason string, asset string) (P01CSpeakerCandidate, bool) {
	c := P01CSpeakerCandidate{RecordID: r.RecordID, Bank: bank, Label: label, Index: idx, State: "CANDIDATE_UNVERIFIED", Role: role, Kind: kind, Reason: reason, Promotion: "NOT_PROMOTED"}
	if pid != "" {
		c.EntityID = pid
		c.EntityNS = "PID"
		c.Evidence = []Evidence{{Source: asset, Field: "ATR1.PID", Value: pid, Note: reason}}
		return c, true
	}
	if mpid != "" {
		c.EntityID = mpid
		c.EntityNS = "MPID"
		c.Evidence = []Evidence{{Source: asset, Field: "ATR1.MPID", Value: mpid, Note: "Profile.bin portrait/model identifier"}}
		return c, true
	}
	return c, false
}

// ---------------------------------------------------------------------------
// Census
// ---------------------------------------------------------------------------

// P01CRoleCounts is a role/kind census cell.
type P01CRoleCounts struct {
	Records              int            `json:"records"`
	JPEmpty              int            `json:"jp_empty"`
	Spoken               int            `json:"spoken"`
	Narration            int            `json:"narration"`
	Choice               int            `json:"choice"`
	System               int            `json:"system"`
	UI                   int            `json:"ui"`
	Control              int            `json:"control"`
	Empty                int            `json:"empty"`
	Unknown              int            `json:"unknown"`
	ByKind               map[string]int `json:"by_kind"`
	SpeakerCorroborated  int            `json:"speaker_corroborated"`
	SpeakerAttribute     int            `json:"speaker_proven_from_attribute"`
	SpeakerConflict      int            `json:"speaker_conflict"`
	SpeakerUnknown       int            `json:"speaker_unknown"`
	SpeakerNotApplicable int            `json:"speaker_not_applicable"`
	Candidates           int            `json:"private_candidates"`
}

// P01CCensus is the full private role/speaker census.
type P01CCensus struct {
	SchemaVersion         int                       `json:"schema_version"`
	Total                 P01CRoleCounts            `json:"total"`
	ByFunction            map[string]P01CRoleCounts `json:"by_function"`
	ByBank                map[string]P01CRoleCounts `json:"by_bank"`
	ByRole                map[string]int            `json:"by_role"`
	ByKind                map[string]int            `json:"by_kind"`
	ByFunctionRole        map[string]map[string]int `json:"by_function_role"`
	ByBankRole            map[string]map[string]int `json:"by_bank_role"`
	DialogueDenominator   int                       `json:"dialogue_denominator"`
	Confirmed             int                       `json:"confirmed"`
	Candidate             int                       `json:"candidate"`
	ChoiceOwnerCandidates int                       `json:"choice_owner_candidates"`
	GenuineUnknown        int                       `json:"genuine_unknown"`
	NotApplicable         int                       `json:"not_applicable"`
	Note                  string                    `json:"note"`
}

func newRoleCounts() P01CRoleCounts {
	return P01CRoleCounts{ByKind: map[string]int{}}
}

func (c *P01CRoleCounts) add(role, kind, speaker string, jpEmpty bool) {
	c.Records++
	if jpEmpty {
		c.JPEmpty++
	}
	c.ByKind[kind]++
	switch role {
	case RoleSpoken:
		c.Spoken++
	case RoleNarration:
		c.Narration++
	case RoleChoice:
		c.Choice++
	case RoleSystem:
		c.System++
	case RoleUI:
		c.UI++
	case RoleControl:
		c.Control++
	case RoleEmpty:
		c.Empty++
	default:
		c.Unknown++
	}
	switch speaker {
	case "CORROBORATED":
		c.SpeakerCorroborated++
	case "PROVEN_FROM_ATTRIBUTE", "PROVEN_FROM_CALLSITE":
		c.SpeakerAttribute++
	case "CONFLICT":
		c.SpeakerConflict++
	case "UNKNOWN":
		if role == RoleSpoken {
			c.SpeakerUnknown++
		} else {
			c.SpeakerNotApplicable++
		}
	}
}

func (c *P01CCensus) finish() {
	c.ByRole = map[string]int{}
	c.ByKind = map[string]int{}
	c.DialogueDenominator = c.Total.Spoken
	c.Confirmed = c.Total.SpeakerCorroborated + c.Total.SpeakerAttribute
	c.Candidate = c.Total.Candidates - c.ChoiceOwnerCandidates
	c.GenuineUnknown = c.Total.SpeakerUnknown
	c.NotApplicable = c.Total.Records - c.Total.Spoken
	c.Note = "dialogue_denominator = records classified SPOKEN (dialogue lines, inner thoughts and post-choice branch replies). Confirmed = CORROBORATED + PROVEN_FROM_ATTRIBUTE public speaker. Candidate rows are private and never public. NOT_APPLICABLE = narration/choice/system/UI/control/empty, which is different from a genuine UNKNOWN speaker on a spoken line."
	for _, v := range c.ByFunction {
		for kind, n := range v.ByKind {
			c.ByKind[kind] += n
		}
		c.ByRole[RoleSpoken] += v.Spoken
		c.ByRole[RoleNarration] += v.Narration
		c.ByRole[RoleChoice] += v.Choice
		c.ByRole[RoleSystem] += v.System
		c.ByRole[RoleUI] += v.UI
		c.ByRole[RoleControl] += v.Control
		c.ByRole[RoleEmpty] += v.Empty
		c.ByRole[RoleUnknown] += v.Unknown
	}
}

func addRoleCount(m map[string]P01CRoleCounts, key, role, kind, speaker string, jpEmpty bool) {
	n, ok := m[key]
	if !ok {
		n = newRoleCounts()
	}
	n.add(role, kind, speaker, jpEmpty)
	m[key] = n
}

// P01CNameLookup is a private, non-romanised person-name lookup built only from
// authenticated Person.bin identifier strings.
type P01CNameLookup struct {
	SchemaVersion int               `json:"schema_version"`
	PersonSHA256  string            `json:"person_sha256"`
	ProfileSHA256 string            `json:"profile_sha256"`
	Persons       map[string]string `json:"persons"`
	ProfileIDs    []string          `json:"profile_ids"`
	Note          string            `json:"note"`
}
