package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const stubSource = `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	fmt.Println("STUB", strings.Join(os.Args[1:], "|"))
}
`

type fixture struct {
	dir      string
	start    string
	buildLog string
	env      []string
	bash     string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not available")
	}
	dir := filepath.Join(t.TempDir(), "project with spaces")
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "fefw-tui"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher, err := os.ReadFile(filepath.Join("..", "..", "START.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "START.sh"), launcher, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fefw\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmd", "fefw-tui", "main.go"), []byte(stubSource), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(t.TempDir(), "toolbin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	buildLog := filepath.Join(t.TempDir(), "build.log")
	wrapper := "#!/usr/bin/env bash\n" +
		"printf '%s\\n' \"$*\" >> \"$BUILD_LOG\"\n" +
		"exec " + fmt.Sprintf("%q", realGo) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "go"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}

	env := append([]string{}, os.Environ()...)
	env = append(env,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BUILD_LOG="+buildLog,
		"GOFLAGS=",
		"FEFW_BUILD_TAGS=",
		"FEFW_GO=",
		"NO_COLOR=1",
	)
	return &fixture{dir: dir, start: filepath.Join(dir, "START.sh"), buildLog: buildLog, env: env, bash: bash}
}

func (f *fixture) run(t *testing.T, cwd string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(f.bash, f.start)
	cmd.Args = append(cmd.Args, args...)
	cmd.Dir = cwd
	cmd.Env = f.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run launcher: %v", err)
		}
	}
	return stdout.String(), stderr.String(), code
}

func (f *fixture) buildCount(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(f.buildLog)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "build ") {
			n++
		}
	}
	return n
}

