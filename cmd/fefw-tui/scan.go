package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"fefw/internal/importer"
)

const detailLimit = 6

type shardLite struct {
	SchemaVersion int          `json:"schema_version"`
	Records       []recordLite `json:"records"`
}

type recordLite struct {
	RecordID string `json:"record_id"`
	Function string `json:"function"`
	Text     struct {
		JP         *string `json:"jp"`
		OfficialEN *string `json:"official_en"`
		RealEN     *string `json:"real_en"`
	} `json:"text"`
	Review struct {
		Status string `json:"status"`
	} `json:"review"`
}

type scanProgress struct {
	Done    int
	Total   int
	Records int
	Current string
}

type scanResult struct {
	ManifestPath string
	ManifestSHA  string
	Recomputed   string

	ManifestFiles      int
	ManifestRecords    int
	ManifestJP         int
	ManifestOfficialEN int
	ManifestRealEN     int
	ByType             map[string]int
	FilesByType        map[string]int
	ByReview           map[string]int

	Files        int
	Records      int
	JP           int
	OfficialEN   int
	RealEN       int
	CheckedBytes int64

	Missing       []string
	HashMismatch  []string
	CountMismatch []string
	SchemaErrors  []string
	DuplicateIDs  []string
	Extra         []string
	Symlinks      []string
	ReadErrors    []string

	MissingCount       int
	HashMismatchCount  int
	CountMismatchCount int
	SchemaErrorCount   int
	DuplicateIDCount   int
	ExtraCount         int
	SymlinkCount       int
	ReadErrorCount     int

	Started  time.Time
	Finished time.Time
}

func (r *scanResult) Elapsed() time.Duration {
	if r == nil || r.Finished.IsZero() {
		return 0
	}
	return r.Finished.Sub(r.Started)
}

func (r *scanResult) OK() bool {
	if r == nil {
		return false
	}
	return r.HashMismatchCount == 0 &&
		r.CountMismatchCount == 0 &&
		r.MissingCount == 0 &&
		r.ExtraCount == 0 &&
		r.SchemaErrorCount == 0 &&
		r.DuplicateIDCount == 0 &&
		r.SymlinkCount == 0 &&
		r.ReadErrorCount == 0 &&
		r.ManifestSHA != "" &&
		r.ManifestSHA == r.Recomputed
}

func (r *scanResult) addDetail(dst *[]string, count *int, item string) {
	*count = *count + 1
	if len(*dst) < detailLimit {
		*dst = append(*dst, item)
	}
}

func shaBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func loadManifest(root string) (*importer.Manifest, string, error) {
	p := manifestPath(root)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, "", err
	}
	var man importer.Manifest
	if err := json.Unmarshal(b, &man); err != nil {
		return nil, "", fmt.Errorf("manifest: %w", err)
	}
	sum := sha256.Sum256(b)
	return &man, hex.EncodeToString(sum[:]), nil
}

func scanCanonical(root string, progress func(scanProgress)) (*scanResult, error) {
	return scanCanonicalContext(context.Background(), root, progress)
}

