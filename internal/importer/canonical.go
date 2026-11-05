package importer

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Shard is one canonical output file.
type Shard struct {
	SchemaVersion int      `json:"schema_version"`
	Records       []Record `json:"records"`
}

// Source is the auditable physical identity of a record.
type Source struct {
	Platform               string  `json:"platform"`
	Bank                   string  `json:"bank"`
	JPAsset                *string `json:"jp_asset"`
	OfficialENAsset        *string `json:"official_en_asset"`
	Label                  string  `json:"label"`
	EntryIndex             int     `json:"entry_index"`
	JPSourceSHA256         *string `json:"jp_source_sha256"`
	OfficialENSourceSHA256 *string `json:"official_en_source_sha256"`
}

// Evidence is one machine-readable source reference.
type Evidence struct {
	Source string `json:"source"`
	Field  string `json:"field,omitempty"`
	Value  string `json:"value,omitempty"`
	Note   string `json:"note,omitempty"`
}

// EntityRef is a speaker/addressee context value.
type EntityRef struct {
	State    string     `json:"state"`
	EntityID *string    `json:"entity_id"`
	Evidence []Evidence `json:"evidence"`
}

// Context is the per-record provenance and editorial context.
type Context struct {
	Speaker           EntityRef `json:"speaker"`
	Addressee         EntityRef `json:"addressee"`
	SceneRef          *string   `json:"scene_ref"`
	NeighborRecordIDs []string  `json:"neighbor_record_ids"`
	UsageState        string    `json:"usage_state"`
	Alignment         string    `json:"alignment"`
	SourceSequence    int       `json:"source_sequence"`
	LabelAliasCount   int       `json:"label_alias_count"`
	// P01C message-role model. dialogue_role is the consumer-facing role of
	// the physical slot; utterance_kind refines it. speaker_applicable is true
	// only for SPOKEN records, so a NOT_APPLICABLE slot is never mistaken for
	// a genuine unknown speaker.
	DialogueRole      string  `json:"dialogue_role"`
	UtteranceKind     string  `json:"utterance_kind"`
	SpeakerApplicable bool    `json:"speaker_applicable"`
	Marker            *string `json:"marker"`
	BranchTag         *string `json:"branch_tag"`
	BranchOption      *string `json:"branch_option"`
	BranchPhase       *string `json:"branch_phase"`
	AuthoredGroup     *string `json:"authored_group"`
}

// Text holds the immutable source strings and the active canonical text.
type Text struct {
	JP         *string `json:"jp"`
	OfficialEN *string `json:"official_en"`
	RealEN     *string `json:"real_en"`
}

// Review is the public editorial status.
type Review struct {
	Status string `json:"status"`
}

// Record is one canonical physical message slot.
type Record struct {
	RecordID string  `json:"record_id"`
	Source   Source  `json:"source"`
	Context  Context `json:"context"`
	Function string  `json:"function"`
	Text     Text    `json:"text"`
	Review   Review  `json:"review"`
}

// IDRegistry rejects duplicate record ids.
type IDRegistry struct {
	Seen map[string]string
}

func NewIDRegistry() *IDRegistry {
	return &IDRegistry{Seen: map[string]string{}}
}

func (r *IDRegistry) Add(id, where string) error {
	if old, ok := r.Seen[id]; ok {
		return fmt.Errorf("duplicate record_id %q at %s (already %s)", id, where, old)
	}
	r.Seen[id] = where
	return nil
}

// BankBuildReport summarizes one bank's records.
type BankBuildReport struct {
	Bank                 string `json:"bank"`
	ShardPath            string `json:"shard_path"`
	JPRecords            int    `json:"jp_records"`
	ENRecords            int    `json:"en_records"`
	Records              int    `json:"records"`
	Aligned              int    `json:"aligned"`
	JPOnly               int    `json:"jp_only"`
	ENOnly               int    `json:"en_only"`
	BothEmpty            int    `json:"both_empty"`
	JPEmpty              int    `json:"jp_empty"`
	ENEmpty              int    `json:"en_empty"`
	LabelCollisionsJP    int    `json:"label_collisions_jp"`
	LabelCollisionsEN    int    `json:"label_collisions_en"`
	BadBucketLabelsJP    int    `json:"bad_bucket_labels_jp"`
	BadBucketLabelsEN    int    `json:"bad_bucket_labels_en"`
	UsageAssetReferenced int    `json:"usage_asset_referenced"`
	UsageUnverified      int    `json:"usage_unverified_candidate"`
	SpeakerCandidate     int    `json:"speaker_candidate_unverified"`
	SpeakerUnknown       int    `json:"speaker_unknown"`
	JPTextSHA256         string `json:"jp_text_sha256"`
	ENTextSHA256         string `json:"en_text_sha256"`
	JPSourceSHA256       string `json:"jp_source_sha256"`
	ENSourceSHA256       string `json:"en_source_sha256"`
}

