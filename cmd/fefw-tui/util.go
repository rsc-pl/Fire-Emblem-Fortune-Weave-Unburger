package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clampInt(v, low, high int) int {
	if high < low {
		return low
	}
	return minInt(maxInt(v, low), high)
}

func comma(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign = "-"
		s = s[1:]
	}
	var b strings.Builder
	b.WriteString(sign)
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func reviewSummary(m map[string]int) string {
	keys := sortedKeys(m)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+" "+comma(m[k]))
	}
	return strings.Join(parts, " · ")
}

func checksSummary(checks map[string]bool) string {
	if len(checks) == 0 {
		return "no checks reported"
	}
	keys := make([]string, 0, len(checks))
	for k := range checks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	passed := 0
	var failed []string
	informational := 0
	total := 0
	for _, k := range keys {
		if k == "fresh_import_purity" {
			// Routine validation accepts an edited tree; this check is a
			// separate import contract, not a canonical-tree failure.
			informational++
			continue
		}
		total++
		if checks[k] {
			passed++
		} else {
			failed = append(failed, k)
		}
	}
	suffix := ""
	if informational > 0 {
		suffix = " · fresh-import purity n/a"
	}
	if len(failed) == 0 {
		return fmt.Sprintf("%d/%d passed%s", passed, total, suffix)
	}
	return fmt.Sprintf("%d/%d passed · failed: %s%s", passed, total, strings.Join(failed, ", "), suffix)
}

func checkCounts(checks map[string]bool) (passed, failed int) {
	for k, ok := range checks {
		if k == "fresh_import_purity" {
			continue
		}
		if ok {
			passed++
		} else {
			failed++
		}
	}
	return passed, failed
}

func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 32 || (r >= 0x7f && r < 0xa0):
		return 0
	case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r):
		return 0
	case r >= 0x1100 && (r <= 0x115f ||
		r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1f64f) ||
		(r >= 0x1f900 && r <= 0x1f9ff) ||
		(r >= 0x20000 && r <= 0x3fffd)):
		return 2
	}
	return 1
}

func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if displayWidth(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runeWidth(r)
		if used+rw > max-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + "…"
}

func truncateMiddle(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if displayWidth(s) <= max {
		return s
	}
	if max <= 5 {
		return truncate(s, max)
	}
	tail := max / 3
	head := max - tail - 1
	return headString(s, head) + "…" + tailString(s, tail)
}

func headString(s string, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runeWidth(r)
		if used+rw > width {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String()
}

func tailString(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	used := 0
	start := len(runes)
	for start > 0 {
		w := runeWidth(runes[start-1])
		if used+w > width {
			break
		}
		used += w
		start--
	}
	return string(runes[start:])
}

func padRight(s string, width int) string {
	if displayWidth(s) >= width {
		return truncate(s, width)
	}
	return s + strings.Repeat(" ", width-displayWidth(s))
}

// asciiProgress is the predictable fallback used when colour is disabled or
// the terminal cannot render the styled bar. It never invents a percentage for
// an unknown total.
func asciiProgress(done, total, width int) string {
	if width < 6 {
		width = 6
	}
	if total <= 0 {
		return "[" + strings.Repeat("·", width) + "]"
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	filled := done * width / total
	pct := done * 100 / total
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]" +
		fmt.Sprintf(" %3d%%", pct)
}

// truncateANSI clips a styled string to a display width while preserving the
// escape sequences it already contains.
func truncateANSI(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

func padANSIWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = truncateANSI(s, width)
	n := ansi.StringWidth(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

// fitView clips and pads a multi-line string to exactly the requested size.
// ANSI sequences and wide characters are measured by display width, not by
// byte length.
func fitView(s string, width, height int) string {
	if height <= 0 {
		return ""
	}
	if width <= 0 {
		width = 1
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i, line := range lines {
		lines[i] = padANSIWidth(line, width)
	}
	return strings.Join(lines, "\n")
}

// hjoin places two rendered columns side by side with a fixed gap. Every line
// keeps its ANSI styling and is padded by display width.
func hjoin(left, right []string, gap int) []string {
	leftWidth, rightWidth := 0, 0
	for _, l := range left {
		if w := ansi.StringWidth(l); w > leftWidth {
			leftWidth = w
		}
	}
	for _, r := range right {
		if w := ansi.StringWidth(r); w > rightWidth {
			rightWidth = w
		}
	}
	rows := maxInt(len(left), len(right))
	out := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, padANSIWidth(l, leftWidth)+strings.Repeat(" ", gap)+r)
	}
	return out
}
