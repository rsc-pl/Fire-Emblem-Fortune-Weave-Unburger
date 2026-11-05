package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// errLiveSnapshotRace marks a corpus/overlay read that overlapped an editorial
// update. Callers may retry or report INCONCLUSIVE; it must never be treated as
// a pass and it must never be confused with an invalid or stale overlay.
var errLiveSnapshotRace = errors.New("live corpus changed during snapshot")

func strp(s string) *string { return &s }

func effectiveRecord(id, alignment string, jp, official, real *string) Record {
	return Record{
		RecordID: id,
		Context:  Context{Alignment: alignment},
		Text:     Text{JP: jp, OfficialEN: official, RealEN: real},
	}
}

func TestSelectEffectiveENRealENWins(t *testing.T) {
	rec := effectiveRecord("r1", "ALIGNED", strp("jp"), strp("official"), strp("authored"))
	got, err := SelectEffectiveEN(rec, EditionHonorifics, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Emitted || got.Text != "authored" || got.Source != EffectiveFromRealEN {
		t.Fatalf("real_en not selected: %+v", got)
	}
}

func TestSelectEffectiveENOfficialFallback(t *testing.T) {
	rec := effectiveRecord("r2", "ALIGNED", strp("jp"), strp("official"), nil)
	for _, edition := range []EnglishEdition{EditionHonorifics, EditionNoHonorifics} {
		got, err := SelectEffectiveEN(rec, edition, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Emitted || got.Text != "official" || got.Source != EffectiveFromOfficialEN {
			t.Fatalf("%s fallback not selected: %+v", edition, got)
		}
	}
}

func TestSelectEffectiveENEmptyRealENIsIntentional(t *testing.T) {
	rec := effectiveRecord("r3", "ALIGNED", strp("jp"), strp("official"), strp(""))
	got, err := SelectEffectiveEN(rec, EditionHonorifics, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Emitted || got.Text != "" || got.Source != EffectiveFromRealEN {
		t.Fatalf("empty real_en fell back or was dropped: %+v", got)
	}
	if got.Reason != "intentional_empty_real_en" {
		t.Fatalf("empty real_en reason=%q", got.Reason)
	}
}

func TestSelectEffectiveENNoEnglishIsReported(t *testing.T) {
	cases := []struct {
		alignment string
		reason    string
	}{
		{"JP_ONLY", "jp_only_no_english"},
		{"BOTH_EMPTY", "both_empty"},
		{"NO_JP_SOURCE", "no_source_text"},
	}
	for _, c := range cases {
		rec := effectiveRecord("r4", c.alignment, strp("jp"), nil, nil)
		got, err := SelectEffectiveEN(rec, EditionHonorifics, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.Emitted || got.Text != "" || got.Source != EffectiveNone || got.Reason != c.reason {
			t.Fatalf("%s not reported correctly: %+v", c.alignment, got)
		}
	}
}

func TestSelectEffectiveENNoHonorificsOverride(t *testing.T) {
	base := "Otto-chan, wait!"
	overlay := map[string]NoHonorificsOverride{
		"r5": {RecordID: "r5", BaseRealENSHA256: shaHexString(base), RealEN: "Otto, wait!"},
	}
	rec := effectiveRecord("r5", "ALIGNED", strp("jp"), strp("official"), strp(base))
	hon, err := SelectEffectiveEN(rec, EditionHonorifics, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if hon.Text != base || hon.Source != EffectiveFromRealEN {
		t.Fatalf("canonical edition changed: %+v", hon)
	}
	no, err := SelectEffectiveEN(rec, EditionNoHonorifics, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if no.Text != "Otto, wait!" || no.Source != EffectiveFromOverride {
		t.Fatalf("no-honorifics override not applied: %+v", no)
	}
}

func TestSelectEffectiveENNoHonorificsFallsBackToBase(t *testing.T) {
	rec := effectiveRecord("r6", "ALIGNED", strp("jp"), strp("official"), strp("unchanged"))
	got, err := SelectEffectiveEN(rec, EditionNoHonorifics, map[string]NoHonorificsOverride{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "unchanged" || got.Source != EffectiveFromRealEN {
		t.Fatalf("no-honorifics base not used: %+v", got)
	}
}

func TestSelectEffectiveENRejectsStaleAndInactiveOverride(t *testing.T) {
	rec := effectiveRecord("r7", "ALIGNED", strp("jp"), strp("official"), strp("new base"))
	stale := map[string]NoHonorificsOverride{
		"r7": {RecordID: "r7", BaseRealENSHA256: shaHexString("old base"), RealEN: "alternative"},
	}
	if _, err := SelectEffectiveEN(rec, EditionNoHonorifics, stale); err == nil {
		t.Fatal("stale override accepted")
	}
	inactive := effectiveRecord("r8", "ALIGNED", strp("jp"), strp("official"), nil)
	overlay := map[string]NoHonorificsOverride{
		"r8": {RecordID: "r8", BaseRealENSHA256: shaHexString("official"), RealEN: "alternative"},
	}
	if _, err := SelectEffectiveEN(inactive, EditionNoHonorifics, overlay); err == nil {
		t.Fatal("override for null real_en accepted")
	}
}

func TestLoadNoHonorificsOverlayRejectsAmbiguousEntries(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dup := write("dup.json", `{"schema_version":1,"variant":"no-honorifics","base":"canonical","overrides":[{"record_id":"a","base_real_en_sha256":"x","real_en":"b"},{"record_id":"a","base_real_en_sha256":"x","real_en":"c"}]}`)
	if _, err := LoadNoHonorificsOverlay(dup); err == nil {
		t.Fatal("duplicate override accepted")
	}
	empty := write("empty.json", `{"schema_version":1,"variant":"no-honorifics","base":"canonical","overrides":[{"record_id":"a","base_real_en_sha256":"x","real_en":""}]}`)
	if _, err := LoadNoHonorificsOverlay(empty); err == nil {
		t.Fatal("empty override accepted")
	}
	good := write("good.json", `{"schema_version":1,"variant":"no-honorifics","base":"canonical","overrides":[]}`)
	if _, err := LoadNoHonorificsOverlay(good); err != nil {
		t.Fatal(err)
	}
}

func TestSelectEffectiveEditedCorpusAfterFreshImport(t *testing.T) {
	records := []Record{
		effectiveRecord("keep", "ALIGNED", strp("jp1"), strp("official1"), strp("edited real")),
		effectiveRecord("fallback", "ALIGNED", strp("jp2"), strp("official2"), nil),
		effectiveRecord("jp-only", "JP_ONLY", strp("jp3"), nil, nil),
	}
	sh := Shard{SchemaVersion: 1, Records: records}
	items, rep, err := SelectEffectiveShard(sh, EditionHonorifics, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || rep.Records != 3 || rep.Emitted != 2 || rep.NotEmitted != 1 {
		t.Fatalf("selection census wrong: %+v", rep)
	}
	if items[0].Text != "edited real" || items[1].Text != "official2" || items[2].Emitted {
		t.Fatalf("edited corpus selection wrong: %+v", items)
	}
}

// liveCorpusSnapshot is one coherent, read-only view of the canonical corpus.
// Every shard hash is checked against the manifest that was read first, and the
// manifest/overlay hashes are re-read before the snapshot is accepted. A change
// observed during the read is a race, not a silently usable snapshot.
type liveCorpusSnapshot struct {
	shards      map[string]Shard
	records     map[string]Record
	manifestSHA string
	overlaySHA  string
	overlayRaw  []byte
}

func fileSHA(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return shaHexString(string(b)), nil
}

func fileStillHasSHA(path, want string) bool {
	got, err := fileSHA(path)
	return err == nil && got == want
}

func readLiveCorpusSnapshot(root string) (*liveCorpusSnapshot, error) {
	manifestPath := filepath.Join(root, "manifest.json")
	overlayPath := filepath.Join(root, "variants", "no-honorifics.json")

	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	snap := &liveCorpusSnapshot{
		shards:      map[string]Shard{},
		records:     map[string]Record{},
		manifestSHA: shaHexString(string(manifestBytes)),
	}
	var man Manifest
	if err := json.Unmarshal(manifestBytes, &man); err != nil {
		return nil, fmt.Errorf("manifest snapshot: %w", err)
	}
	if man.SchemaVersion != 1 || len(man.Files) == 0 {
		return nil, fmt.Errorf("manifest snapshot: no files or unsupported schema")
	}
	seenPaths := map[string]bool{}
	for _, mf := range man.Files {
		if mf.Path == "" || seenPaths[mf.Path] {
			return nil, fmt.Errorf("manifest snapshot: empty or duplicate shard path %q", mf.Path)
		}
		seenPaths[mf.Path] = true
	}
	onDisk, err := snapshotShardPaths(root)
	if err != nil {
		return nil, err
	}
	if len(onDisk) != len(man.Files) {
		if !fileStillHasSHA(manifestPath, snap.manifestSHA) {
			return nil, fmt.Errorf("%w: shard set changed during snapshot", errLiveSnapshotRace)
		}
		return nil, fmt.Errorf("corpus snapshot: on-disk shards %d != manifest files %d", len(onDisk), len(man.Files))
	}
	for _, p := range onDisk {
		if !seenPaths[p] {
			if !fileStillHasSHA(manifestPath, snap.manifestSHA) {
				return nil, fmt.Errorf("%w: shard set changed during snapshot", errLiveSnapshotRace)
			}
			return nil, fmt.Errorf("corpus snapshot: shard %s is not listed in the manifest", p)
		}
	}
	for _, mf := range man.Files {
		p := filepath.Join(root, filepath.FromSlash(mf.Path))
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("manifest snapshot: %s: %w", mf.Path, err)
		}
		if shaHexString(string(b)) != mf.SHA256 {
			// A manifest that moved while shards were being read is a race.
			if !fileStillHasSHA(manifestPath, snap.manifestSHA) {
				return nil, fmt.Errorf("%w: manifest changed while reading %s", errLiveSnapshotRace, mf.Path)
			}
			return nil, fmt.Errorf("corpus snapshot: manifest hash mismatch for %s", mf.Path)
		}
		var sh Shard
		if err := json.Unmarshal(b, &sh); err != nil {
			return nil, fmt.Errorf("corpus snapshot: %s: %w", mf.Path, err)
		}
		if len(sh.Records) != mf.Records {
			return nil, fmt.Errorf("corpus snapshot: %s record count %d != manifest %d", mf.Path, len(sh.Records), mf.Records)
		}
		snap.shards[mf.Path] = sh
		for _, r := range sh.Records {
			snap.records[r.RecordID] = r
		}
	}

	snap.overlayRaw, err = os.ReadFile(overlayPath)
	if err != nil {
		return nil, fmt.Errorf("overlay snapshot: %w", err)
	}
	snap.overlaySHA = shaHexString(string(snap.overlayRaw))

	// Re-read the two small control files. This closes the window in which an
	// editor could have replaced the manifest or overlay after the snapshot.
	if !fileStillHasSHA(manifestPath, snap.manifestSHA) || !fileStillHasSHA(overlayPath, snap.overlaySHA) {
		return nil, fmt.Errorf("%w: manifest or overlay changed during snapshot", errLiveSnapshotRace)
	}
	return snap, nil
}

func (s *liveCorpusSnapshot) stillMatches(root string) bool {
	return fileStillHasSHA(filepath.Join(root, "manifest.json"), s.manifestSHA) &&
		fileStillHasSHA(filepath.Join(root, "variants", "no-honorifics.json"), s.overlaySHA)
}

// snapshotShardPaths lists the canonical shard paths without reading their
// bytes, so the snapshot can prove the manifest covers the whole tree.
func snapshotShardPaths(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".json") || d.Name() == "manifest.json" {
			return nil
		}
		if strings.Contains(filepath.ToSlash(p), "/variants/") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}

func parseNoHonorificsOverlayBytes(b []byte) (*NoHonorificsOverlay, error) {
	var o NoHonorificsOverlay
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, fmt.Errorf("no-honorifics overlay: %w", err)
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return &o, nil
}

// validateLiveOverlayAgainstSnapshot is fail-closed: an invalid, stale or
// inactive overlay returns an error and no index. It never substitutes an
// empty overlay to make the audit pass. It also re-proves the exact hash of
// every target, not just the first failure.
func validateLiveOverlayAgainstSnapshot(snap *liveCorpusSnapshot, overlay *NoHonorificsOverlay) (map[string]NoHonorificsOverride, int, error) {
	index, err := overlay.Index()
	if err != nil {
		return nil, 0, err
	}
	if len(index) == 0 {
		return nil, 0, fmt.Errorf("no-honorifics overlay has no targets")
	}
	if err := overlay.ValidateAgainstRecords(snap.records); err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	verified := 0
	for _, id := range ids {
		ov := index[id]
		rec, ok := snap.records[id]
		if !ok {
			return nil, 0, fmt.Errorf("no-honorifics override targets an unknown record: %s", id)
		}
		if rec.Text.RealEN == nil {
			return nil, 0, fmt.Errorf("no-honorifics override targets a record without active real_en: %s", id)
		}
		if got := shaHexString(*rec.Text.RealEN); got != ov.BaseRealENSHA256 {
			return nil, 0, fmt.Errorf("stale no-honorifics base hash: %s (%s != %s)", id, got, ov.BaseRealENSHA256)
		}
		if *rec.Text.RealEN == ov.RealEN {
			return nil, 0, fmt.Errorf("redundant no-honorifics override: %s", id)
		}
		verified++
	}
	if verified != len(index) {
		return nil, 0, fmt.Errorf("overlay target verification incomplete: %d/%d", verified, len(index))
	}
	return index, verified, nil
}

// TestSelectEffectiveLiveCorpusInventory exercises the selection layer over the
// complete canonical corpus. It reads only; it never writes translations.
// Invalid/stale/inactive overlays are a hard failure. A simultaneous editorial
// update is retried and, if it will not settle, reported as INCONCLUSIVE
// (skipped); it is never accepted or reported as PASS.
func TestSelectEffectiveLiveCorpusInventory(t *testing.T) {
	root := filepath.Join("..", "..", "translations")
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); err != nil {
		t.Skip("live canonical corpus not present")
	}

	const attempts = 3
	var snap *liveCorpusSnapshot
	var overlay *NoHonorificsOverlay
	var overlayIndex map[string]NoHonorificsOverride
	var verifiedTargets int
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		s, err := readLiveCorpusSnapshot(root)
		if err != nil {
			if errors.Is(err, errLiveSnapshotRace) {
				lastErr = err
				continue
			}
			t.Fatalf("read-only corpus snapshot failed: %v", err)
		}
		ov, err := parseNoHonorificsOverlayBytes(s.overlayRaw)
		if err != nil {
			if !s.stillMatches(root) {
				lastErr = fmt.Errorf("%w: overlay changed during validation: %v", errLiveSnapshotRace, err)
				continue
			}
			t.Fatalf("live no-honorifics overlay is invalid (fail-closed): %v", err)
		}
		idx, verified, err := validateLiveOverlayAgainstSnapshot(s, ov)
		if err != nil {
			if !s.stillMatches(root) {
				lastErr = fmt.Errorf("%w: overlay changed during validation: %v", errLiveSnapshotRace, err)
				continue
			}
			t.Fatalf("live no-honorifics overlay is invalid/stale/inactive (fail-closed): %v", err)
		}
		if !s.stillMatches(root) {
			lastErr = fmt.Errorf("%w: manifest or overlay changed during validation", errLiveSnapshotRace)
			continue
		}
		snap, overlay, overlayIndex, verifiedTargets = s, ov, idx, verified
		break
	}
	if snap == nil {
		t.Skipf("INCONCLUSIVE after %d read-only snapshot attempts: %v", attempts, lastErr)
	}

	// Prove the actual overlay target count and every recorded base hash on the
	// stable snapshot before using it.
	if len(overlay.Overrides) != verifiedTargets || verifiedTargets == 0 {
		t.Fatalf("overlay targets=%d verified=%d; expected a non-empty, fully verified overlay", len(overlay.Overrides), verifiedTargets)
	}
	t.Logf("live overlay: targets=%d sha_verified=%d overlay_sha256=%s manifest_sha256=%s", len(overlay.Overrides), verifiedTargets, snap.overlaySHA, snap.manifestSHA)
	targetIDs := make([]string, 0, len(overlayIndex))
	for id := range overlayIndex {
		targetIDs = append(targetIDs, id)
	}
	sort.Strings(targetIDs)
	for _, id := range targetIDs {
		t.Logf("overlay target verified: %s sha256=%s", id, overlayIndex[id].BaseRealENSHA256)
	}

	banks := map[string]bool{}
	for _, sh := range snap.shards {
		for _, r := range sh.Records {
			banks[r.Source.Bank] = true
		}
	}
	if len(banks) != 523 {
		t.Fatalf("bank inventory=%d, want 523", len(banks))
	}

	paths := make([]string, 0, len(snap.shards))
	for p := range snap.shards {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, edition := range []EnglishEdition{EditionHonorifics, EditionNoHonorifics} {
		seen := map[string]int{}
		alignedOfficial := 0
		notEmitted := 0
		fallback := 0
		overrides := 0
		realEN := 0
		for _, path := range paths {
			sh := snap.shards[path]
			items, rep, err := SelectEffectiveShard(sh, edition, overlayIndex)
			if err != nil {
				t.Fatalf("%s %s: %v", edition, path, err)
			}
			if len(items) != len(sh.Records) || rep.Records != len(sh.Records) {
				t.Fatalf("%s %s: selection count %d/%d != records %d", edition, path, len(items), rep.Records, len(sh.Records))
			}
			notEmitted += rep.NotEmitted
			fallback += rep.OfficialEN
			overrides += rep.Overrides
			realEN += rep.RealEN
			for i, r := range sh.Records {
				seen[items[i].RecordID]++
				if items[i].RecordID != r.RecordID {
					t.Fatalf("%s %s: selection order changed at %s", edition, path, r.RecordID)
				}
				if r.Context.Alignment == "ALIGNED" && r.Text.OfficialEN != nil {
					alignedOfficial++
					if !items[i].Emitted {
						t.Fatalf("%s %s: aligned official-present record not emitted: %s", edition, path, r.RecordID)
					}
				}
			}
		}
		if len(seen) != len(snap.records) {
			t.Fatalf("%s: emitted/selected unique ids %d != corpus records %d", edition, len(seen), len(snap.records))
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("%s: record selected %d times: %s", edition, n, id)
			}
		}
		if alignedOfficial == 0 {
			t.Fatalf("%s: no aligned official-present records found", edition)
		}
		if fallback == 0 {
			t.Fatalf("%s: official_en fallback never used", edition)
		}
		if notEmitted > len(snap.records) {
			t.Fatalf("%s: invalid not-emitted count %d", edition, notEmitted)
		}
		t.Logf("%s: records=%d aligned_official_present=%d real_en=%d fallback_official=%d overrides=%d not_emitted=%d", edition, len(snap.records), alignedOfficial, realEN, fallback, overrides, notEmitted)
		switch edition {
		case EditionHonorifics:
			if overrides != 0 {
				t.Fatalf("honorifics edition applied %d overrides", overrides)
			}
		case EditionNoHonorifics:
			if overrides != len(overlay.Overrides) {
				t.Fatalf("no-honorifics edition applied %d/%d verified overrides", overrides, len(overlay.Overrides))
			}
		}
	}
}

// TestLiveOverlayValidationFailsClosed is the regression guard for the old
// audit fallback, which logged a stale overlay error and then continued with an
// empty override map. A synthetic stale/inactive overlay must return an error
// instead of being silently replaced.
func TestLiveOverlayValidationFailsClosed(t *testing.T) {
	base := "current base text"
	snap := &liveCorpusSnapshot{records: map[string]Record{
		"stale":    effectiveRecord("stale", "ALIGNED", strp("jp"), nil, strp(base)),
		"inactive": effectiveRecord("inactive", "ALIGNED", strp("jp"), nil, nil),
	}}

	stale := &NoHonorificsOverlay{
		SchemaVersion: 1, Variant: "no-honorifics", Base: "canonical",
		Overrides: []NoHonorificsOverride{{
			RecordID: "stale", BaseRealENSHA256: shaHexString("old base"), RealEN: "alternative",
		}},
	}
	if idx, _, err := validateLiveOverlayAgainstSnapshot(snap, stale); err == nil {
		t.Fatalf("stale overlay was accepted and produced %d overrides; fail-closed regression", len(idx))
	}

	inactive := &NoHonorificsOverlay{
		SchemaVersion: 1, Variant: "no-honorifics", Base: "canonical",
		Overrides: []NoHonorificsOverride{{
			RecordID: "inactive", BaseRealENSHA256: shaHexString("anything"), RealEN: "alternative",
		}},
	}
	if idx, _, err := validateLiveOverlayAgainstSnapshot(snap, inactive); err == nil {
		t.Fatalf("overlay for inactive real_en was accepted and produced %d overrides; fail-closed regression", len(idx))
	}

	valid := &NoHonorificsOverlay{
		SchemaVersion: 1, Variant: "no-honorifics", Base: "canonical",
		Overrides: []NoHonorificsOverride{{
			RecordID: "stale", BaseRealENSHA256: shaHexString(base), RealEN: "alternative",
		}},
	}
	idx, verified, err := validateLiveOverlayAgainstSnapshot(snap, valid)
	if err != nil {
		t.Fatalf("valid synthetic overlay rejected: %v", err)
	}
	if len(idx) != 1 || verified != 1 {
		t.Fatalf("valid synthetic overlay targets=%d verified=%d", len(idx), verified)
	}
}
