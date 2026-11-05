package importer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"fefw/internal/msbt"
)

// P01BBankReport is one source bank's post-enrichment/classification report.
type P01BBankReport struct {
	Bank           string            `json:"bank"`
	Records        int               `json:"records"`
	JPRecords      int               `json:"jp_records"`
	ENRecords      int               `json:"en_records"`
	ShardPaths     []string          `json:"shard_paths"`
	Sections       map[string]int    `json:"sections,omitempty"`
	Category       string            `json:"category"`
	Rule           string            `json:"rule"`
	Evidence       []string          `json:"evidence"`
	SpeakerStates  map[string]int    `json:"speaker_states"`
	UsageStates    map[string]int    `json:"usage_states"`
	JPSourceSHA256 string            `json:"jp_source_sha256"`
	ENSourceSHA256 string            `json:"en_source_sha256"`
	Extra          map[string]string `json:"extra,omitempty"`
}

// P01BResult is the complete enriched/classified build.
type P01BResult struct {
	Shards          map[string][]byte
	Manifest        *Manifest
	ManifestBytes   []byte
	BankReports     []P01BBankReport
	Classifications []Classification
	Coverage        *SpeakerCoverage
	Candidates      []SpeakerCandidate
	Addressees      []AddresseeCandidate
	Totals          ImportTotals
	Records         int
}

// SpeakerCoverage is the private speaker/addressee coverage census.
type SpeakerCoverage struct {
	SchemaVersion       int                       `json:"schema_version"`
	TotalRecords        int                       `json:"total_records"`
	PIDNonEmpty         int                       `json:"pid_non_empty"`
	MPIDNonEmpty        int                       `json:"mpid_non_empty"`
	PIDAndMPID          int                       `json:"pid_and_mpid"`
	MPIDOnly            int                       `json:"mpid_only"`
	PIDOnly             int                       `json:"pid_only"`
	Corroborated        int                       `json:"corroborated"`
	Unknown             int                       `json:"unknown"`
	Conflicts           int                       `json:"conflicts"`
	Reasons             map[string]int            `json:"reasons"`
	ByBank              map[string]map[string]int `json:"by_bank"`
	TopEntities         []map[string]any          `json:"top_entities"`
	JPENMismatches      int                       `json:"jp_en_mismatches"`
	AddresseeCandidates int                       `json:"addressee_candidates"`
	AddresseeEntities   []map[string]any          `json:"addressee_entities"`
	ByCategory          map[string]map[string]int `json:"by_category"`
	Note                string                    `json:"note"`
}

