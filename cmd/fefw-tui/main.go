// Command fefw-tui is a read-only terminal console for the canonical FEFW
// corpus. It inspects the public translation tree, verifies manifest hashes
// and record counts, and can run an in-memory corpus build when the private
// staged sources are present. It never writes to translations or sources.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"fefw/internal/importer"
)

const version = "1.1.0"

func main() {
	var (
		root     = flag.String("root", ".", "project root")
		plain    = flag.Bool("plain", false, "print a plain report and exit (also used when stdout is not a TTY)")
		noColor  = flag.Bool("no-color", false, "disable ANSI colour")
		theme    = flag.String("theme", "auto", "colour theme: auto, dark or light")
		showVer  = flag.Bool("version", false, "print version and exit")
		showHelp = flag.Bool("help", false, "print help and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `fefw-tui — read-only canonical corpus console

usage: fefw-tui [flags]

flags:
  --root PATH     project root (default: current directory)
  --plain         print a plain report and exit; never enter the console
  --no-color      disable ANSI colour
  --theme MODE    auto, dark or light (default: auto)
  --version       print version and exit
  --help          print this help and exit

The console never writes to translations, sources, reports or editorial
documents. Key bindings are shown in the footer and with '?'.

environment:
  NO_COLOR        disable ANSI colour
  FEFW_THEME      light or dark; overrides automatic palette selection
`)
	}
	flag.Parse()

	if *showHelp {
		flag.Usage()
		return
	}
	if *showVer {
		fmt.Printf("fefw-tui %s\n", version)
		return
	}

	light := detectLightTheme()
	switch strings.ToLower(*theme) {
	case "auto":
	case "dark":
		light = false
	case "light":
		light = true
	default:
		fmt.Fprintf(os.Stderr, "error: --theme must be auto, dark or light\n")
		os.Exit(2)
	}

	color := !*noColor && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	opts := uiOptions{Color: color, Light: light, NoColor: !color}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot resolve project root: %v\n", err)
		os.Exit(2)
	}
	info, err := os.Stat(absRoot)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "error: project root is not a directory: %s\n", absRoot)
		os.Exit(2)
	}

	man, manSHA, manErr := loadManifest(absRoot)
	manErrText := ""
	if manErr != nil {
		manErrText = manErr.Error()
	}

	if *plain || !isTTY(os.Stdout.Fd()) {
		if manErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", manErr)
			fmt.Fprintf(os.Stderr, "hint: run from the project root or pass --root\n")
			os.Exit(1)
		}
		os.Exit(runPlain(absRoot))
	}
	os.Exit(runTUI(absRoot, man, manSHA, manErrText, opts))
}

func runTUI(root string, man *importer.Manifest, manSHA, manErr string, opts uiOptions) int {
	model := newModel(root, man, manSHA, manErr, opts)
	program := tea.NewProgram(model, tea.WithAltScreen())

	var signalCode int32
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	go func() {
		s, ok := <-sigCh
		if !ok {
			return
		}
		switch s {
		case syscall.SIGHUP:
			atomic.StoreInt32(&signalCode, 129)
		default:
			atomic.StoreInt32(&signalCode, 143)
		}
		program.Quit()
	}()

	final, err := program.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	code := int(atomic.LoadInt32(&signalCode))
	if m, ok := final.(*Model); ok && m.exitCode != 0 {
		code = m.exitCode
	}
	return code
}

// defaultConfig returns a config whose paths are anchored at root, so the
// console behaves the same when launched from another working directory.
func defaultConfig(root string) importer.Config {
	cfg := importer.DefaultConfig()
	cfg.ProjectRoot = root
	cfg.SourceRoot = filepath.Join(root, ".local", "_incoming", "fefw-original-sources")
	cfg.TranslationsDir = filepath.Join(root, "translations")
	cfg.EvidenceDir = filepath.Join(root, ".local", "technical", "evidence")
	cfg.ReportsDir = filepath.Join(root, ".local", "reports")
	cfg.WorkDir = filepath.Join(root, ".local", "work")
	return cfg
}

func translationsDir(root string) string {
	return filepath.Join(root, "translations")
}

func manifestPath(root string) string {
	return filepath.Join(translationsDir(root), "manifest.json")
}
