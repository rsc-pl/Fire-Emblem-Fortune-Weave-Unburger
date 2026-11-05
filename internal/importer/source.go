package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fefw/internal/elfdata"
	"fefw/internal/zstd"
)

// Config holds all paths used by the importer. All paths are relative to the
// project root unless explicitly absolute; reports never echo absolute paths.
type Config struct {
	ProjectRoot     string
	SourceRoot      string
	TranslationsDir string
	EvidenceDir     string
	ReportsDir      string
	WorkDir         string
	DryRun          bool
	Recompress      bool
}

// DefaultConfig returns the project-relative default configuration.
func DefaultConfig() Config {
	return Config{
		ProjectRoot:     ".",
		SourceRoot:      ".local/_incoming/fefw-original-sources",
		TranslationsDir: "translations",
		EvidenceDir:     ".local/technical/evidence",
		ReportsDir:      ".local/reports",
		WorkDir:         ".local/work",
	}
}

func (c Config) msgRoot() string   { return filepath.Join(c.SourceRoot, "romfs", "Msg") }
func (c Config) exefsRoot() string { return filepath.Join(c.SourceRoot, "exefs") }

// FileEntry is one inventoried source file. Path is relative to the project
// root (with forward slashes) so no user-specific absolute path is recorded.
type FileEntry struct {
	Path   string `json:"path"`
	Role   string `json:"role"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Locale string `json:"locale,omitempty"`
	Bank   string `json:"bank,omitempty"`
}

// Identity records only what can be read from the staged files.
type Identity struct {
	Platform      string `json:"platform"`
	ProgramID     string `json:"program_id"`
	ProgramSource string `json:"program_id_source"`
	TitleID       string `json:"title_id"`
	Version       string `json:"version"`
	Region        string `json:"region"`
	Edition       string `json:"edition"`
	NPDMStatus    string `json:"npdm_status"`
}

// InventoryReport is the private inventory summary.
type InventoryReport struct {
	SchemaVersion int             `json:"schema_version"`
	SourceRoot    string          `json:"source_root"`
	FileCount     int             `json:"file_count"`
	TotalBytes    int64           `json:"total_bytes"`
	Counts        map[string]int  `json:"counts"`
	Presence      map[string]bool `json:"presence"`
	Files         []FileEntry     `json:"files"`
	Identity      Identity        `json:"identity"`
}

// PlatformVerification is one platform entry in the hash ledger.
type PlatformVerification struct {
	Platform     string `json:"platform"`
	Verification string `json:"verification"`
	Note         string `json:"note"`
}

// Ledger is the private source hash ledger. It follows the project's
// SOURCE_HASH_LEDGER_TEMPLATE shape and adds source identity.
type Ledger struct {
	SchemaVersion     int                    `json:"schema_version"`
	SourceInventory   []FileEntry            `json:"source_inventory"`
	VerifiedPlatforms []PlatformVerification `json:"verified_platforms"`
	SourceEdition     string                 `json:"source_edition"`
	RoundtripStatus   string                 `json:"roundtrip_status"`
	Notes             string                 `json:"notes"`
	SourceIdentity    Identity               `json:"source_identity"`
}

// HashFile returns the SHA-256 and size of a file.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func shaBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// DecompressZstd decompresses one zstd frame.
func DecompressZstd(data []byte) ([]byte, error) {
	r := zstd.NewReader(bytes.NewReader(data))
	return io.ReadAll(r)
}

// Inventory walks the staged source tree and hashes every regular file. It
// never follows symlinks.
func Inventory(c Config) (*InventoryReport, error) {
	root := c.SourceRoot
	rep := &InventoryReport{
		SchemaVersion: 1,
		SourceRoot:    filepath.ToSlash(root),
		Counts:        map[string]int{},
		Presence:      map[string]bool{},
		Identity: Identity{
			Platform:   "switch2",
			TitleID:    "UNKNOWN",
			Version:    "UNKNOWN",
			Region:     "UNKNOWN",
			Edition:    "UNKNOWN",
			NPDMStatus: "ABSENT",
		},
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			rep.Presence["symlink_skipped"] = true
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(c.ProjectRoot, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		sum, size, err := HashFile(path)
		if err != nil {
			return err
		}
		entry := FileEntry{Path: rel, Size: size, SHA256: sum}
		classify(&entry, path, root)
		rep.Files = append(rep.Files, entry)
		rep.Counts[entry.Role]++
		rep.FileCount++
		rep.TotalBytes += size
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(rep.Files, func(i, j int) bool { return rep.Files[i].Path < rep.Files[j].Path })
	rep.Presence["msbt_jp"] = rep.Counts["jp_msbt"] == 523
	rep.Presence["msbt_usen"] = rep.Counts["en_msbt"] == 523
	rep.Presence["msbp_pair"] = rep.Counts["jp_msbp"] == 1 && rep.Counts["en_msbp"] == 1
	rep.Presence["talkmsg"] = rep.Counts["talkmsg"] > 0
	rep.Presence["gamedata"] = rep.Counts["gamedata"] > 0
	rep.Presence["script"] = rep.Counts["script"] > 0
	rep.Presence["sound_metadata"] = rep.Counts["sound"] > 0
	rep.Presence["exefs"] = rep.Counts["exefs"] > 0
	rep.Presence["map_data"] = rep.Counts["map_data"] > 0
	rep.Presence["font"] = rep.Counts["font"] > 0
	rep.Presence["layout"] = rep.Counts["layout"] > 0

	// Read the NPDM only for identity; it is not interpreted as a consumer.
	npdm := filepath.Join(c.exefsRoot(), "main.npdm")
	if data, err := os.ReadFile(npdm); err == nil {
		if pid, ok := parseNPDMProgramID(data); ok {
			rep.Identity.ProgramID = pid
			rep.Identity.ProgramSource = "exefs/main.npdm ACI0"
			rep.Identity.NPDMStatus = "PARSED"
		} else {
			rep.Identity.NPDMStatus = "UNPARSED"
		}
	}
	return rep, nil
}

func classify(e *FileEntry, full, sourceRoot string) {
	rel, _ := filepath.Rel(sourceRoot, full)
	rel = filepath.ToSlash(rel)
	switch {
	case strings.HasPrefix(rel, "romfs/Msg/JPja/") && strings.HasSuffix(rel, ".msbt.zst"):
		e.Role, e.Locale = "jp_msbt", "JPja"
		e.Bank = strings.TrimSuffix(filepath.Base(rel), ".msbt.zst")
	case strings.HasPrefix(rel, "romfs/Msg/USen/") && strings.HasSuffix(rel, ".msbt.zst"):
		e.Role, e.Locale = "en_msbt", "USen"
		e.Bank = strings.TrimSuffix(filepath.Base(rel), ".msbt.zst")
	case rel == "romfs/Msg/JPja/Msg.msbp":
		e.Role, e.Locale = "jp_msbp", "JPja"
	case rel == "romfs/Msg/USen/Msg.msbp":
		e.Role, e.Locale = "en_msbp", "USen"
	case rel == "romfs/Msg/TalkMsg.elf.zst":
		e.Role = "talkmsg"
	case strings.HasPrefix(rel, "romfs/GameData/"):
		e.Role = "gamedata"
	case strings.HasPrefix(rel, "romfs/Script/"):
		e.Role = "script"
	case strings.HasPrefix(rel, "romfs/Sound/"):
		e.Role = "sound"
	case strings.HasPrefix(rel, "romfs/Map/"):
		e.Role = "map_data"
	case strings.HasPrefix(rel, "romfs/Font/"):
		e.Role = "font"
	case strings.HasPrefix(rel, "romfs/Layout/"):
		e.Role = "layout"
	case rel == "romfs/Fs.bin.zst":
		e.Role = "fs_table"
	case strings.HasPrefix(rel, "exefs/"):
		e.Role = "exefs"
	default:
		e.Role = "other"
	}
}

// parseNPDMProgramID reads the ACI0 program id from a Nintendo NPDM file.
func parseNPDMProgramID(data []byte) (string, bool) {
	if len(data) < 0x80 || string(data[0:4]) != "META" {
		return "", false
	}
	aciOff := int(le32(data[0x70:0x74]))
	aciSize := int(le32(data[0x74:0x78]))
	if aciOff <= 0 || aciSize < 0x18 || aciOff+0x18 > len(data) {
		return "", false
	}
	if string(data[aciOff:aciOff+4]) != "ACI0" {
		return "", false
	}
	pid := data[aciOff+0x10 : aciOff+0x18]
	var v uint64
	for _, b := range pid {
		v = v<<8 | uint64(b)
	}
	if v == 0 {
		return "", false
	}
	return fmt.Sprintf("%016x", v), true
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// LoadLedger reads the private source hash ledger if present.
func LoadLedger(c Config) (*Ledger, error) {
	path := filepath.Join(c.EvidenceDir, "SOURCE_HASH_LEDGER.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Ledger
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// VerifyLedger rejects any inventory difference from the recorded source.
// The only way to accept a new source build is to deliberately regenerate the
// ledger with the inventory command after independent verification.
func VerifyLedger(inv *InventoryReport, led *Ledger) error {
	if led == nil {
		return nil
	}
	old := map[string]FileEntry{}
	for _, f := range led.SourceInventory {
		if _, ok := old[f.Path]; ok {
			return fmt.Errorf("source ledger contains duplicate path %s", f.Path)
		}
		old[f.Path] = f
	}
	if len(old) != len(inv.Files) {
		return fmt.Errorf("source inventory changed: ledger=%d files, current=%d; regenerate the ledger only after verifying the new source", len(old), len(inv.Files))
	}
	for _, f := range inv.Files {
		o, ok := old[f.Path]
		if !ok {
			return fmt.Errorf("source file not in ledger: %s", f.Path)
		}
		if o.SHA256 != f.SHA256 || o.Size != f.Size {
			return fmt.Errorf("source hash changed: %s (ledger %s/%d, current %s/%d); blocked as an unverified source change", f.Path, o.SHA256, o.Size, f.SHA256, f.Size)
		}
	}
	return nil
}

// JSON writes deterministic, HTML-unescaped, indented JSON.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadTalkMsg reads and parses the decompressed TalkMsg ELF.
func LoadTalkMsg(c Config) (*elfdata.TalkMsg, string, error) {
	p := filepath.Join(c.msgRoot(), "TalkMsg.elf.zst")
	comp, err := os.ReadFile(p)
	if err != nil {
		return nil, "", err
	}
	raw, err := DecompressZstd(comp)
	if err != nil {
		return nil, "", fmt.Errorf("TalkMsg.elf.zst: %w", err)
	}
	tm, err := elfdata.ParseTalkMsg(raw)
	if err != nil {
		return nil, "", err
	}
	return tm, shaBytes(comp), nil
}