// BuildP01B builds enriched shards, classifications and a manifest without
// writing anything. It never changes text or review status.
func BuildP01B(corp *Corpus, ctx *P01BContext) (*P01BResult, error) {
	if corp == nil {
		return nil, fmt.Errorf("nil corpus")
	}
	res := &P01BResult{Shards: map[string][]byte{}, Coverage: &SpeakerCoverage{SchemaVersion: 1, Reasons: map[string]int{}, ByBank: map[string]map[string]int{}}}
	ids := NewIDRegistry()
	var talk *TalkMsgRef
	if corp.Talk != nil {
		talk = &TalkMsgRef{Fn: corp.Talk.Lookup}
	}
	for _, bank := range corp.Banks {
		records, _, _, _, err := BuildBankRecords(bank, talk, ids)
		if err != nil {
			return nil, err
		}
		if len(ctxSectionMap(ctx)) == 0 && ctx != nil {
			// still fine: section names fall back to numeric tokens
		}
		br := P01BBankReport{Bank: bank.Name, Records: len(records), Sections: map[string]int{}, SpeakerStates: map[string]int{}, UsageStates: map[string]int{}}
		if bank.JP != nil {
			br.JPRecords = len(bank.JP.Text)
			br.JPSourceSHA256 = bank.JP.CompressedSHA
		}
		if bank.EN != nil {
			br.ENRecords = len(bank.EN.Text)
			br.ENSourceSHA256 = bank.EN.CompressedSHA
		}
		for i := range records {
			r := &records[i]
			idx := r.Source.EntryIndex
			var jpAsset, enAsset string
			if bank.JP != nil {
				jpAsset = bank.JP.AssetPath
			}
			if bank.EN != nil {
				enAsset = bank.EN.AssetPath
			}
			pid, enPID, mpid, event, sec := "", "", "", "", uint8(0)
			if bank.JP != nil && idx < len(bank.JP.Attrs) {
				row := bank.JP.Attrs[idx]
				pid = attrString(row, "PID")
				mpid = attrString(row, "MPID")
				event = attrString(row, "Event")
				if v, ok := FindAttr(row, "Section"); ok {
					sec = uint8(v.Enum)
				}
			}
			if bank.EN != nil && idx < len(bank.EN.Attrs) {
				enPID = attrString(bank.EN.Attrs[idx], "PID")
			}
			visible := false
			if bank.JP != nil && idx < len(bank.JP.File.TXT2.Strings) {
				visible = VisibleText(bank.JP.File.TXT2.Strings[idx])
			}
			sp := ResolveSpeaker(pid, enPID, mpid, event, visible, ctxPersonIDs(ctx), jpAsset, enAsset)
			if sp.State == "CONFLICT" {
				res.Coverage.Conflicts++
				res.Coverage.JPENMismatches++
			}
			if sp.State == "CORROBORATED" {
				id := sp.EntityID
				r.Context.Speaker = EntityRef{State: "CORROBORATED", EntityID: &id, Evidence: sp.Evidence}
				res.Coverage.Corroborated++
			} else {
				r.Context.Speaker = EntityRef{State: "UNKNOWN", EntityID: nil, Evidence: []Evidence{}}
				res.Coverage.Unknown++
				res.Coverage.Reasons[sp.Reason]++
			}
			// Private candidate: PID is person-like, MPID is portrait/model.
			if sp.State != "CORROBORATED" {
				if p := NormalizePID(pid); p != "" {
					res.Candidates = append(res.Candidates, SpeakerCandidate{
						RecordID: r.RecordID, Bank: bank.Name, Label: r.Source.Label, Index: idx,
						State: "CANDIDATE_UNVERIFIED", EntityID: p,
						Evidence: []Evidence{{Source: jpAsset, Field: "ATR1.PID", Value: p, Note: sp.Reason}},
						Note:     "reason=" + sp.Reason + "; person identifier present but speaker binding not proven for this line",
					})
				} else if m := NormalizePID(mpid); m != "" {
					res.Candidates = append(res.Candidates, SpeakerCandidate{
						RecordID: r.RecordID, Bank: bank.Name, Label: r.Source.Label, Index: idx,
						State: "CANDIDATE_UNVERIFIED", EntityID: m,
						Evidence: []Evidence{{Source: jpAsset, Field: "ATR1.MPID", Value: m, Note: "Profile.bin portrait/model identifier; not a person ID"}},
						Note:     "reason=MPID_ONLY; MPID is a portrait/model identifier, never a proven speaker",
					})
				}
			}
			if p := NormalizePID(pid); p != "" {
				res.Coverage.PIDNonEmpty++
				if m := NormalizePID(mpid); m != "" {
					res.Coverage.PIDAndMPID++
				} else {
					res.Coverage.PIDOnly++
				}
				adds := ResolveAddresseeCandidates(r.RecordID, p, event, jpAsset)
				if len(adds) > 0 {
					res.Coverage.AddresseeCandidates++
					res.Addressees = append(res.Addressees, adds...)
				}
			}
			if NormalizePID(mpid) != "" {
				res.Coverage.MPIDNonEmpty++
				if NormalizePID(pid) == "" {
					res.Coverage.MPIDOnly++
				}
			}
			// Section evidence (private): validated against TalkMsg elsewhere.
			secName := ""
			if ctx != nil {
				secName = ctx.SectionNames[sec]
			}
			if secName == "" {
				secName = fmt.Sprintf("sec-%d", sec)
			}
			br.Sections[secName]++
			// Usage + exact callsite refs.
			usage := "UNVERIFIED_CANDIDATE"
			if talk != nil && r.Source.Label != "" {
				if _, _, _, ok := talk.Lookup(r.Source.Label); ok {
					usage = "ASSET_REFERENCED"
				}
			}
			var sceneRef *string
			if ctx != nil && ctx.Scripts != nil && r.Source.Label != "" {
				paths := ctx.Scripts.DistinctPaths(r.Source.Label)
				if len(paths) > 0 && usage != "ASSET_REFERENCED" {
					usage = "ASSET_REFERENCED"
				}
				if len(paths) == 1 {
					p := paths[0]
					sceneRef = &p
				}
			}
			r.Context.UsageState = usage
			r.Context.SceneRef = sceneRef
			br.SpeakerStates[r.Context.Speaker.State]++
			br.UsageStates[usage]++
			if res.Coverage.ByBank[bank.Name] == nil {
				res.Coverage.ByBank[bank.Name] = map[string]int{}
			}
			res.Coverage.ByBank[bank.Name]["records"]++
			res.Coverage.ByBank[bank.Name][strings.ToLower(r.Context.Speaker.State)]++
		}
		groups, cl := ClassifyBank(bank, records, ctx)
		if len(groups) == 0 {
			groups = []ShardGroup{{Path: "unclassified/" + safeName(bank.Name) + ".json", Function: "unclassified", Indices: rangeInts(len(records))}}
		}
		cl.ShardCount = len(groups)
		cl.RecordCount = len(records)
		res.Classifications = append(res.Classifications, cl)
		br.Category = cl.Category
		br.Rule = cl.Rule
		br.Evidence = cl.Evidence
		for _, g := range groups {
			if len(g.Indices) == 0 {
				continue
			}
			if _, exists := res.Shards[g.Path]; exists {
				return nil, fmt.Errorf("shard path collision: %s", g.Path)
			}
			recs := make([]Record, 0, len(g.Indices))
			for _, i := range g.Indices {
				r := records[i]
				r.Function = g.Function
				recs = append(recs, r)
			}
			sh := Shard{SchemaVersion: 1, Records: recs}
			b, err := marshalDeterministic(sh)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", bank.Name, g.Path, err)
			}
			res.Shards[g.Path] = b
			br.ShardPaths = append(br.ShardPaths, g.Path)
		}
		sort.Strings(br.ShardPaths)
		res.BankReports = append(res.BankReports, br)
		res.Totals.Banks++
		res.Totals.Records += len(records)
		res.Totals.JPRecords += br.JPRecords
		res.Totals.ENRecords += br.ENRecords
		res.Records += len(records)
	}
	var err error
	res.Manifest, res.ManifestBytes, err = BuildManifestFromShards(res.Shards)
	if err != nil {
		return nil, err
	}
	// Totals that do not depend on text attributes.
	for _, br := range res.BankReports {
		res.Totals.SpeakerCandidate += br.SpeakerStates["CANDIDATE_UNVERIFIED"]
		res.Totals.UsageAssetReferenced += br.UsageStates["ASSET_REFERENCED"]
		res.Totals.UsageUnverified += br.UsageStates["UNVERIFIED_CANDIDATE"]
	}
	res.Coverage.TotalRecords = res.Records
	res.Coverage.Unknown = res.Records - res.Coverage.Corroborated - res.Coverage.Conflicts
	res.Coverage.ByCategory = map[string]map[string]int{}
	for _, br := range res.BankReports {
		if res.Coverage.ByCategory[br.Category] == nil {
			res.Coverage.ByCategory[br.Category] = map[string]int{}
		}
		res.Coverage.ByCategory[br.Category]["banks"]++
		res.Coverage.ByCategory[br.Category]["records"] += br.Records
		for k, v := range br.SpeakerStates {
			res.Coverage.ByCategory[br.Category][strings.ToLower(k)] += v
		}
	}
	res.Coverage.Note = "Public speaker is CORROBORATED only when PID is a Person.bin person, JP/US EN agree, the record has visible text, and the Event names exactly that PID as the sole staged actor with a performance command. MPID alone is never promoted."
	res.Coverage.TopEntities = topCandidateEntities(res.Candidates, 40)
	res.Coverage.AddresseeEntities = topAddressees(res.Addressees, 40)
	return res, nil
}

