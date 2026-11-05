package importer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// listJSONShards returns every canonical shard JSON under root in sorted path
// order, excluding manifest.json and the variants overlay.
func listJSONShards(root string) ([]shardFile, error) {
	var out []shardFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".json") || d.Name() == "manifest.json" {
			return nil
		}
		if strings.Contains(filepath.ToSlash(path), "/variants/") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, shardFile{Path: filepath.ToSlash(rel), Bytes: b})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// ForEachShard reads every canonical shard and calls fn. It is used by the
// private audit tools; it never writes.
func ForEachShard(root string, fn func(path string, sh Shard)) error {
	files, err := listJSONShards(root)
	if err != nil {
		return err
	}
	for _, f := range files {
		var sh Shard
		if err := json.Unmarshal(f.Bytes, &sh); err != nil {
			return err
		}
		fn(f.Path, sh)
	}
	return nil
}
