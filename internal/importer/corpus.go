package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fefw/internal/elfdata"
	"fefw/internal/msbt"
)

// Side is one locale's parsed view of a source bank.
type Side struct {
	Locale         string
	AssetPath      string
	CompressedSHA  string
	CompressedSize int64
	Decompressed   []byte
	File           *msbt.File
	Project        *msbt.Project
	Labels         map[uint32]string
	AliasCount     map[uint32]int
	Collisions     []uint32
	BadBuckets     int
	Text           []string
	TokenStats     []msbt.TokenStats
	Attrs          [][]msbt.AttrValue
}

// Bank is a JP/US EN bank pair.
type Bank struct {
	Name string
	JP   *Side
	EN   *Side
}

// Corpus is the loaded and validated source corpus.
type Corpus struct {
	Banks      []Bank
	Talk       *elfdata.TalkMsg
	TalkSHA    string
	TalkErr    string
	JPMSBPPath string
	ENMSBPPath string
	JPMSBPSHA  string
	ENMSBPSHA  string
	JPProject  *msbt.Project
	ENProject  *msbt.Project
}

// LoadCorpus loads all MSBT banks, both MSBPs and TalkMsg. It fails closed on
// malformed text/attribute structures; TalkMsg failure is recorded but does
// not invalidate text import.
func LoadCorpus(c Config) (*Corpus, error) {
	jpDir := filepath.Join(c.msgRoot(), "JPja")
	enDir := filepath.Join(c.msgRoot(), "USen")
	jpNames, err := listMSBT(jpDir)
	if err != nil {
		return nil, err
	}
	enNames, err := listMSBT(enDir)
	if err != nil {
		return nil, err
	}
	if strings.Join(jpNames, "\n") != strings.Join(enNames, "\n") {
		return nil, fmt.Errorf("JP/US EN bank basename sets differ")
	}
	jpMSBP := filepath.Join(jpDir, "Msg.msbp")
	enMSBP := filepath.Join(enDir, "Msg.msbp")
	jpProject, jpHash, err := loadProject(jpMSBP)
	if err != nil {
		return nil, fmt.Errorf("JP Msg.msbp: %w", err)
	}
	enProject, enHash, err := loadProject(enMSBP)
	if err != nil {
		return nil, fmt.Errorf("US EN Msg.msbp: %w", err)
	}
	corp := &Corpus{
		JPMSBPPath: filepath.ToSlash(relTo(c.ProjectRoot, jpMSBP)),
		ENMSBPPath: filepath.ToSlash(relTo(c.ProjectRoot, enMSBP)),
		JPMSBPSHA:  jpHash,
		ENMSBPSHA:  enHash,
		JPProject:  jpProject,
		ENProject:  enProject,
	}
	for _, name := range jpNames {
		jp, err := loadSide(c, "JPja", "Msg/JPja/"+name+".msbt.zst", jpProject)
		if err != nil {
			return nil, fmt.Errorf("JP %s: %w", name, err)
		}
		en, err := loadSide(c, "USen", "Msg/USen/"+name+".msbt.zst", enProject)
		if err != nil {
			return nil, fmt.Errorf("US EN %s: %w", name, err)
		}
		corp.Banks = append(corp.Banks, Bank{Name: name, JP: jp, EN: en})
	}
	if tm, sha, err := LoadTalkMsg(c); err == nil {
		corp.Talk = tm
		corp.TalkSHA = sha
	} else {
		corp.TalkErr = err.Error()
	}
	return corp, nil
}

func listMSBT(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".msbt.zst") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".msbt.zst"))
	}
	sort.Strings(out)
	return out, nil
}

func loadProject(path string) (*msbt.Project, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	p, err := msbt.ParseProject(data)
	if err != nil {
		return nil, "", err
	}
	return p, shaBytes(data), nil
}

func loadSide(c Config, locale, assetPath string, project *msbt.Project) (*Side, error) {
	full := filepath.Join(c.SourceRoot, "romfs", filepath.FromSlash(assetPath))
	comp, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	raw, err := DecompressZstd(comp)
	if err != nil {
		return nil, fmt.Errorf("zstd: %w", err)
	}
	f, err := msbt.Parse(raw)
	if err != nil {
		return nil, err
	}
	if f.TXT2 == nil {
		return nil, fmt.Errorf("missing TXT2")
	}
	if f.LBL1 == nil {
		return nil, fmt.Errorf("missing LBL1")
	}
	s := &Side{
		Locale:         locale,
		AssetPath:      assetPath,
		CompressedSHA:  shaBytes(comp),
		CompressedSize: int64(len(comp)),
		Decompressed:   raw,
		File:           f,
		Project:        project,
		Labels:         map[uint32]string{},
		AliasCount:     map[uint32]int{},
		BadBuckets:     len(f.LBL1.BadBucket),
		Collisions:     f.LBL1.Collisions(),
	}
	for i := range f.TXT2.Strings {
		label, aliases := f.LBL1.CanonicalLabel(uint32(i))
		s.Labels[uint32(i)] = label
		s.AliasCount[uint32(i)] = aliases
		enc, st, err := msbt.EncodeText(f.TXT2.Strings[i])
		if err != nil {
			return nil, fmt.Errorf("text %d tokenization: %w", i, err)
		}
		s.Text = append(s.Text, enc)
		s.TokenStats = append(s.TokenStats, st)
	}
	if f.ATR1 != nil && f.ATO1 != nil && project != nil && project.Schema != nil {
		s.Attrs, err = f.ATR1.ExtractAttributes(f.ATO1, project.Schema)
		if err != nil {
			return nil, fmt.Errorf("attributes: %w", err)
		}
	}
	return s, nil
}

func relTo(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}

// FindAttr returns the first attribute with the given name.
func FindAttr(row []msbt.AttrValue, name string) (msbt.AttrValue, bool) {
	for _, v := range row {
		if v.Name == name {
			return v, true
		}
	}
	return msbt.AttrValue{}, false
}