func ctxPersonIDs(ctx *P01BContext) map[string]bool {
	if ctx == nil || ctx.Entities == nil {
		return nil
	}
	return ctx.Entities.PersonIDs
}
func ctxSectionMap(ctx *P01BContext) map[uint8]string {
	if ctx == nil {
		return nil
	}
	return ctx.SectionNames
}
func attrString(row []msbt.AttrValue, name string) string {
	if v, ok := FindAttr(row, name); ok {
		return v.String
	}
	return ""
}
func topCandidateEntities(c []SpeakerCandidate, n int) []map[string]any {
	m := map[string]int{}
	for _, x := range c {
		m[x.EntityID]++
	}
	type kv struct {
		k string
		v int
	}
	var a []kv
	for k, v := range m {
		a = append(a, kv{k, v})
	}
	sort.Slice(a, func(i, j int) bool {
		if a[i].v != a[j].v {
			return a[i].v > a[j].v
		}
		return a[i].k < a[j].k
	})
	if len(a) > n {
		a = a[:n]
	}
	out := make([]map[string]any, 0, len(a))
	for _, x := range a {
		out = append(out, map[string]any{"entity_id": x.k, "records": x.v})
	}
	return out
}
func topAddressees(c []AddresseeCandidate, n int) []map[string]any {
	m := map[string]int{}
	for _, x := range c {
		m[x.Target]++
	}
	type kv struct {
		k string
		v int
	}
	var a []kv
	for k, v := range m {
		a = append(a, kv{k, v})
	}
	sort.Slice(a, func(i, j int) bool {
		if a[i].v != a[j].v {
			return a[i].v > a[j].v
		}
		return a[i].k < a[j].k
	})
	if len(a) > n {
		a = a[:n]
	}
	out := make([]map[string]any, 0, len(a))
	for _, x := range a {
		out = append(out, map[string]any{"entity_id": x.k, "records": x.v})
	}
	return out
}