type sideRec struct {
	side    *Side
	index   int
	label   string
	aliases int
}

// BuildBankRecords creates one canonical record per physical slot. Alignment is
// by exact label+index only; positional fallback is never used.
func BuildBankRecords(bank Bank, talk *TalkMsgRef, ids *IDRegistry) ([]Record, BankBuildReport, []SpeakerCandidate, []PIDObservation, error) {
	rep := BankBuildReport{Bank: bank.Name, ShardPath: "unclassified/" + bank.Name + ".json"}
	jpMap := map[string]sideRec{}
	enMap := map[string]sideRec{}
	for i := range bank.JP.Text {
		l := bank.JP.Labels[uint32(i)]
		key := l + "\x00" + strconv.Itoa(i)
		if _, exists := jpMap[key]; exists {
			return nil, rep, nil, nil, fmt.Errorf("JP bank %s duplicate qualified key %q", bank.Name, key)
		}
		jpMap[key] = sideRec{side: bank.JP, index: i, label: l, aliases: bank.JP.AliasCount[uint32(i)]}
	}
	for i := range bank.EN.Text {
		l := bank.EN.Labels[uint32(i)]
		key := l + "\x00" + strconv.Itoa(i)
		if _, exists := enMap[key]; exists {
			return nil, rep, nil, nil, fmt.Errorf("US EN bank %s duplicate qualified key %q", bank.Name, key)
		}
		enMap[key] = sideRec{side: bank.EN, index: i, label: l, aliases: bank.EN.AliasCount[uint32(i)]}
	}
	keys := make([]string, 0, len(jpMap)+len(enMap))
	for k := range jpMap {
		if _, ok := enMap[k]; ok {
			keys = append(keys, k)
		} else {
			keys = append(keys, k)
		}
	}
	for k := range enMap {
		if _, ok := jpMap[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		ai, al := splitKey(a)
		bi, bl := splitKey(b)
		if ai != bi {
			return ai < bi
		}
		return al < bl
	})
	records := make([]Record, 0, len(keys))
	candidates := make([]SpeakerCandidate, 0)
	pidOnly := make([]PIDObservation, 0)
	pidSeen := map[string]bool{}
	for _, key := range keys {
		jp, hasJP := jpMap[key]
		en, hasEN := enMap[key]
		index, label, aliases := 0, "", 1
		if hasJP {
			index, label, aliases = jp.index, jp.label, jp.aliases
		} else {
			index, label, aliases = en.index, en.label, en.aliases
		}
		if hasJP && hasEN {
			if jp.aliases > aliases {
				aliases = jp.aliases
			}
			if en.aliases > aliases {
				aliases = en.aliases
			}
		}
		rid := makeRecordID(bank.Name, label, index, aliases)
		if err := ids.Add(rid, bank.Name+"#"+strconv.Itoa(index)); err != nil {
			return nil, rep, nil, nil, err
		}
		alignment := "ALIGNED"
		switch {
		case hasJP && !hasEN:
			alignment = "JP_ONLY"
			rep.JPOnly++
		case !hasJP && hasEN:
			alignment = "EN_ONLY"
			rep.ENOnly++
		default:
			rep.Aligned++
		}
		rec := Record{
			RecordID: rid,
			Source: Source{
				Platform:   "switch2",
				Bank:       bank.Name,
				Label:      label,
				EntryIndex: index,
			},
			Context: Context{
				Speaker:           EntityRef{State: "UNKNOWN", EntityID: nil, Evidence: []Evidence{}},
				Addressee:         EntityRef{State: "UNKNOWN", EntityID: nil, Evidence: []Evidence{}},
				NeighborRecordIDs: []string{},
				Alignment:         alignment,
				SourceSequence:    index,
				LabelAliasCount:   aliases,
			},
			Function: "unclassified",
			Review:   Review{Status: "UNREVIEWED"},
		}
		if hasJP {
			asset := bank.JP.AssetPath
			hash := bank.JP.CompressedSHA
			rec.Source.JPAsset = &asset
			rec.Source.JPSourceSHA256 = &hash
			txt := bank.JP.Text[jp.index]
			rec.Text.JP = &txt
			rep.JPEmpty += boolToInt(bank.JP.File.TXT2.Strings[jp.index] == nil || len(bank.JP.File.TXT2.Strings[jp.index]) == 0)
		}
		if hasEN {
			asset := bank.EN.AssetPath
			hash := bank.EN.CompressedSHA
			rec.Source.OfficialENAsset = &asset
			rec.Source.OfficialENSourceSHA256 = &hash
			txt := bank.EN.Text[en.index]
			rec.Text.OfficialEN = &txt
			rep.ENEmpty += boolToInt(bank.EN.File.TXT2.Strings[en.index] == nil || len(bank.EN.File.TXT2.Strings[en.index]) == 0)
		}
		if hasJP && hasEN && isEmpty(bank.JP, jp.index) && isEmpty(bank.EN, en.index) {
			rep.BothEmpty++
		}
		// Usage evidence from the TalkMsg asset index only.
		if label != "" && talk != nil {
			if _, _, _, ok := talk.Lookup(label); ok {
				rec.Context.UsageState = "ASSET_REFERENCED"
				rep.UsageAssetReferenced++
			} else {
				rec.Context.UsageState = "UNVERIFIED_CANDIDATE"
				rep.UsageUnverified++
			}
		} else {
			rec.Context.UsageState = "UNVERIFIED_CANDIDATE"
			rep.UsageUnverified++
		}
		// Speaker remains UNKNOWN in the public record. Candidate MPID/PID
		// values are written only to the private speaker source map.
		if hasJP {
			if c, ok := candidateFromRow(bank.JP, jp.index, rid, bank.Name, label); ok {
				candidates = append(candidates, c)
			} else if p, ok := pidOnlyFromRow(bank.JP, jp.index, rid, bank.Name, label); ok && !pidSeen[rid] {
				pidOnly = append(pidOnly, p)
				pidSeen[rid] = true
			}
		}
		if hasEN {
			if c, ok := candidateFromRow(bank.EN, en.index, rid, bank.Name, label); ok {
				appendOrMergeCandidate(&candidates, c)
			} else if p, ok := pidOnlyFromRow(bank.EN, en.index, rid, bank.Name, label); ok && !pidSeen[rid] {
				pidOnly = append(pidOnly, p)
				pidSeen[rid] = true
			}
		}
		records = append(records, rec)
	}
	// Physical-order neighbours.
	for i := range records {
		prev, next := "", ""
		if i > 0 {
			prev = records[i-1].RecordID
		}
		if i+1 < len(records) {
			next = records[i+1].RecordID
		}
		if prev != "" {
			records[i].Context.NeighborRecordIDs = append(records[i].Context.NeighborRecordIDs, prev)
		}
		if next != "" {
			records[i].Context.NeighborRecordIDs = append(records[i].Context.NeighborRecordIDs, next)
		}
	}
	rep.Records = len(records)
	rep.SpeakerCandidate = len(candidates)
	rep.SpeakerUnknown = rep.Records - rep.SpeakerCandidate
	rep.JPRecords = len(bank.JP.Text)
	rep.ENRecords = len(bank.EN.Text)
	rep.LabelCollisionsJP = len(bank.JP.Collisions)
	rep.LabelCollisionsEN = len(bank.EN.Collisions)
	rep.BadBucketLabelsJP = bank.JP.BadBuckets
	rep.BadBucketLabelsEN = bank.EN.BadBuckets
	rep.JPSourceSHA256 = bank.JP.CompressedSHA
	rep.ENSourceSHA256 = bank.EN.CompressedSHA
	rep.JPTextSHA256 = textHash(bank.JP)
	rep.ENTextSHA256 = textHash(bank.EN)
	return records, rep, candidates, pidOnly, nil
}