// scanCanonicalContext is scanCanonical with cooperative cancellation between
// files. Cancellation never changes what is read or written: it only stops the
// read-only walk early.
func scanCanonicalContext(ctx context.Context, root string, progress func(scanProgress)) (*scanResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	man, manSHA, err := loadManifest(root)
	if err != nil {
		return nil, err
	}
	res := &scanResult{
		ManifestPath:       manifestPath(root),
		ManifestSHA:        manSHA,
		ManifestFiles:      man.Counts.Files,
		ManifestRecords:    man.Counts.Records,
		ManifestJP:         man.Counts.JP,
		ManifestOfficialEN: man.Counts.OfficialEN,
		ManifestRealEN:     man.Counts.RealEN,
		ByType:             cloneMap(man.Counts.ByType),
		FilesByType:        map[string]int{},
		ByReview:           cloneMap(man.Counts.ByReviewStatus),
		Started:            time.Now(),
	}
	rec := &importer.Manifest{
		SchemaVersion: 1,
		Counts: importer.ManifestCounts{
			ByType:         map[string]int{},
			ByReviewStatus: map[string]int{},
		},
	}
	ids := make(map[string]struct{}, man.Counts.Records)
	seenPaths := make(map[string]struct{}, len(man.Files))
	files := append([]importer.ManifestFile(nil), man.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	total := len(files)
	for i, mf := range files {
		if err := ctx.Err(); err != nil {
			res.Finished = time.Now()
			return res, err
		}
		if progress != nil {
			progress(scanProgress{Done: i, Total: total, Records: res.Records, Current: mf.Path})
		}
		if !safeManifestPath(mf.Path) {
			res.addDetail(&res.SchemaErrors, &res.SchemaErrorCount, mf.Path+": unsafe relative path")
			continue
		}
		if _, dup := seenPaths[mf.Path]; dup {
			res.addDetail(&res.SchemaErrors, &res.SchemaErrorCount, mf.Path+": duplicate manifest path")
			continue
		}
		seenPaths[mf.Path] = struct{}{}

		full := filepath.Join(translationsDir(root), filepath.FromSlash(mf.Path))
		info, statErr := os.Lstat(full)
		if os.IsNotExist(statErr) {
			res.addDetail(&res.Missing, &res.MissingCount, mf.Path)
			continue
		}
		if statErr != nil {
			res.addDetail(&res.ReadErrors, &res.ReadErrorCount, mf.Path+": "+statErr.Error())
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			res.addDetail(&res.Symlinks, &res.SymlinkCount, mf.Path)
			continue
		}
		if !info.Mode().IsRegular() {
			res.addDetail(&res.SchemaErrors, &res.SchemaErrorCount, mf.Path+": not a regular file")
			continue
		}
		b, readErr := os.ReadFile(full)
		if readErr != nil {
			res.addDetail(&res.ReadErrors, &res.ReadErrorCount, mf.Path+": "+readErr.Error())
			continue
		}
		res.CheckedBytes += int64(len(b))
		sum := sha256.Sum256(b)
		fileSHA := hex.EncodeToString(sum[:])
		if !strings.EqualFold(fileSHA, mf.SHA256) {
			res.addDetail(&res.HashMismatch, &res.HashMismatchCount, mf.Path)
		}

		var top map[string]json.RawMessage
		if err := json.Unmarshal(b, &top); err != nil {
			res.addDetail(&res.SchemaErrors, &res.SchemaErrorCount, mf.Path+": invalid JSON")
			continue
		}
		if len(top) != 2 {
			res.addDetail(&res.SchemaErrors, &res.SchemaErrorCount, mf.Path+": top-level keys")
			continue
		}
		if _, ok := top["schema_version"]; !ok {
			res.addDetail(&res.SchemaErrors, &res.SchemaErrorCount, mf.Path+": missing schema_version")
			continue
		}
		if _, ok := top["records"]; !ok {
			res.addDetail(&res.SchemaErrors, &res.SchemaErrorCount, mf.Path+": missing records")
			continue
		}
		var sh shardLite
		if err := json.Unmarshal(b, &sh); err != nil {
			res.addDetail(&res.SchemaErrors, &res.SchemaErrorCount, mf.Path+": "+err.Error())
			continue
		}
		if sh.SchemaVersion != 1 {
			res.addDetail(&res.SchemaErrors, &res.SchemaErrorCount, fmt.Sprintf("%s: schema_version %d", mf.Path, sh.SchemaVersion))
		}
		if len(sh.Records) != mf.Records {
			res.addDetail(&res.CountMismatch, &res.CountMismatchCount, fmt.Sprintf("%s: records %d != manifest %d", mf.Path, len(sh.Records), mf.Records))
		}
		typ := "unclassified"
		if len(sh.Records) > 0 {
			typ = sh.Records[0].Function
		}
		if typ != mf.Type {
			res.addDetail(&res.SchemaErrors, &res.SchemaErrorCount, fmt.Sprintf("%s: type %q != manifest %q", mf.Path, typ, mf.Type))
		}
		rec.Files = append(rec.Files, importer.ManifestFile{
			Path: mf.Path, SHA256: fileSHA, Records: len(sh.Records), Type: typ,
		})
		rec.Counts.Files++
		res.Files++
		res.FilesByType[mf.Type]++
		for _, r := range sh.Records {
			res.Records++
			rec.Counts.Records++
			if r.RecordID == "" {
				res.addDetail(&res.SchemaErrors, &res.SchemaErrorCount, mf.Path+": empty record_id")
			} else if _, ok := ids[r.RecordID]; ok {
				res.addDetail(&res.DuplicateIDs, &res.DuplicateIDCount, r.RecordID)
			} else {
				ids[r.RecordID] = struct{}{}
			}
			if r.Text.JP != nil {
				res.JP++
				rec.Counts.JP++
			}
			if r.Text.OfficialEN != nil {
				res.OfficialEN++
				rec.Counts.OfficialEN++
			}
			if r.Text.RealEN != nil {
				res.RealEN++
				rec.Counts.RealEN++
			}
			rec.Counts.ByType[r.Function]++
			rec.Counts.ByReviewStatus[r.Review.Status]++
		}
	}
	rec.Counts.MissingJP = rec.Counts.Records - rec.Counts.JP
	rec.Counts.MissingOfficialEN = rec.Counts.Records - rec.Counts.OfficialEN

	if man.SchemaVersion != 1 {
		res.addDetail(&res.SchemaErrors, &res.SchemaErrorCount, fmt.Sprintf("manifest schema_version %d", man.SchemaVersion))
	}
	if !equalCounts(man.Counts, rec.Counts) {
		res.addDetail(&res.CountMismatch, &res.CountMismatchCount, "recomputed manifest counts differ")
	}
	recomputedBytes, err := marshalManifest(rec)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		res.Finished = time.Now()
		return res, err
	}
	sum := sha256.Sum256(recomputedBytes)
	res.Recomputed = hex.EncodeToString(sum[:])

	extra, symlinks, walkErr := extraFiles(root, seenPaths)
	if walkErr != nil {
		return nil, walkErr
	}
	for _, p := range extra {
		res.addDetail(&res.Extra, &res.ExtraCount, p)
	}
	for _, p := range symlinks {
		res.addDetail(&res.Symlinks, &res.SymlinkCount, p)
	}
	res.Finished = time.Now()
	if progress != nil {
		progress(scanProgress{Done: total, Total: total, Records: res.Records, Current: ""})
	}
	return res, nil
}