func (f *fixture) binary(t *testing.T) string {
	t.Helper()
	return filepath.Join(f.dir, ".local", "bin", "fefw-tui")
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestLauncherBuildsMissingBinaryAndFastPaths(t *testing.T) {
	f := newFixture(t)
	out, errOut, code := f.run(t, t.TempDir())
	if code != 0 || !strings.Contains(out, "STUB") {
		t.Fatalf("first run: code=%d out=%q err=%q", code, out, errOut)
	}
	if f.buildCount(t) != 1 {
		t.Fatalf("first run build count=%d", f.buildCount(t))
	}
	out, errOut, code = f.run(t, t.TempDir())
	if code != 0 || !strings.Contains(out, "STUB") {
		t.Fatalf("second run: code=%d out=%q err=%q", code, out, errOut)
	}
	if f.buildCount(t) != 1 {
		t.Fatalf("second run unexpectedly rebuilt: count=%d", f.buildCount(t))
	}
	if strings.Contains(errOut, "building fefw-tui") {
		t.Fatalf("fast path printed build message: %q", errOut)
	}
}

func TestLauncherRebuildsOnSourceAndConfigChange(t *testing.T) {
	f := newFixture(t)
	if _, errOut, code := f.run(t, f.dir); code != 0 {
		t.Fatalf("initial build failed: %d %s", code, errOut)
	}
	if f.buildCount(t) != 1 {
		t.Fatalf("initial count=%d", f.buildCount(t))
	}

	mainPath := filepath.Join(f.dir, "cmd", "fefw-tui", "main.go")
	appendFile(t, mainPath, "\n// source change\n")
	if _, errOut, code := f.run(t, f.dir); code != 0 {
		t.Fatalf("source-change run failed: %d %s", code, errOut)
	}
	if f.buildCount(t) != 2 {
		t.Fatalf("source change did not rebuild: count=%d", f.buildCount(t))
	}

	appendFile(t, filepath.Join(f.dir, "go.mod"), "\n// config change\n")
	if _, errOut, code := f.run(t, f.dir); code != 0 {
		t.Fatalf("config-change run failed: %d %s", code, errOut)
	}
	if f.buildCount(t) != 3 {
		t.Fatalf("config change did not rebuild: count=%d", f.buildCount(t))
	}
}

func TestLauncherForcedRebuild(t *testing.T) {
	f := newFixture(t)
	if _, _, code := f.run(t, f.dir); code != 0 {
		t.Fatalf("initial build failed: %d", code)
	}
	if _, errOut, code := f.run(t, f.dir, "--rebuild"); code != 0 {
		t.Fatalf("forced rebuild failed: %d %s", code, errOut)
	}
	if f.buildCount(t) != 2 {
		t.Fatalf("forced rebuild count=%d", f.buildCount(t))
	}
}

func TestLauncherBuildFailurePreservesOldBinary(t *testing.T) {
	f := newFixture(t)
	if _, errOut, code := f.run(t, f.dir); code != 0 {
		t.Fatalf("initial build failed: %d %s", code, errOut)
	}
	before := hashFile(t, f.binary(t))

	mainPath := filepath.Join(f.dir, "cmd", "fefw-tui", "main.go")
	original, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainPath, []byte("package main\nfunc main() {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := f.run(t, f.dir, "--rebuild")
	if code != 5 {
		t.Fatalf("failed build exit=%d, want 5; stderr=%q", code, errOut)
	}
	if !strings.Contains(errOut, "previous binary was left untouched") {
		t.Fatalf("missing failure guidance: %q", errOut)
	}
	after := hashFile(t, f.binary(t))
	if before != after {
		t.Fatalf("old binary changed after failed build")
	}
	if err := os.WriteFile(mainPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, code := f.run(t, f.dir)
	if code != 0 || !strings.Contains(out, "STUB") {
		t.Fatalf("old binary did not run: %d %q", code, out)
	}
	if f.buildCount(t) != 2 {
		t.Fatalf("restored source should fast-path; count=%d", f.buildCount(t))
	}
}

func TestLauncherCheckMode(t *testing.T) {
	f := newFixture(t)
	if _, _, code := f.run(t, f.dir, "--check"); code != 1 {
		t.Fatalf("check before build=%d, want 1", code)
	}
	if _, errOut, code := f.run(t, f.dir); code != 0 {
		t.Fatalf("build failed: %d %s", code, errOut)
	}
	out, _, code := f.run(t, f.dir, "--check")
	if code != 0 || !strings.Contains(out, "current:") {
		t.Fatalf("check current: code=%d out=%q", code, out)
	}
	appendFile(t, filepath.Join(f.dir, "cmd", "fefw-tui", "main.go"), "\n// drift\n")
	out, _, code = f.run(t, f.dir, "--check")
	if code != 1 || !strings.Contains(out, "stale:") {
		t.Fatalf("check stale: code=%d out=%q", code, out)
	}
	if _, _, code := f.run(t, f.dir, "--check", "extra"); code != 2 {
		t.Fatalf("check with args=%d, want 2", code)
	}
}

func TestLauncherOutsideRootAndHelp(t *testing.T) {
	f := newFixture(t)
	other := t.TempDir()
	out, errOut, code := f.run(t, other)
	if code != 0 || !strings.Contains(out, "STUB") {
		t.Fatalf("outside-root run: %d out=%q err=%q", code, out, errOut)
	}
	out, _, code = f.run(t, other, "--help")
	if code != 0 || !strings.Contains(out, "usage:") {
		t.Fatalf("help: code=%d out=%q", code, out)
	}
}

func TestLauncherNoToolchain(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "no-go")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dirname, err := exec.LookPath("dirname")
	if err != nil {
		t.Skip("dirname not available")
	}
	for _, name := range []string{dirname} {
		base := filepath.Base(name)
		if err := os.Symlink(name, filepath.Join(dir, base)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(f.bash, f.start, "--check")
	cmd.Dir = f.dir
	cmd.Env = []string{"PATH=" + dir, "FEFW_GO="}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if code != 3 || !strings.Contains(stderr.String(), "Go toolchain not found") {
		t.Fatalf("no-toolchain: code=%d stderr=%q", code, stderr.String())
	}
}

func TestLauncherStaleLockAndTemp(t *testing.T) {
	f := newFixture(t)
	if _, errOut, code := f.run(t, f.dir); code != 0 {
		t.Fatalf("initial build failed: %d %s", code, errOut)
	}
	lock := filepath.Join(f.dir, ".local", "state", "fefw-tui", "build.lock")
	if err := os.MkdirAll(lock, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lock, "pid"), []byte("999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(f.dir, ".local", "bin", ".fefw-tui.tmp.stale")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := f.run(t, f.dir, "--rebuild"); code != 0 {
		t.Fatalf("stale-lock run failed: %d %s", code, errOut)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("stale lock was not removed: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp was not removed: %v", err)
	}
}

func TestLauncherConcurrentFirstBuild(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	results := make([]string, 5)
	errs := make([]string, 5)
	codes := make([]int, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i], codes[i] = f.run(t, t.TempDir())
		}(i)
	}
	wg.Wait()
	for i := range results {
		if codes[i] != 0 || !strings.Contains(results[i], "STUB") {
			t.Fatalf("concurrent %d: code=%d out=%q err=%q", i, codes[i], results[i], errs[i])
		}
	}
	if f.buildCount(t) != 1 {
		t.Fatalf("concurrent first build count=%d, want 1", f.buildCount(t))
	}
}

func TestLauncherIgnoresMtimeAndTranslationChanges(t *testing.T) {
	f := newFixture(t)
	if _, errOut, code := f.run(t, f.dir); code != 0 {
		t.Fatalf("initial build failed: %d %s", code, errOut)
	}
	mainPath := filepath.Join(f.dir, "cmd", "fefw-tui", "main.go")
	later := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(mainPath, later, later); err != nil {
		t.Fatal(err)
	}
	if _, _, code := f.run(t, f.dir); code != 0 {
		t.Fatalf("mtime-only run failed: %d", code)
	}
	if f.buildCount(t) != 1 {
		t.Fatalf("mtime-only change rebuilt: count=%d", f.buildCount(t))
	}
	if err := os.MkdirAll(filepath.Join(f.dir, "translations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "translations", "manifest.json"), []byte("{\"schema_version\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, code := f.run(t, f.dir); code != 0 {
		t.Fatalf("translation-change run failed: %d", code)
	}
	if f.buildCount(t) != 1 {
		t.Fatalf("translation change rebuilt: count=%d", f.buildCount(t))
	}
}

func TestLauncherForwardedArguments(t *testing.T) {
	f := newFixture(t)
	out, errOut, code := f.run(t, t.TempDir(), "--", "--plain", "two words")
	if code != 0 {
		t.Fatalf("forwarded args: code=%d err=%q", code, errOut)
	}
	if !strings.Contains(out, "--plain|two words") {
		t.Fatalf("arguments were not forwarded: %q", out)
	}
}

func TestSourceDigestChangesWithSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "fefw-tui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fefw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "cmd", "fefw-tui", "main.go")
	if err := os.WriteFile(mainPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "x", "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, n, err := sourceDigest(dir)
	if err != nil || n != 3 {
		t.Fatalf("digest: n=%d err=%v", n, err)
	}
	appendFile(t, filepath.Join(dir, "internal", "x", "x.go"), "\n// change\n")
	after, _, err := sourceDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("source digest did not change")
	}
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// Ensure a fresh fingerprint even on filesystems with coarse mtimes.
	_ = os.Chtimes(path, time.Now(), time.Now())
}