// BuildManifestFromShards computes the universal release manifest from shard
// bytes. The shard's type is the first record's function.
func BuildManifestFromShards(shards map[string][]byte) (*Manifest, []byte, error) {
	m := &Manifest{SchemaVersion: 1, Counts: ManifestCounts{ByType: map[string]int{}, ByReviewStatus: map[string]int{}}}
	paths := make([]string, 0, len(shards))
	for p := range shards {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		b := shards[p]
		var sh Shard
		if err := json.Unmarshal(b, &sh); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
		typ := "unclassified"
		if len(sh.Records) > 0 {
			typ = sh.Records[0].Function
		}
		m.Files = append(m.Files, ManifestFile{Path: p, SHA256: shaBytes(b), Records: len(sh.Records), Type: typ})
		m.Counts.Files++
		m.Counts.Records += len(sh.Records)
		for _, r := range sh.Records {
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
	b, err := marshalDeterministic(m)
	return m, b, err
}

// CategoryCounts summarizes shard/record counts by function.
func (r *P01BResult) CategoryCounts() map[string]map[string]int {
	out := map[string]map[string]int{}
	for _, br := range r.BankReports {
		if out[br.Category] == nil {
			out[br.Category] = map[string]int{"banks": 0, "records": 0, "shards": 0}
		}
		out[br.Category]["banks"]++
		out[br.Category]["records"] += br.Records
		out[br.Category]["shards"] += len(br.ShardPaths)
	}
	return out
}

// CategoryShardCounts summarizes output shards by top-level directory.
func (r *P01BResult) CategoryShardCounts() map[string]int {
	out := map[string]int{}
	for p := range r.Shards {
		top := p
		if i := strings.IndexByte(top, '/'); i >= 0 {
			top = top[:i]
		}
		out[top]++
	}
	return out
}
