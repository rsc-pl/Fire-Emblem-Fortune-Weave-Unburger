package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type buildInfo struct {
	BinaryPath    string
	BinarySize    int64
	BinaryMod     time.Time
	BinaryPresent bool
	Fingerprint   string
	FPMod         time.Time
	FPPresent     bool
	SourceDigest  string
	SourceFiles   int
	SourceErr     string
	Runtime       string
}

func inspectBuild(root string) buildInfo {
	bi := buildInfo{
		BinaryPath: filepath.Join(root, ".local", "bin", "fefw-tui"),
		Runtime:    fmt.Sprintf("%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH),
	}
	if info, err := os.Stat(bi.BinaryPath); err == nil {
		bi.BinaryPresent = true
		bi.BinarySize = info.Size()
		bi.BinaryMod = info.ModTime()
	}
	fp := filepath.Join(root, ".local", "state", "fefw-tui", "fingerprint")
	if b, err := os.ReadFile(fp); err == nil {
		bi.FPPresent = true
		bi.Fingerprint = strings.TrimSpace(string(b))
		if info, err := os.Stat(fp); err == nil {
			bi.FPMod = info.ModTime()
		}
	}
	digest, n, err := sourceDigest(root)
	if err != nil {
		bi.SourceErr = err.Error()
	} else {
		bi.SourceDigest = digest
		bi.SourceFiles = n
	}
	return bi
}

// sourceDigest hashes the same source set used by START.sh: the TUI command,
// the internal packages it depends on, and the module files. It has no
// dependency on translation JSON or file modification times.
func sourceDigest(root string) (string, int, error) {
	files, err := sourceFiles(root)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", 0, err
		}
		sum := sha256.Sum256(b)
		fmt.Fprintf(h, "%s %s\n", rel, hex.EncodeToString(sum[:]))
	}
	return hex.EncodeToString(h.Sum(nil)), len(files), nil
}

func sourceFiles(root string) ([]string, error) {
	var files []string
	add := func(rel string) {
		if rel != "" {
			files = append(files, rel)
		}
	}
	for _, base := range []string{"cmd/fefw-tui", "internal"} {
		dir := filepath.Join(root, filepath.FromSlash(base))
		err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return relErr
			}
			add(filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, rel := range []string{"go.mod", "go.sum"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			add(rel)
		}
	}
	sort.Strings(files)
	return files, nil
}
