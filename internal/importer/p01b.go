package importer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"fefw/internal/elfdata"
	"fefw/internal/msbt"
)

// P01BContext bundles the private evidence indexes used for enrichment and
// classification. A nil context disables P01B enrichment.
type P01BContext struct {
	Scripts      *ScriptIndex
	Entities     *EntityIndex
	Talk         *elfdata.TalkMsg
	SectionNames map[uint8]string
}

// NamespaceHas reports whether a GameData table contains the given identifier
// namespace literal.
func (p *P01BContext) NamespaceHas(ns, table string) bool {
	if p == nil || p.Entities == nil {
		return false
	}
	if !strings.HasSuffix(ns, "_") {
		ns += "_"
	}
	for _, f := range p.Entities.NamespaceFile[ns] {
		if f == table {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// P01B evidence layer: script callsites, entity tables, attribute audit,
// speaker/addressee resolution and evidence-backed function classification.
// ---------------------------------------------------------------------------

// ScriptRef is one exact literal occurrence of a message/entity identifier in
// a source script. Offset is private evidence and never emitted publicly.
type ScriptRef struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Token  string `json:"token"`
}

// ScriptIndex maps source identifiers to every script literal occurrence.
type ScriptIndex struct {
	SchemaVersion int                    `json:"schema_version"`
	Files         int                    `json:"files"`
	Literals      int                    `json:"literals"`
	Distinct      int                    `json:"distinct_tokens"`
	Refs          map[string][]ScriptRef `json:"refs"`
	Errors        []string               `json:"errors"`
}

var p01bTokenRe = regexp.MustCompile(`(?:MID|MSID|MPID|MIID|MSQID|MSSID|MJID|MLID|MATID|MCID|MRID|MTID|MWID)_[A-Za-z0-9_]+`)

// BuildScriptIndex decompresses every staged Script/*.bin.zst and indexes
// identifier literals. It records exact file+offset evidence only.
func BuildScriptIndex(c Config) (*ScriptIndex, error) {
	idx := &ScriptIndex{SchemaVersion: 1, Refs: map[string][]ScriptRef{}, Errors: []string{}}
	root := filepath.Join(c.SourceRoot, "romfs", "Script")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".bin.zst") {
			return nil
		}
		comp, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		raw, err := DecompressZstd(comp)
		if err != nil {
			idx.Errors = append(idx.Errors, fmt.Sprintf("%s: zstd: %v", filepath.Base(path), err))
			return nil
		}
		rel, _ := filepath.Rel(c.SourceRoot, path)
		rel = strings.TrimPrefix(filepath.ToSlash(rel), "romfs/")
		for _, m := range p01bTokenRe.FindAllIndex(raw, -1) {
			tok := string(raw[m[0]:m[1]])
			idx.Refs[tok] = append(idx.Refs[tok], ScriptRef{Path: rel, Offset: int64(m[0]), Token: tok})
			idx.Literals++
		}
		idx.Files++
		return nil
	})
	if err != nil {
		return nil, err
	}
	idx.Distinct = len(idx.Refs)
	// Deterministic order for evidence output.
	for k := range idx.Refs {
		sort.Slice(idx.Refs[k], func(i, j int) bool {
			if idx.Refs[k][i].Path != idx.Refs[k][j].Path {
				return idx.Refs[k][i].Path < idx.Refs[k][j].Path
			}
			return idx.Refs[k][i].Offset < idx.Refs[k][j].Offset
		})
	}
	return idx, nil
}

// DistinctPaths returns the sorted unique script files referencing a token.
func (s *ScriptIndex) DistinctPaths(token string) []string {
	if s == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range s.Refs[token] {
		if !seen[r.Path] {
			seen[r.Path] = true
			out = append(out, r.Path)
		}
	}
	sort.Strings(out)
	return out
}

// EntityIndex records which source tables contain a person/profile/namespace
// identifier. Membership is string-table evidence, not runtime proof.
type EntityIndex struct {
	PersonIDs     map[string]bool     `json:"person_ids"`
	ProfileIDs    map[string]bool     `json:"profile_ids"`
	NamespaceFile map[string][]string `json:"namespace_files"`
	PersonSHA256  string              `json:"person_sha256"`
	ProfileSHA256 string              `json:"profile_sha256"`
}

var p01bPIDRe = regexp.MustCompile(`PID_([^\x00\r\n]+)`)
var p01bMPIDRe = regexp.MustCompile(`MPID_([^\x00\r\n]+)`)

var p01bNamespaces = []string{"MATID_", "MCID_", "MIID_", "MJID_", "MLID_", "MPID_", "MSID_", "MSQID_", "MSSID_", "MRID_", "MTID_", "MWID_", "MID_"}

