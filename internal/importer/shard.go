package importer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Manifest is the universal release-builder-compatible translations manifest.
type Manifest struct {
	SchemaVersion int            `json:"schema_version"`
	Counts        ManifestCounts `json:"counts"`
	Files         []ManifestFile `json:"files"`
}

// ManifestCounts mirrors the counts object produced by the release builder.
type ManifestCounts struct {
	Files             int            `json:"files"`
	Records           int            `json:"records"`
	JP                int            `json:"jp"`
	OfficialEN        int            `json:"official_en"`
	RealEN            int            `json:"real_en"`
	MissingJP         int            `json:"missing_jp"`
	MissingOfficialEN int            `json:"missing_official_en"`
	ByType            map[string]int `json:"by_type"`
	ByReviewStatus    map[string]int `json:"by_review_status"`
}

// ManifestFile is one shard entry.
type ManifestFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Records int    `json:"records"`
	Type    string `json:"type"`
}

// ImportResult is the complete in-memory result of an import build.
type ImportResult struct {
	Shards          map[string][]byte
	Manifest        *Manifest
	ManifestBytes   []byte
	BankReports     []BankBuildReport
	Candidates      []SpeakerCandidate
	CandidateByBank map[string]int
	PIDOnly         []PIDObservation
	PIDOnlyByBank   map[string]int
	Totals          ImportTotals
}

// ImportTotals is the corpus-level census.
type ImportTotals struct {
	Banks                 int `json:"banks"`
	Records               int `json:"records"`
	JPRecords             int `json:"jp_records"`
	ENRecords             int `json:"en_records"`
	Aligned               int `json:"aligned"`
	JPOnly                int `json:"jp_only"`
	ENOnly                int `json:"en_only"`
	BothEmpty             int `json:"both_empty"`
	JPEmpty               int `json:"jp_empty"`
	ENEmpty               int `json:"en_empty"`
	UsageAssetReferenced  int `json:"usage_asset_referenced"`
	UsageUnverified       int `json:"usage_unverified_candidate"`
	SpeakerCandidate      int `json:"speaker_candidate_unverified"`
	SpeakerUnknown        int `json:"speaker_unknown"`
	LabelCollisions       int `json:"label_collisions"`
	LabelSetMismatchBanks int `json:"label_set_mismatch_banks"`
	BadBucketLabels       int `json:"bad_bucket_labels"`
}

// Build builds all deterministic shard bytes and the manifest without writing.
func Build(corp *Corpus) (*ImportResult, error) {
	res := &ImportResult{
		Shards:          map[string][]byte{},
		CandidateByBank: map[string]int{},
		PIDOnlyByBank:   map[string]int{},
	}
	ids := NewIDRegistry()
	var talk *TalkMsgRef
	if corp.Talk != nil {
		talk = &TalkMsgRef{Fn: corp.Talk.Lookup}
	}
	for _, bank := range corp.Banks {
		records, rep, candidates, pidOnly, err := BuildBankRecords(bank, talk, ids)
		if err != nil {
			return nil, err
		}
		shard := Shard{SchemaVersion: 1, Records: records}
		b, err := marshalDeterministic(shard)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", bank.Name, err)
		}
		res.Shards[rep.ShardPath] = b
		res.BankReports = append(res.BankReports, rep)
		res.Candidates = append(res.Candidates, candidates...)
		res.CandidateByBank[bank.Name] = len(candidates)
		res.PIDOnly = append(res.PIDOnly, pidOnly...)
		res.PIDOnlyByBank[bank.Name] = len(pidOnly)
		res.Totals.Banks++
		res.Totals.Records += rep.Records
		res.Totals.JPRecords += rep.JPRecords
		res.Totals.ENRecords += rep.ENRecords
		res.Totals.Aligned += rep.Aligned
		res.Totals.JPOnly += rep.JPOnly
		res.Totals.ENOnly += rep.ENOnly
		res.Totals.BothEmpty += rep.BothEmpty
		res.Totals.JPEmpty += rep.JPEmpty
		res.Totals.ENEmpty += rep.ENEmpty
		res.Totals.UsageAssetReferenced += rep.UsageAssetReferenced
		res.Totals.UsageUnverified += rep.UsageUnverified
		res.Totals.SpeakerCandidate += rep.SpeakerCandidate
		res.Totals.SpeakerUnknown += rep.SpeakerUnknown
		res.Totals.LabelCollisions += rep.LabelCollisionsJP + rep.LabelCollisionsEN
		res.Totals.BadBucketLabels += rep.BadBucketLabelsJP + rep.BadBucketLabelsEN
		if labelSetMismatch(bank) {
			res.Totals.LabelSetMismatchBanks++
		}
	}
	res.Manifest = buildManifest(res)
	b, err := marshalDeterministic(res.Manifest)
	if err != nil {
		return nil, err
	}
	res.ManifestBytes = b
	return res, nil
}

func labelSetMismatch(bank Bank) bool {
	jp := map[string]bool{}
	for _, l := range bank.JP.Labels {
		jp[l] = true
	}
	en := map[string]bool{}
	for _, l := range bank.EN.Labels {
		en[l] = true
	}
	if len(jp) != len(en) {
		return true
	}
	for l := range jp {
		if !en[l] {
			return true
		}
	}
	return false
}

func buildManifest(res *ImportResult) *Manifest {
	m := &Manifest{
		SchemaVersion: 1,
		Counts: ManifestCounts{
			ByType:         map[string]int{},
			ByReviewStatus: map[string]int{},
		},
	}
	paths := make([]string, 0, len(res.Shards))
	for p := range res.Shards {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		b := res.Shards[p]
		var shard Shard
		if err := json.Unmarshal(b, &shard); err != nil {
			panic("importer: internal shard JSON error: " + err.Error())
		}
		typ := "unclassified"
		if len(shard.Records) > 0 {
			typ = shard.Records[0].Function
		}
		m.Files = append(m.Files, ManifestFile{Path: p, SHA256: shaBytes(b), Records: len(shard.Records), Type: typ})
		m.Counts.Files++
		m.Counts.Records += len(shard.Records)
		for _, r := range shard.Records {
			if r.Text.JP != nil {
				m.Counts.JP++
			}
			if r.Text.OfficialEN != nil {
				m.Counts.OfficialEN++
			}
			if r.Text.RealEN != nil {
				m.Counts.RealEN++
			}
			m.Counts.ByType[r.Function]++
			m.Counts.ByReviewStatus[r.Review.Status]++
		}
	}
	m.Counts.MissingJP = m.Counts.Records - m.Counts.JP
	m.Counts.MissingOfficialEN = m.Counts.Records - m.Counts.OfficialEN
	return m
}

// marshalDeterministic marshals with two-space indentation, no HTML escaping
// and a trailing newline. Go sorts map keys, so output is stable.
func marshalDeterministic(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
