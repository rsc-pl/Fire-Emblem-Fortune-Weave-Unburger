package importer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// P01CBankReport is one source bank's P01C role/speaker report.
type P01CBankReport struct {
	Bank           string         `json:"bank"`
	Records        int            `json:"records"`
	ShardPaths     []string       `json:"shard_paths"`
	Category       string         `json:"category"`
	RoleCounts     P01CRoleCounts `json:"role_counts"`
	SpeakerStates  map[string]int `json:"speaker_states"`
	Kinds          map[string]int `json:"utterance_kinds"`
	JPSourceSHA256 string         `json:"jp_source_sha256"`
	ENSourceSHA256 string         `json:"en_source_sha256"`
	Sections       map[string]int `json:"sections,omitempty"`
	Evidence       []string       `json:"evidence"`
}

// P01CResult is the complete P01C build.
type P01CResult struct {
	Shards          map[string][]byte
	Manifest        *Manifest
	ManifestBytes   []byte
	BankReports     []P01CBankReport
	Census          *P01CCensus
	Candidates      []P01CSpeakerCandidate
	Classifications []Classification
	Records         int
	Totals          ImportTotals
}

// BuildP01C builds P01C-enriched shards with the same classification and shard
// paths as P01B, so no record moves and no text changes.
func BuildP01C(corp *Corpus, ctx *P01BContext) (*P01CResult, error) {
	if corp == nil {
		return nil, fmt.Errorf("nil corpus")
	}
	res := &P01CResult{
		Shards: map[string][]byte{},
		Census: &P01CCensus{
			SchemaVersion:  1,
			Total:          newRoleCounts(),
			ByFunction:     map[string]P01CRoleCounts{},
			ByBank:         map[string]P01CRoleCounts{},
			ByFunctionRole: map[string]map[string]int{},
			ByBankRole:     map[string]map[string]int{},
		},
	}
	ids := NewIDRegistry()
	var talk *TalkMsgRef
	if corp.Talk != nil {
		talk = &TalkMsgRef{Fn: corp.Talk.Lookup}
	}
	personIDs := ctxPersonIDs(ctx)
	for _, bank := range corp.Banks {
		records, _, _, _, err := BuildBankRecords(bank, talk, ids)
		if err != nil {
			return nil, err
		}
		groups, cl := ClassifyBank(bank, records, ctx)
		if len(groups) == 0 {
			groups = []ShardGroup{{Path: "unclassified/" + safeName(bank.Name) + ".json", Function: "unclassified", Indices: rangeInts(len(records))}}
		}
		cl.ShardCount = len(groups)
		cl.RecordCount = len(records)
		res.Classifications = append(res.Classifications, cl)
		funcByIndex := map[int]string{}
		for _, g := range groups {
			for _, i := range g.Indices {
				funcByIndex[i] = g.Function
			}
		}
		narBlock := p01cNarrationBlock(bank)
		br := P01CBankReport{
			Bank: bank.Name, Records: len(records), Category: cl.Category,
			RoleCounts: newRoleCounts(), SpeakerStates: map[string]int{}, Kinds: map[string]int{},
			Sections: map[string]int{}, Evidence: cl.Evidence,
		}
		if bank.JP != nil {
			br.JPSourceSHA256 = bank.JP.CompressedSHA
		}
		if bank.EN != nil {
			br.ENSourceSHA256 = bank.EN.CompressedSHA
		}
		for i := range records {
			r := &records[i]
			idx := r.Source.EntryIndex
			jpAsset, enAsset := "", ""
			if bank.JP != nil {
				jpAsset = bank.JP.AssetPath
			}
			if bank.EN != nil {
				enAsset = bank.EN.AssetPath
			}
			pid, enPID, mpid, event, section, partVoice := "", "", "", "", uint8(0), ""
			if bank.JP != nil && idx < len(bank.JP.Attrs) {
				row := bank.JP.Attrs[idx]
				pid = attrString(row, "PID")
				mpid = attrString(row, "MPID")
				event = attrString(row, "Event")
				if pv, ok := FindAttr(row, "PartVoice"); ok {
					partVoice = pv.EnumName
				}
				if v, ok := FindAttr(row, "Section"); ok {
					section = uint8(v.Enum)
				}
			}
			if bank.EN != nil && idx < len(bank.EN.Attrs) {
				enPID = attrString(bank.EN.Attrs[idx], "PID")
			}
			fn := funcByIndex[i]
			if fn == "" {
				fn = "unclassified"
			}
			role := ClassifyP01CRecord(bank, idx, fn, partVoice, narBlock[idx])
			sp := ResolveSpeakerP01C(role.Role, role.Kind, pid, enPID, mpid, event, partVoice, personIDs, jpAsset, enAsset)
			r.Context.DialogueRole = role.Role
			r.Context.UtteranceKind = role.Kind
			r.Context.SpeakerApplicable = sp.Applicable
			r.Context.Marker = optStr(role.Marker)
			r.Context.BranchTag = optStr(role.BranchTag)
			r.Context.BranchOption = optStr(role.BranchOption)
			r.Context.BranchPhase = optStr(role.BranchPhase)
			r.Context.AuthoredGroup = optStr(role.AuthoredGroup)
			if sp.State == "CORROBORATED" || sp.State == "PROVEN_FROM_ATTRIBUTE" || sp.State == "PROVEN_FROM_CALLSITE" || sp.State == "CONFLICT" {
				id := sp.EntityID
				r.Context.Speaker = EntityRef{State: sp.State, EntityID: &id, Evidence: sp.Evidence}
			} else {
				r.Context.Speaker = EntityRef{State: "UNKNOWN", EntityID: nil, Evidence: []Evidence{}}
			}
			// Private candidate ledger.
			if role.Role == RoleSpoken && sp.State != "CORROBORATED" && sp.State != "PROVEN_FROM_ATTRIBUTE" && sp.State != "PROVEN_FROM_CALLSITE" {
				if c, ok := BuildP01CCandidate(*r, bank.Name, r.Source.Label, idx, role.Role, role.Kind, pid, mpid, sp.Reason, jpAsset); ok {
					res.Candidates = append(res.Candidates, c)
				}
			} else if role.Role == RoleChoice && pid != "" {
				// Audited counterexample: the PID on a choice option is the
				// dialogue owner (interlocutor), never the chooser.
				c := P01CSpeakerCandidate{
					RecordID: r.RecordID, Bank: bank.Name, Label: r.Source.Label, Index: idx,
					State: "CANDIDATE_UNVERIFIED", EntityID: pid, EntityNS: "PID", Role: role.Role, Kind: role.Kind,
					Reason: "CHOICE_OWNER_NOT_SPEAKER", Promotion: "NEVER_PROMOTE",
					Evidence: []Evidence{{Source: jpAsset, Field: "ATR1.PID", Value: pid, Note: "choice option: PID belongs to the dialogue owner, not the chooser; audited counterexample"}},
				}
				res.Candidates = append(res.Candidates, c)
				res.Census.ChoiceOwnerCandidates++
			}
			// Usage evidence (same rule as P01B).
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
			secName := ""
			if ctx != nil {
				secName = ctx.SectionNames[section]
			}
			if secName == "" {
				secName = fmt.Sprintf("sec-%d", section)
			}
			br.Sections[secName]++
			br.SpeakerStates[r.Context.Speaker.State]++
			br.Kinds[role.Kind]++
			br.RoleCounts.add(role.Role, role.Kind, r.Context.Speaker.State, jpIsEmpty(*r))
			res.Census.Total.add(role.Role, role.Kind, r.Context.Speaker.State, jpIsEmpty(*r))
			addRoleCount(res.Census.ByFunction, cl.Category, role.Role, role.Kind, r.Context.Speaker.State, jpIsEmpty(*r))
			addRoleCount(res.Census.ByBank, bank.Name, role.Role, role.Kind, r.Context.Speaker.State, jpIsEmpty(*r))
			if res.Census.ByFunctionRole[cl.Category] == nil {
				res.Census.ByFunctionRole[cl.Category] = map[string]int{}
			}
			res.Census.ByFunctionRole[cl.Category][role.Role]++
			if res.Census.ByBankRole[bank.Name] == nil {
				res.Census.ByBankRole[bank.Name] = map[string]int{}
			}
			res.Census.ByBankRole[bank.Name][role.Role]++
		}
		// Emit shards for this bank.
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
		br.RoleCounts.Candidates = 0
		res.BankReports = append(res.BankReports, br)
		res.Totals.Banks++
		res.Totals.Records += len(records)
		if bank.JP != nil {
			res.Totals.JPRecords += len(bank.JP.Text)
		}
		if bank.EN != nil {
			res.Totals.ENRecords += len(bank.EN.Text)
		}
		res.Records += len(records)
	}
	// Candidate counts per function/bank.
	for _, c := range res.Candidates {
		fn := ""
		for _, br := range res.BankReports {
			if br.Bank == c.Bank {
				fn = br.Category
				bn := res.Census.ByBank[c.Bank]
				bn.Candidates++
				res.Census.ByBank[c.Bank] = bn
				break
			}
		}
		if fn != "" {
			cnt := res.Census.ByFunction[fn]
			cnt.Candidates++
			res.Census.ByFunction[fn] = cnt
		}
		res.Census.Total.Candidates++
	}
	res.Census.finish()
	var err error
	res.Manifest, res.ManifestBytes, err = BuildManifestFromShards(res.Shards)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func jpIsEmpty(r Record) bool {
	return r.Text.JP == nil || strings.TrimSpace(*r.Text.JP) == ""
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

// P01CPromotionAudit is the re-check of pre-P01C public promotions.
type P01CPromotionAudit struct {
	SchemaVersion         int                 `json:"schema_version"`
	PrePromoted           int                 `json:"pre_promoted"`
	FalsePositive         int                 `json:"false_positive_role"`
	FalsePositiveIDs      []string            `json:"false_positive_ids"`
	KeptCorroborated      int                 `json:"kept_corroborated"`
	DowngradedToAttribute int                 `json:"downgraded_to_proven_from_attribute"`
	ByNewRole             map[string]int      `json:"by_new_role"`
	ByNewKind             map[string]int      `json:"by_new_kind"`
	ByEvidence            map[string]int      `json:"by_evidence"`
	Samples               []map[string]string `json:"samples"`
	Note                  string              `json:"note"`
}

// AuditPromotions compares the P01B public speaker promotions with the P01C
// role/evidence model.
func AuditPromotions(cfg Config, corp *Corpus, ctx *P01BContext, preRoot string) (*P01CPromotionAudit, error) {
	res, err := BuildP01C(corp, ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]Record{}
	for _, b := range res.Shards {
		var sh Shard
		if err := json.Unmarshal(b, &sh); err != nil {
			return nil, err
		}
		for _, r := range sh.Records {
			byID[r.RecordID] = r
		}
	}
	audit := &P01CPromotionAudit{SchemaVersion: 1, ByNewRole: map[string]int{}, ByNewKind: map[string]int{}, ByEvidence: map[string]int{}}
	if preRoot == "" {
		preRoot = cfg.TranslationsDir
	}
	err = walkTranslations(preRoot, func(rel string, b []byte) error {
		var sh Shard
		if err := json.Unmarshal(b, &sh); err != nil {
			return err
		}
		for _, old := range sh.Records {
			if old.Context.Speaker.State != "CORROBORATED" {
				continue
			}
			audit.PrePromoted++
			nr, ok := byID[old.RecordID]
			if !ok {
				audit.FalsePositive++
				audit.FalsePositiveIDs = append(audit.FalsePositiveIDs, old.RecordID)
				continue
			}
			audit.ByNewRole[nr.Context.DialogueRole]++
			audit.ByNewKind[nr.Context.UtteranceKind]++
			if nr.Context.DialogueRole != RoleSpoken {
				audit.FalsePositive++
				if len(audit.FalsePositiveIDs) < 50 {
					audit.FalsePositiveIDs = append(audit.FalsePositiveIDs, old.RecordID)
				}
				continue
			}
			switch nr.Context.Speaker.State {
			case "CORROBORATED":
				audit.KeptCorroborated++
				audit.ByEvidence["event_motion_or_staging_on_pid"]++
			case "PROVEN_FROM_ATTRIBUTE":
				audit.DowngradedToAttribute++
				audit.ByEvidence["pid_attribute_only"]++
			default:
				audit.FalsePositive++
				if len(audit.FalsePositiveIDs) < 50 {
					audit.FalsePositiveIDs = append(audit.FalsePositiveIDs, old.RecordID)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	audit.Note = "Every pre-P01C promotion is re-classified with the P01C role model. A promotion is a false positive only when the record is not a SPOKEN record (choice/system/narration/control/UI/empty) or its PID is absent from Person.bin / a JP-EN PID conflict. Records whose Event does not name the PID are still correct speaker attributions but are labelled PROVEN_FROM_ATTRIBUTE instead of CORROBORATED."
	return audit, nil
}

// walkTranslations walks canonical shard JSON files (excluding manifest and
// variants) and calls fn with the project-relative path and bytes.
func walkTranslations(root string, fn func(string, []byte) error) error {
	files, err := listJSONShards(root)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := fn(f.Path, f.Bytes); err != nil {
			return err
		}
	}
	return nil
}

// shardFile is one canonical shard read from disk.
type shardFile struct {
	Path  string
	Bytes []byte
}