// BuildEntityIndex scans the staged GameData tables for person/profile IDs and
// identifier namespaces. It never copies source bytes into public artifacts.
func BuildEntityIndex(c Config) (*EntityIndex, error) {
	e := &EntityIndex{PersonIDs: map[string]bool{}, ProfileIDs: map[string]bool{}, NamespaceFile: map[string][]string{}}
	personPath := filepath.Join(c.SourceRoot, "romfs", "GameData", "Person.bin")
	profilePath := filepath.Join(c.SourceRoot, "romfs", "GameData", "Profile.bin")
	if b, err := os.ReadFile(personPath); err == nil {
		for _, m := range p01bPIDRe.FindAllSubmatch(b, -1) {
			e.PersonIDs[strings.TrimSpace(string(m[1]))] = true
		}
		e.PersonSHA256 = shaBytes(b)
	}
	if b, err := os.ReadFile(profilePath); err == nil {
		for _, m := range p01bMPIDRe.FindAllSubmatch(b, -1) {
			e.ProfileIDs[strings.TrimSpace(string(m[1]))] = true
		}
		e.ProfileSHA256 = shaBytes(b)
	}
	root := filepath.Join(c.SourceRoot, "romfs", "GameData")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(c.SourceRoot, path)
		rel = strings.TrimPrefix(filepath.ToSlash(rel), "romfs/")
		for _, ns := range p01bNamespaces {
			if bytes.Contains(b, []byte(ns)) {
				e.NamespaceFile[ns] = append(e.NamespaceFile[ns], rel)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for k := range e.NamespaceFile {
		sort.Strings(e.NamespaceFile[k])
	}
	return e, nil
}

// SectionNames maps the MSBP Section enum value to its authored token.
func SectionNames(p *msbt.Project) map[uint8]string {
	out := map[uint8]string{}
	if p == nil || p.Schema == nil || len(p.Schema.Enums) <= 5 {
		return out
	}
	for i, name := range p.Schema.Enums[5] {
		if i > 255 {
			break
		}
		out[uint8(i)] = name
	}
	return out
}

// SectionToken returns a filesystem-safe token for a section name.
func SectionToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// IsStorySection excludes Sec-None (0) and Sec-Ignore (7).
func IsStorySection(v uint8) bool { return v != 0 && v != 7 }

// ---------------------------------------------------------------------------
// Attribute audit
// ---------------------------------------------------------------------------

// AttrFieldAudit is one MSBP attribute field bound to a locale's ATO1/ATR1.
type AttrFieldAudit struct {
	Name             string   `json:"name"`
	Type             uint8    `json:"type"`
	EnumID           uint16   `json:"enum_id"`
	Offset           uint32   `json:"offset"`
	PresentInATO1    bool     `json:"present_in_ato1"`
	RecordSize       uint32   `json:"record_size"`
	NonEmpty         int      `json:"non_empty"`
	Distinct         int      `json:"distinct"`
	EnumMembers      int      `json:"enum_members"`
	OutOfRangeValues int      `json:"out_of_range_values"`
	Examples         []string `json:"examples"`
}

// AttrAudit is the private per-locale attribute schema/data audit.
type AttrAudit struct {
	Locale           string           `json:"locale"`
	MSBPSHA256       string           `json:"msbp_sha256"`
	DefCount         int              `json:"defined_attributes"`
	ATO1Offsets      []uint32         `json:"ato1_offsets"`
	PresentOffsets   []uint32         `json:"present_offsets"`
	Fields           []AttrFieldAudit `json:"fields"`
	Banks            int              `json:"banks"`
	Records          int              `json:"records"`
	Notes            []string         `json:"notes"`
	NonEmptyByBank   map[string]int   `json:"non_empty_by_bank,omitempty"`
	ValueSampleLimit int              `json:"value_sample_limit"`
}

// AuditAttributes builds the per-locale schema/presence/value audit.
func AuditAttributes(c *Corpus) (map[string]*AttrAudit, error) {
	out := map[string]*AttrAudit{}
	if c == nil || c.JPProject == nil {
		return out, fmt.Errorf("missing JP MSBP project")
	}
	for _, loc := range []string{"JPja", "USen"} {
		proj := c.JPProject
		sha := c.JPMSBPSHA
		if loc == "USen" {
			proj = c.ENProject
			sha = c.ENMSBPSHA
		}
		audit := &AttrAudit{Locale: loc, MSBPSHA256: sha, ValueSampleLimit: 4, NonEmptyByBank: map[string]int{}}
		if proj == nil || proj.Schema == nil {
			out[loc] = audit
			continue
		}
		audit.DefCount = len(proj.Schema.Defs)
		for _, d := range proj.Schema.Defs {
			f := AttrFieldAudit{Name: d.Name, Type: d.Type, EnumID: d.EnumID, Offset: d.Offset, EnumMembers: enumLen(proj, d.EnumID)}
			audit.Fields = append(audit.Fields, f)
		}
		// Presence from every bank's ATO1; all must agree.
		presence := map[uint32]int{}
		for _, bank := range c.Banks {
			s := bank.JP
			if loc == "USen" {
				s = bank.EN
			}
			if s == nil || s.File == nil || s.File.ATO1 == nil {
				continue
			}
			audit.Banks++
			audit.Records += len(s.Text)
			var offs []uint32
			var present []uint32
			for _, v := range s.File.ATO1.Offsets {
				offs = append(offs, v)
				if v != 0xFFFFFFFF {
					presence[v]++
					present = append(present, v)
				}
			}
			if len(audit.ATO1Offsets) == 0 {
				audit.ATO1Offsets = offs
				audit.PresentOffsets = present
			}
		}
		for i := range audit.Fields {
			audit.Fields[i].PresentInATO1 = presence[audit.Fields[i].Offset] > 0
		}
		// Value census.
		distinct := make([]map[string]bool, len(audit.Fields))
		nonEmpty := make([]int, len(audit.Fields))
		outOfRange := make([]int, len(audit.Fields))
		examples := make([][]string, len(audit.Fields))
		for i := range distinct {
			distinct[i] = map[string]bool{}
		}
		for _, bank := range c.Banks {
			s := bank.JP
			if loc == "USen" {
				s = bank.EN
			}
			if s == nil || s.Attrs == nil {
				continue
			}
			for _, row := range s.Attrs {
				for _, v := range row {
					if v.Index < 0 || v.Index >= len(audit.Fields) {
						continue
					}
					f := &audit.Fields[v.Index]
					var val string
					if v.Type == 9 {
						if v.Enum == 0 {
							continue
						}
						val = fmt.Sprintf("enum:%d", v.Enum)
						if enumLen(proj, f.EnumID) > 0 && int(v.Enum) >= enumLen(proj, f.EnumID) {
							outOfRange[v.Index]++
						}
					} else if v.Type == 8 {
						val = v.String
					} else {
						val = fmt.Sprintf("num:%d", v.Int)
					}
					if val == "" {
						continue
					}
					nonEmpty[v.Index]++
					distinct[v.Index][val] = true
					if len(examples[v.Index]) < 4 && !strings.Contains(val, "\n") && len(val) <= 80 {
						examples[v.Index] = append(examples[v.Index], val)
					}
				}
			}
		}
		for i := range audit.Fields {
			audit.Fields[i].NonEmpty = nonEmpty[i]
			audit.Fields[i].Distinct = len(distinct[i])
			audit.Fields[i].OutOfRangeValues = outOfRange[i]
			audit.Fields[i].Examples = examples[i]
			audit.Fields[i].RecordSize = recordSize(c, loc)
		}
		audit.Notes = []string{
			"All 523 banks declare the same ATO1 presence set: offsets 0..69 plus 70; Speaker(71), Voice_*(72..99) and USen_*(93..103) are declared in MSBP but are not stored in ATR1.",
			"Use(offset 70, declared enum 137 members) has 256 distinct byte values and out-of-range values; it is not usable as a chapter/usage table and is never used for classification.",
			"PID is a string person id (matches PID_<value> in GameData/Person.bin). MPID is a string portrait/model id (matches MPID_<value> in GameData/Profile.bin).",
			"ReferenceJPja/ReferenceUSen are string cross-references to other message/entity ids, not speaker fields.",
		}
		out[loc] = audit
	}
	return out, nil
}

func enumLen(p *msbt.Project, id uint16) int {
	if p == nil || p.Schema == nil || int(id) >= len(p.Schema.Enums) {
		return 0
	}
	return len(p.Schema.Enums[id])
}

func recordSize(c *Corpus, loc string) uint32 {
	for _, b := range c.Banks {
		s := b.JP
		if loc == "USen" {
			s = b.EN
		}
		if s != nil && s.File != nil && s.File.ATR1 != nil {
			return s.File.ATR1.BytesPerAttribute
		}
	}
	return 0
}

func firstJP(c *Corpus) *Side {
	if c == nil || len(c.Banks) == 0 {
		return nil
	}
	return c.Banks[0].JP
}
func firstEN(c *Corpus) *Side {
	if c == nil || len(c.Banks) == 0 {
		return nil
	}
	return c.Banks[0].EN
}

// ---------------------------------------------------------------------------
// Speaker/addressee resolution
// ---------------------------------------------------------------------------

var p01bEventPIDRe = regexp.MustCompile(`PID_([^\s,\)\r\n]+)`)
var p01bLookRe = regexp.MustCompile(`キャラ視線\(PID_([^,\s,\)]+)\s*,\s*PID_([^,\s,\)]+)`)
var p01bTagStrip = regexp.MustCompile(`<[^>]*>`)

// SpeakerResolution is the deterministic result for one physical record.
type SpeakerResolution struct {
	State    string
	EntityID string
	Evidence []Evidence
	Reason   string
	PIDs     []string
	MPID     string
	Section  string
}

// NormalizePID trims whitespace/CRLF noise from a source PID value.
func NormalizePID(s string) string { return strings.TrimSpace(s) }

// VisibleText reports whether a raw MSBT string contains player-visible text
// after removing encoded tag markers and control characters.
func VisibleText(raw []byte) bool {
	s := string(raw)
	s = p01bTagStrip.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || r >= 32 {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String()) != ""
}

// HasActorCommand reports an exact line-level actor performance command for pid.
func HasActorCommand(event, pid string) bool {
	for _, p := range []string{
		"キャラモーション再生(PID_" + pid,
		"キャラ視線(PID_" + pid,
		"拠点_会話開始",
	} {
		if strings.Contains(event, p) {
			return true
		}
	}
	return false
}

// ResolveSpeaker applies the P01B promotion rule:
//   - PID must be a real Person.bin person identifier.
//   - JP and US EN PID must agree when both are present.
//   - The record must have visible JP text.
//   - The record's Event must name exactly one PID, equal to the row PID,
//     with a line-level actor performance command.
//
// MPID alone never promotes: it is a Profile.bin portrait/model identifier.
func ResolveSpeaker(pid, enPID, mpid, event string, visible bool, personIDs map[string]bool, assetJP, assetEN string) SpeakerResolution {
	res := SpeakerResolution{State: "UNKNOWN", EntityID: "", Evidence: []Evidence{}, MPID: NormalizePID(mpid)}
	rawPID := NormalizePID(pid)
	rawEN := NormalizePID(enPID)
	if rawPID == "" {
		res.Reason = "NO_PID"
		return res
	}
	if rawEN == "" {
		res.Reason = "JP_EN_PID_MISSING"
		return res
	}
	if rawEN != rawPID {
		res.State = "CONFLICT"
		res.Reason = "JP_EN_PID_MISMATCH"
		res.Evidence = []Evidence{
			{Source: assetJP, Field: "ATR1.PID", Value: rawPID, Note: "JP person identifier"},
			{Source: assetEN, Field: "ATR1.PID", Value: rawEN, Note: "US EN person identifier differs"},
		}
		return res
	}
	uniq := map[string]bool{}
	for _, m := range p01bEventPIDRe.FindAllStringSubmatch(event, -1) {
		uniq[NormalizePID(m[1])] = true
	}
	for k := range uniq {
		res.PIDs = append(res.PIDs, k)
	}
	sort.Strings(res.PIDs)
	switch {
	case !visible:
		res.Reason = "NO_VISIBLE_TEXT"
		return res
	case len(uniq) == 0:
		res.Reason = "NO_EVENT_ACTOR"
		return res
	case !uniq[rawPID]:
		res.Reason = "EVENT_ACTOR_MISMATCH"
		return res
	case len(uniq) > 1:
		res.Reason = "EVENT_MULTI_ACTOR"
		return res
	case !personIDs[rawPID]:
		res.Reason = "PERSON_TABLE_MISSING"
		return res
	case !HasActorCommand(event, rawPID):
		res.Reason = "NO_ACTOR_PERFORMANCE"
		return res
	}
	res.State = "CORROBORATED"
	res.EntityID = rawPID
	res.Reason = "PID_PERSON_TABLE_AND_SOLE_EVENT_ACTOR"
	res.Evidence = []Evidence{
		{Source: assetJP, Field: "ATR1.PID", Value: rawPID, Note: "person identifier; Person.bin contains PID_" + rawPID},
		{Source: assetJP, Field: "ATR1.Event", Value: "PID_" + rawPID, Note: "sole staged actor with motion/look-at/placement command for this message"},
		{Source: "GameData/Person.bin", Field: "PID_" + rawPID, Value: rawPID, Note: "person-table membership"},
	}
	if rawEN != "" {
		res.Evidence = append(res.Evidence, Evidence{Source: assetEN, Field: "ATR1.PID", Value: rawEN, Note: "US EN person identifier agrees"})
	}
	return res
}

// AddresseeCandidate is private-only look-at evidence; it is never promoted.
type AddresseeCandidate struct {
	RecordID string `json:"record_id"`
	Speaker  string `json:"speaker_pid"`
	Target   string `json:"target_pid"`
	Source   string `json:"event_asset"`
}

// ResolveAddresseeCandidates extracts unique look-at targets for a speaker.
func ResolveAddresseeCandidates(recordID, pid, event, asset string) []AddresseeCandidate {
	pid = NormalizePID(pid)
	if pid == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []AddresseeCandidate
	for _, m := range p01bLookRe.FindAllStringSubmatch(event, -1) {
		s, t := NormalizePID(m[1]), NormalizePID(m[2])
		if s != pid || t == "" || t == pid || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, AddresseeCandidate{RecordID: recordID, Speaker: pid, Target: t, Source: asset})
	}
	return out
}

// ---------------------------------------------------------------------------
// Evidence-backed classification
// ---------------------------------------------------------------------------

// ShardGroup is one output shard slice of a physical bank.
type ShardGroup struct {
	Path     string
	Function string
	Indices  []int
}

// Classification is the private evidence for one bank's grouping decision.
type Classification struct {
	Bank        string         `json:"bank"`
	Category    string         `json:"category"`
	Rule        string         `json:"rule"`
	Evidence    []string       `json:"evidence"`
	Sections    map[string]int `json:"sections,omitempty"`
	Prefixes    map[string]int `json:"prefixes,omitempty"`
	ShardCount  int            `json:"shard_count"`
	RecordCount int            `json:"record_count"`
	Remainder   bool           `json:"remainder"`
}

var p01bSupportPairRe = regexp.MustCompile(`^MID_SE_([A-Za-z]+_[A-Za-z]+)_`)

func prefixOf(label string) string {
	if i := strings.IndexByte(label, '_'); i >= 0 {
		return label[:i]
	}
	return label
}

// ClassifyBank groups a bank's records from source-backed evidence only. It
// never uses the source bank filename or prefix as a category guess.
func ClassifyBank(bank Bank, records []Record, enr *P01BContext) ([]ShardGroup, Classification) {
	if enr == nil {
		enr = &P01BContext{}
	}
	cl := Classification{Bank: bank.Name, Category: "unclassified", Sections: map[string]int{}, Prefixes: map[string]int{}, RecordCount: len(records)}
	byIndex := map[int]Record{}
	for _, r := range records {
		byIndex[r.Source.EntryIndex] = r
	}
	labels := map[int]string{}
	for i := range bank.JP.Text {
		labels[i] = bank.JP.Labels[uint32(i)]
	}
	if len(labels) == 0 && bank.EN != nil {
		for i := range bank.EN.Text {
			labels[i] = bank.EN.Labels[uint32(i)]
		}
	}
	prefixCount := map[string]int{}
	supportPairCount := map[string]int{}
	sectionCount := map[string]int{}
	sectionValues := map[int]uint8{}
	talkRefs := 0
	scriptRefs := 0
	for i := 0; i < len(records); i++ {
		lbl := labels[i]
		prefixCount[prefixOf(lbl)]++
		if m := p01bSupportPairRe.FindStringSubmatch(lbl); len(m) == 2 {
			supportPairCount[m[1]]++
		}
		if bank.JP != nil && i < len(bank.JP.Attrs) {
			if v, ok := FindAttr(bank.JP.Attrs[i], "Section"); ok {
				sectionValues[i] = uint8(v.Enum)
				name := enr.SectionNames[uint8(v.Enum)]
				if name == "" {
					name = fmt.Sprintf("sec-%d", v.Enum)
				}
				sectionCount[name]++
			}
		}
		if enr.Talk != nil {
			if _, _, _, ok := enr.Talk.Lookup(lbl); ok {
				talkRefs++
			}
		}
		if enr.Scripts != nil && len(enr.Scripts.Refs[lbl]) > 0 {
			scriptRefs++
		}
	}
	cl.Prefixes = prefixCount
	cl.Sections = sectionCount
	dominantPrefix := ""
	dominantCount := 0
	for k, v := range prefixCount {
		if v > dominantCount || (v == dominantCount && k < dominantPrefix) {
			dominantPrefix, dominantCount = k, v
		}
	}
	n := len(records)
	ratio := func(k int) float64 {
		if n == 0 {
			return 0
		}
		return float64(k) / float64(n)
	}
	// 1. Support conversations: the label itself names the pair.
	if pair := dominantSupportPair(supportPairCount, n); pair != "" {
		groups := groupBy(records, func(i int) string {
			if m := p01bSupportPairRe.FindStringSubmatch(labels[i]); len(m) == 2 {
				return m[1]
			}
			return ""
		})
		var out []ShardGroup
		for _, g := range groups {
			if g.key == "" {
				out = append(out, ShardGroup{Path: "unclassified/" + safeName(bank.Name) + ".json", Function: "unclassified", Indices: g.idx})
				continue
			}
			out = append(out, ShardGroup{Path: "supports/" + safeName(g.key) + "/" + safeName(bank.Name) + ".json", Function: "supports", Indices: g.idx})
		}
		sortGroups(out)
		cl.Category, cl.Rule = "supports", "label namespace MID_SE_<pair>"
		cl.Evidence = append(cl.Evidence, "every grouped record label matches ^MID_SE_<pair>_")
		cl.ShardCount = len(out)
		return out, cl
	}
	// 2-5. Table-backed identifier families.
	type fam struct {
		ns, category, table string
	}
	families := []fam{
		{"MIID", "items", "GameData/Item.bin"},
		{"MJID", "classes", "GameData/Job.bin"},
		{"MSID", "skills", "GameData/Skill.bin"},
		{"MPID", "characters", "GameData/Profile.bin"},
		{"MLID", "other", "GameData/Legion.bin"},
		{"MTID", "other", "GameData/Terrain.bin"},
		{"MRID", "other", "GameData/Job.bin"},
	}
	for _, f := range families {
		if dominantPrefix != f.ns || ratio(prefixCount[f.ns]) < 0.80 || !enr.NamespaceHas(f.ns+"_", f.table) {
			continue
		}
		groups := map[string][]int{}
		for i := 0; i < n; i++ {
			lbl := labels[i]
			role := "names"
			if strings.Contains(lbl, "_H_") || strings.HasSuffix(lbl, "_H") {
				role = "descriptions"
			}
			if f.category == "characters" {
				role = "profiles"
			}
			if f.ns == "MSID" {
				if strings.HasPrefix(lbl, "MSID_H_") {
					role = "descriptions"
				} else {
					role = "names"
				}
			}
			if f.category == "items" && role == "descriptions" {
				// Structural help-line split: trailing 1/2.
				if strings.HasSuffix(lbl, "1") {
					role = "descriptions-line1"
				} else if strings.HasSuffix(lbl, "2") {
					role = "descriptions-line2"
				} else {
					role = "descriptions-other"
				}
			}
			groups[role] = append(groups[role], i)
		}
		var out []ShardGroup
		if f.category == "other" {
			out = append(out, ShardGroup{Path: "other/" + strings.ToLower(f.ns) + "/" + safeName(bank.Name) + ".json", Function: "other", Indices: rangeInts(n)})
		} else {
			for role, idx := range groups {
				out = append(out, ShardGroup{Path: f.category + "/" + role + "/" + safeName(bank.Name) + ".json", Function: f.category, Indices: idx})
			}
		}
		sortGroups(out)
		cl.Category, cl.Rule = f.category, "dominant label namespace "+f.ns+"_ and table presence"
		cl.Evidence = append(cl.Evidence, fmt.Sprintf("%s_ labels=%d/%d (%.1f%%)", f.ns, prefixCount[f.ns], n, 100*ratio(prefixCount[f.ns])))
		cl.Evidence = append(cl.Evidence, f.table+" contains "+f.ns+"_ identifier strings")
		cl.ShardCount = len(out)
		return out, cl
	}
	// 6. Roster entries: per-character career text.
	if ratio(countPrefix(labels, "MID_Roster_")) >= 0.80 {
		groups := map[string][]int{}
		for i := 0; i < n; i++ {
			parts := strings.Split(labels[i], "_")
			tok := "other"
			if len(parts) >= 5 {
				tok = parts[3]
			}
			groups[tok] = append(groups[tok], i)
		}
		var out []ShardGroup
		for tok, idx := range groups {
			out = append(out, ShardGroup{Path: "characters/roster/" + safeName(tok) + "/" + safeName(bank.Name) + ".json", Function: "characters", Indices: idx})
		}
		sortGroups(out)
		cl.Category, cl.Rule = "characters", "label namespace MID_Roster_<field>_<token>_"
		cl.Evidence = append(cl.Evidence, "roster labels: "+fmt.Sprint(len(groups))+" source character tokens across MID_Roster_<field>_ families")
		cl.ShardCount = len(out)
		return out, cl
	}
	// 7. Quests: MSQID_<Role>_<QuestID>.
	if ratio(prefixCount["MSQID"]+prefixCount["MSSID"]) >= 0.60 {
		groups := map[string][]int{}
		for i := 0; i < n; i++ {
			parts := strings.Split(labels[i], "_")
			key := "other"
			if len(parts) >= 3 {
				key = strings.Join(parts[2:], "_")
			}
			groups[key] = append(groups[key], i)
		}
		var out []ShardGroup
		for key, idx := range groups {
			out = append(out, ShardGroup{Path: "quests/" + safeName(key) + "/" + safeName(bank.Name) + ".json", Function: "quests", Indices: idx})
		}
		sortGroups(out)
		cl.Category, cl.Rule = "quests", "label namespace MSQID_/MSSID_<quest>"
		cl.Evidence = append(cl.Evidence, fmt.Sprintf("MSQID_=%d MSSID_=%d of %d", prefixCount["MSQID"], prefixCount["MSSID"], n))
		cl.ShardCount = len(out)
		return out, cl
	}
	// 7a. Feature/voice banks that use story-like MID labels but are not
	// chapter scenes. Their authored sub-namespaces prove the family.
	lib := countPrefix(labels, "MID_LGV") + countPrefix(labels, "MID_LGA")
	if ratio(lib) >= 0.60 {
		groups := map[string][]int{}
		for i := 0; i < n; i++ {
			parts := strings.Split(labels[i], "_")
			key := "other"
			if len(parts) >= 4 {
				key = strings.Join(parts[:4], "_")
			} else if len(parts) >= 2 {
				key = strings.Join(parts[:2], "_")
			}
			groups[key] = append(groups[key], i)
		}
		var out []ShardGroup
		for key, idx := range groups {
			out = append(out, ShardGroup{Path: "other/library-guide/" + safeName(key) + "/" + safeName(bank.Name) + ".json", Function: "other", Indices: idx})
		}
		sortGroups(out)
		cl.Category, cl.Rule = "other", "dominant label namespace MID_LGV_/MID_LGA_ (library guide)"
		cl.Evidence = append(cl.Evidence, fmt.Sprintf("MID_LGV_=%d MID_LGA_=%d of %d", countPrefix(labels, "MID_LGV"), countPrefix(labels, "MID_LGA"), n))
		cl.ShardCount = len(out)
		return out, cl
	}
	death := countPrefix(labels, "MID_DIE") + countPrefix(labels, "MID_EV_DIE") + countPrefix(labels, "MID_EV_RETREAT") + countPrefix(labels, "MID_RETREAT")
	if ratio(death) >= 0.60 {
		out := []ShardGroup{{Path: "other/death-retreat/" + safeName(bank.Name) + ".json", Function: "other", Indices: rangeInts(n)}}
		cl.Category, cl.Rule = "other", "label namespace MID_DIE_/MID_EV_DIE_/MID_RETREAT_ (death/retreat quotes)"
		cl.Evidence = append(cl.Evidence, fmt.Sprintf("death/retreat labels=%d/%d", death, n))
		cl.ShardCount = 1
		return out, cl
	}
	if ratio(countPrefix(labels, "MID_GiftReaction")) >= 0.60 {
		out := []ShardGroup{{Path: "other/gift-reaction/" + safeName(bank.Name) + ".json", Function: "other", Indices: rangeInts(n)}}
		cl.Category, cl.Rule = "other", "label namespace MID_GiftReaction_"
		cl.Evidence = append(cl.Evidence, "gift reaction labels dominate")
		cl.ShardCount = 1
		return out, cl
	}
	if ratio(countPrefix(labels, "MID_LG_Sub")+countPrefix(labels, "MID_SubLobby")) >= 0.60 {
		out := []ShardGroup{{Path: "other/sublobby/" + safeName(bank.Name) + ".json", Function: "other", Indices: rangeInts(n)}}
		cl.Category, cl.Rule = "other", "label namespace MID_LG_Sub_/MID_SubLobby_"
		cl.Evidence = append(cl.Evidence, "sub-lobby labels dominate")
		cl.ShardCount = 1
		return out, cl
	}
	// 7b. Movie subtitles and ending text are story by authored label family.
	if ratio(countPrefix(labels, "MID_MV")) >= 0.80 {
		out := []ShardGroup{{Path: "story/cutscene/" + safeName(bank.Name) + ".json", Function: "story", Indices: rangeInts(n)}}
		cl.Category, cl.Rule = "story", "label namespace MID_MV (movie subtitles)"
		cl.Evidence = append(cl.Evidence, "≥80% labels start with MID_MV")
		cl.ShardCount = 1
		return out, cl
	}
	if ratio(countPrefix(labels, "MID_END")) >= 0.80 {
		out := []ShardGroup{{Path: "story/ending/" + safeName(bank.Name) + ".json", Function: "story", Indices: rangeInts(n)}}
		cl.Category, cl.Rule = "story", "label namespace MID_END (ending text)"
		cl.Evidence = append(cl.Evidence, "≥80% labels start with MID_END")
		cl.ShardCount = 1
		return out, cl
	}
	// 7c. Authored system-voice namespace.
	if ratio(countPrefix(labels, "MID_SV_")) >= 0.80 {
		out := []ShardGroup{{Path: "other/system-voice/" + safeName(bank.Name) + ".json", Function: "other", Indices: rangeInts(n)}}
		cl.Category, cl.Rule = "other", "label namespace MID_SV_ (system voice)"
		cl.Evidence = append(cl.Evidence, "≥80% labels start with MID_SV_")
		cl.ShardCount = 1
		return out, cl
	}
	// 8. UI/system labels with an explicit sub-namespace.
	if uiFam := uiFamily(labels, n); uiFam != "" {
		groups := map[string][]int{}
		for i := 0; i < n; i++ {
			groups[uiFam] = append(groups[uiFam], i)
		}
		out := []ShardGroup{{Path: "ui/" + uiFam + "/" + safeName(bank.Name) + ".json", Function: "ui", Indices: groups[uiFam]}}
		cl.Category, cl.Rule = "ui", "dominant explicit UI/help label namespace"
		cl.Evidence = append(cl.Evidence, "UI label sub-namespace "+uiFam)
		cl.ShardCount = 1
		return out, cl
	}
	// 9. Story: TalkMsg-referenced plus a non-null, non-ignore Section and
	// either an EV/SC story label family or an exact script callsite.
	storyLabels := countPrefix(labels, "MID_EV") + countPrefix(labels, "MID_SC")
	if talkRefs > 0 && ratio(prefixCount["MID"]) >= 0.80 && (ratio(storyLabels) >= 0.50 || scriptRefs > 0) {
		groups := map[string][]int{}
		for i := 0; i < n; i++ {
			sec := sectionValues[i]
			if !IsStorySection(sec) {
				groups["sec-other"] = append(groups["sec-other"], i)
				continue
			}
			name := enr.SectionNames[sec]
			if name == "" {
				name = fmt.Sprintf("sec-%d", sec)
			}
			groups[SectionToken(name)] = append(groups[SectionToken(name)], i)
		}
		var out []ShardGroup
		for sec, idx := range groups {
			fn := "story"
			dir := "story/" + sec
			if sec == "sec-other" {
				fn = "unclassified"
				dir = "unclassified"
			}
			out = append(out, ShardGroup{Path: dir + "/" + safeName(bank.Name) + ".json", Function: fn, Indices: idx})
		}
		sortGroups(out)
		cl.Category, cl.Rule = "story", "TalkMsg-referenced labels + corroborated Section attribute"
		cl.Evidence = append(cl.Evidence, fmt.Sprintf("TalkMsg labels=%d; sections=%v", talkRefs, sectionCount))
		cl.ShardCount = len(out)
		return out, cl
	}
	// 10. Explicit ignore section.
	if n > 0 && ratio(sectionCount["Sec-Ignore"]) >= 0.80 {
		out := []ShardGroup{{Path: "other/sec-ignore/" + safeName(bank.Name) + ".json", Function: "other", Indices: rangeInts(n)}}
		cl.Category, cl.Rule = "other", "Section=Sec-Ignore"
		cl.Evidence = append(cl.Evidence, "Section attribute is Sec-Ignore")
		cl.ShardCount = 1
		return out, cl
	}
	// Remainder: no evidence-backed category boundary.
	out := []ShardGroup{{Path: "unclassified/" + safeName(bank.Name) + ".json", Function: "unclassified", Indices: rangeInts(n)}}
	cl.Category, cl.Rule = "unclassified", "no source-backed category boundary"
	cl.Evidence = append(cl.Evidence, fmt.Sprintf("dominant prefix %q=%d/%d; talkRefs=%d; scriptRefs=%d", dominantPrefix, dominantCount, n, talkRefs, scriptRefs))
	cl.ShardCount = 1
	cl.Remainder = true
	return out, cl
}

func dominantSupportPair(m map[string]int, n int) string {
	best, count := "", 0
	for k, v := range m {
		if v > count || (v == count && k < best) {
			best, count = k, v
		}
	}
	if n == 0 || float64(count)/float64(n) < 0.60 {
		return ""
	}
	return best
}
func countPrefix(labels map[int]string, p string) int {
	c := 0
	for _, l := range labels {
		if strings.HasPrefix(l, p) {
			c++
		}
	}
	return c
}
func uiFamily(labels map[int]string, n int) string {
	if n == 0 {
		return ""
	}
	prefixes := map[string]string{
		"MID_Sys_":                "system",
		"MID_H_Sys_":              "system",
		"MID_SysMenu_":            "system",
		"MID_Menu_":               "menu",
		"MID_H_Menu_":             "menu",
		"MID_GiftMenu_":           "gift-menu",
		"MID_ScoutInfo_":          "scout-info",
		"MID_Tips_":               "tips",
		"MID_TUT_":                "tutorial",
		"MATID_":                  "achievement",
		"MCID_":                   "chapter",
		"MID_Status_":             "status",
		"MID_Option_":             "options",
		"MID_Save_":               "save",
		"MID_Config_":             "config",
		"MID_Transporter_":        "transporter",
		"MID_BtlMenu_":            "system",
		"MID_CmdMenu_":            "system",
		"MID_CmdBtl_":             "system",
		"MID_GmapMenu_":           "system",
		"MID_Gmap_":               "gmap",
		"MID_Gmap":                "gmap",
		"MID_GmapMap_":            "gmap",
		"MID_VandahlMenu_":        "gmap",
		"MID_BanquetMenu_":        "gmap",
		"MID_LegionDispatchMenu_": "gmap",
		"MID_DeliveryAccident_":   "system",
		"MID_Construction_":       "system",
		"MID_LeyLine_":            "system",
		"MID_DeliveryGuard_":      "system",
		"MID_FT_":                 "facility",
		"MID_Map_Facility_":       "facility",
		"MID_Map_Area_":           "facility",
		"MID_DiningMenu_":         "facility",
		"MID_DivinationEnhance_":  "facility",
		"MID_Access_":             "facility",
		"MID_CharaMaking_":        "hub-menu",
		"MID_HeroMenu_":           "hub-menu",
		"MID_ObeliskMenu_":        "hub-menu",
		"MID_PerchMenu_":          "hub-menu",
		"MID_EXMenu_":             "hub-menu",
		"MID_TempleMenu_":         "hub-menu",
		"MID_QuestMenu_":          "hub-menu",
		"MID_ClassTrialMenu_":     "hub-menu",
		"MID_CorpsMenu_":          "gmap",
		"MID_LeyLineMenu_":        "gmap",
		"MID_LegionCombackMenu_":  "gmap",
		"MID_HubMenu_":            "system",
		"MID_ItemMenu_":           "system",
		"MID_StatusMenu_":         "system",
		"MID_Facility_":           "system",
		"MID_Dungeon_":            "system",
		"MID_AreaTelop_":          "system",
		"MID_Shop_":               "system",
		"MID_Arena_":              "system",
		"MID_Thermae_":            "system",
		"MID_Meal_":               "system",
		"MID_Scout_":              "system",
		"MID_Lesson_":             "system",
		"MID_RewindMsg_":          "system",
		"MID_RankingBtl_":         "system",
		"MID_WatchMenu_":          "system",
		"MID_ProfileMenu_":        "system",
		"MID_Divination_":         "system",
		"MID_Library_":            "system",
		"MID_Title_":              "title",
		"MID_ADVICE":              "advice",
		"MID_Articles_":           "articles",
		"MID_BattleTitle_":        "battle-title",
		"MID_Clan_":               "clan",
		"MID_DateTime_":           "datetime",
		"MID_Guide":               "guide",
		"MID_Obelisk_":            "obelisk",
		"MID_WINRULE_":            "rules",
		"MID_LOSERULE_":           "rules",
		"MID_Rule_":               "rules",
		"MID_LevelUp_":            "levelup",
		"MID_Skill_":              "levelup",
		"MID_Likes_":              "likes",
		"MID_Strengths_":          "likes",
		"MID_H_Spot":              "spot",
		"MID_Spot_":               "spot",
		"MID_EventLocation_":      "spot",
		"MID_Stage_":              "theater",
		"MID_Calendar_":           "notice",
		"MID_HonorLv":             "notice",
		"MID_NewAction_":          "notice",
	}
	// Longest specific prefix wins regardless of map iteration order.
	type pfx struct {
		p string
		f string
	}
	var list []pfx
	for p, f := range prefixes {
		list = append(list, pfx{p, f})
	}
	sort.Slice(list, func(i, j int) bool {
		if len(list[i].p) != len(list[j].p) {
			return len(list[i].p) > len(list[j].p)
		}
		return list[i].p < list[j].p
	})
	counts := map[string]int{}
	total := 0
	for _, l := range labels {
		for _, x := range list {
			if strings.HasPrefix(l, x.p) {
				counts[x.f]++
				total++
				break
			}
		}
	}
	if float64(total)/float64(n) < 0.60 {
		return ""
	}
	best, c := "", 0
	for k, v := range counts {
		if v > c || (v == c && k < best) {
			best, c = k, v
		}
	}
	return best
}

func groupBy(records []Record, key func(int) string) []struct {
	key string
	idx []int
} {
	m := map[string][]int{}
	order := []string{}
	for i := range records {
		k := key(i)
		if _, ok := m[k]; !ok {
			order = append(order, k)
		}
		m[k] = append(m[k], i)
	}
	sort.Strings(order)
	out := make([]struct {
		key string
		idx []int
	}, 0, len(order))
	for _, k := range order {
		out = append(out, struct {
			key string
			idx []int
		}{k, m[k]})
	}
	return out
}
func safeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
func rangeInts(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
func sortGroups(g []ShardGroup) {
	sort.Slice(g, func(i, j int) bool { return g[i].Path < g[j].Path })
}