// TalkMsgRef is the small interface the record builder needs.
type TalkMsgRef struct {
	Fn func(string) (kind string, index int, flag byte, ok bool)
}

func (t *TalkMsgRef) Lookup(label string) (string, int, byte, bool) {
	if t == nil || t.Fn == nil {
		return "", 0, 0, false
	}
	return t.Fn(label)
}

func makeRecordID(bank, label string, index, aliases int) string {
	l := label
	if l == "" {
		l = "_"
	}
	l = url.PathEscape(l)
	id := fmt.Sprintf("fefw:switch2:vUNKNOWN:msbt-v3:%s:%s#%d", bank, l, index)
	if aliases > 1 {
		id += fmt.Sprintf("~c%d", aliases)
	}
	return id
}

func splitKey(k string) (int, string) {
	for i := len(k) - 1; i >= 0; i-- {
		if k[i] == 0 {
			n, _ := strconv.Atoi(k[i+1:])
			return n, k[:i]
		}
	}
	return 0, k
}

func isEmpty(s *Side, i int) bool {
	return s == nil || i < 0 || i >= len(s.File.TXT2.Strings) || len(s.File.TXT2.Strings[i]) == 0
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func textHash(s *Side) string {
	if s == nil {
		return ""
	}
	// Hash the decoded tokenized strings in physical order. This is a
	// deterministic corpus fingerprint independent of JSON formatting.
	var b []byte
	for i := range s.Text {
		b = append(b, s.Text[i]...)
		b = append(b, 0)
	}
	return shaBytes(b)
}

// SpeakerCandidate is a private, source-backed but unproven speaker observation.
type SpeakerCandidate struct {
	RecordID string     `json:"record_id"`
	Bank     string     `json:"bank"`
	Label    string     `json:"label"`
	Index    int        `json:"entry_index"`
	State    string     `json:"state"`
	EntityID string     `json:"entity_id"`
	Evidence []Evidence `json:"evidence"`
	Note     string     `json:"note"`
}

func candidateFromRow(s *Side, index int, rid, bank, label string) (SpeakerCandidate, bool) {
	if s == nil || index < 0 || index >= len(s.Attrs) {
		return SpeakerCandidate{}, false
	}
	row := s.Attrs[index]
	mpid, hasMPID := FindAttr(row, "MPID")
	if !hasMPID || strings.TrimSpace(mpid.String) == "" || strings.EqualFold(strings.TrimSpace(mpid.String), "unknown") {
		return SpeakerCandidate{}, false
	}
	ev := []Evidence{}
	entity := mpid.String
	ev = append(ev, Evidence{Source: s.AssetPath, Field: "ATR1.MPID", Value: mpid.String, Note: "parsed attribute; speaker binding not proven"})
	if pid, ok := FindAttr(row, "PID"); ok && pid.String != "" {
		ev = append(ev, Evidence{Source: s.AssetPath, Field: "ATR1.PID", Value: pid.String, Note: "parsed attribute; speaker binding not proven"})
	}
	return SpeakerCandidate{
		RecordID: rid,
		Bank:     bank,
		Label:    label,
		Index:    index,
		State:    "CANDIDATE_UNVERIFIED",
		EntityID: entity,
		Evidence: ev,
		Note:     "MPID/PID parsed from the matching-locale MSBP/ATR1; the field is not proven to denote the speaking character, so canonical speaker remains UNKNOWN.",
	}, true
}

// PIDObservation is a private PID-only observation. It is not promoted to a
// speaker candidate because no MPID/voice binding was proven.
type PIDObservation struct {
	RecordID string `json:"record_id"`
	Bank     string `json:"bank"`
	Label    string `json:"label"`
	Index    int    `json:"entry_index"`
	PID      string `json:"pid"`
}

func pidOnlyFromRow(s *Side, index int, rid, bank, label string) (PIDObservation, bool) {
	if s == nil || index < 0 || index >= len(s.Attrs) {
		return PIDObservation{}, false
	}
	pid, ok := FindAttr(s.Attrs[index], "PID")
	if !ok || pid.String == "" {
		return PIDObservation{}, false
	}
	return PIDObservation{RecordID: rid, Bank: bank, Label: label, Index: index, PID: pid.String}, true
}

func appendOrMergeCandidate(list *[]SpeakerCandidate, c SpeakerCandidate) {
	for i := range *list {
		if (*list)[i].RecordID != c.RecordID {
			continue
		}
		// Same record: merge evidence, retain the first non-empty entity.
		if (*list)[i].EntityID == "" {
			(*list)[i].EntityID = c.EntityID
		}
		(*list)[i].Evidence = append((*list)[i].Evidence, c.Evidence...)
		return
	}
	*list = append(*list, c)
}