func extraFiles(root string, known map[string]struct{}) ([]string, []string, error) {
	var extra, symlinks []string
	err := filepath.WalkDir(translationsDir(root), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(translationsDir(root), p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if _, ok := known[rel]; !ok {
				symlinks = append(symlinks, rel)
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if rel == "manifest.json" || strings.HasPrefix(rel, "variants/") || strings.Contains("/"+rel, "/variants/") {
			return nil
		}
		if _, ok := known[rel]; !ok {
			extra = append(extra, rel)
		}
		return nil
	})
	sort.Strings(extra)
	sort.Strings(symlinks)
	return extra, symlinks, err
}

func safeManifestPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	clean := path.Clean(p)
	if clean != p || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	return true
}

func equalCounts(a, b importer.ManifestCounts) bool {
	if a.Files != b.Files || a.Records != b.Records || a.JP != b.JP ||
		a.OfficialEN != b.OfficialEN || a.RealEN != b.RealEN ||
		a.MissingJP != b.MissingJP || a.MissingOfficialEN != b.MissingOfficialEN {
		return false
	}
	if !reflect.DeepEqual(nonNil(a.ByType), nonNil(b.ByType)) {
		return false
	}
	return reflect.DeepEqual(nonNil(a.ByReviewStatus), nonNil(b.ByReviewStatus))
}

func nonNil(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	return m
}

func cloneMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func marshalManifest(m *importer.Manifest) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
