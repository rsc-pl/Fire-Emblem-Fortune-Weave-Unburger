package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// EnglishEdition selects which approved English text a build composes.
type EnglishEdition string

const (
	// EditionHonorifics is the canonical edition: the selected canonical text
	// is used directly.
	EditionHonorifics EnglishEdition = "honorifics"
	// EditionNoHonorifics is the sparse-overlay edition. An override replaces
	// the canonical text only when its recorded base hash still matches.
	EditionNoHonorifics EnglishEdition = "no-honorifics"
)

// Effective text sources. These are build-selection results, not new editorial
// states.
const (
	EffectiveFromRealEN     = "real_en"
	EffectiveFromOfficialEN = "official_en"
	EffectiveFromOverride   = "no_honorifics_override"
	EffectiveNone           = "none"
)

// NoHonorificsOverride is one sparse full-sentence replacement.
type NoHonorificsOverride struct {
	RecordID         string `json:"record_id"`
	BaseRealENSHA256 string `json:"base_real_en_sha256"`
	RealEN           string `json:"real_en"`
}

// NoHonorificsOverlay is the public two-edition overlay document.
type NoHonorificsOverlay struct {
	SchemaVersion int                    `json:"schema_version"`
	Variant       string                 `json:"variant"`
	Base          string                 `json:"base"`
	Overrides     []NoHonorificsOverride `json:"overrides"`
}

// EffectiveText is the selected English text for one record in one edition.
type EffectiveText struct {
	RecordID string         `json:"record_id"`
	Edition  EnglishEdition `json:"edition"`
	Text     string         `json:"text"`
	Source   string         `json:"source"`
	Emitted  bool           `json:"emitted"`
	Reason   string         `json:"reason,omitempty"`
}

// EffectiveReport is a deterministic census of a selection run.
type EffectiveReport struct {
	SchemaVersion int            `json:"schema_version"`
	Edition       EnglishEdition `json:"edition"`
	Records       int            `json:"records"`
	Emitted       int            `json:"emitted"`
	RealEN        int            `json:"real_en"`
	OfficialEN    int            `json:"official_en"`
	Overrides     int            `json:"no_honorifics_overrides"`
	Empty         int            `json:"emitted_empty"`
	NotEmitted    int            `json:"not_emitted"`
	ByReason      map[string]int `json:"by_reason"`
}

// LoadNoHonorificsOverlay reads and validates the sparse two-edition overlay.
func LoadNoHonorificsOverlay(p string) (*NoHonorificsOverlay, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var o NoHonorificsOverlay
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, fmt.Errorf("no-honorifics overlay: %w", err)
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return &o, nil
}

// Validate checks the overlay header and uniqueness. It does not check whether
// each target record is active; use ValidateAgainstRecords for that.
func (o *NoHonorificsOverlay) Validate() error {
	if o == nil {
		return fmt.Errorf("nil no-honorifics overlay")
	}
	if o.SchemaVersion != 1 || o.Variant != "no-honorifics" || o.Base != "canonical" {
		return fmt.Errorf("invalid no-honorifics overlay header")
	}
	seen := map[string]bool{}
	for _, ov := range o.Overrides {
		if ov.RecordID == "" {
			return fmt.Errorf("no-honorifics override has an empty record_id")
		}
		if seen[ov.RecordID] {
			return fmt.Errorf("duplicate no-honorifics override for %s", ov.RecordID)
		}
		seen[ov.RecordID] = true
		if ov.BaseRealENSHA256 == "" {
			return fmt.Errorf("no-honorifics override for %s has no base hash", ov.RecordID)
		}
		if ov.RealEN == "" {
			return fmt.Errorf("no-honorifics override for %s has empty text", ov.RecordID)
		}
	}
	return nil
}

// Index returns the overlay as a record-id map.
func (o *NoHonorificsOverlay) Index() (map[string]NoHonorificsOverride, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	out := make(map[string]NoHonorificsOverride, len(o.Overrides))
	for _, ov := range o.Overrides {
		out[ov.RecordID] = ov
	}
	return out, nil
}

// ValidateAgainstRecords proves that every override points at an active
// canonical real_en with the recorded hash and that the alternative differs.
func (o *NoHonorificsOverlay) ValidateAgainstRecords(records map[string]Record) error {
	index, err := o.Index()
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ov := index[id]
		rec, ok := records[id]
		if !ok {
			return fmt.Errorf("no-honorifics override targets an unknown record: %s", id)
		}
		if rec.Text.RealEN == nil {
			return fmt.Errorf("no-honorifics override targets a record without active real_en: %s", id)
		}
		if shaHexString(*rec.Text.RealEN) != ov.BaseRealENSHA256 {
			return fmt.Errorf("stale no-honorifics base hash: %s", id)
		}
		if *rec.Text.RealEN == ov.RealEN {
			return fmt.Errorf("redundant no-honorifics override: %s", id)
		}
	}
	return nil
}

// SelectEffectiveEN chooses the effective English text by nullness:
//
//  1. real_en when non-null (an intentionally empty real_en stays empty);
//  2. official_en when real_en is null and official_en is non-null;
//  3. no invented text; the result is not emitted and carries a reason derived
//     from the record alignment.
//
// The No-Honorifics edition uses a sparse override only when the record has an
// active real_en and the override base hash still matches.
func SelectEffectiveEN(rec Record, edition EnglishEdition, overlay map[string]NoHonorificsOverride) (EffectiveText, error) {
	if edition != EditionHonorifics && edition != EditionNoHonorifics {
		return EffectiveText{}, fmt.Errorf("unknown English edition %q", edition)
	}
	out := EffectiveText{RecordID: rec.RecordID, Edition: edition}
	if rec.Text.RealEN != nil {
		base := *rec.Text.RealEN
		if edition == EditionNoHonorifics {
			if ov, ok := overlay[rec.RecordID]; ok {
				if ov.BaseRealENSHA256 == "" {
					return EffectiveText{}, fmt.Errorf("no-honorifics override for %s has no base hash", rec.RecordID)
				}
				if shaHexString(base) != ov.BaseRealENSHA256 {
					return EffectiveText{}, fmt.Errorf("stale no-honorifics base hash: %s", rec.RecordID)
				}
				if ov.RealEN == "" {
					return EffectiveText{}, fmt.Errorf("empty no-honorifics override: %s", rec.RecordID)
				}
				out.Text = ov.RealEN
				out.Source = EffectiveFromOverride
				out.Emitted = true
				return out, nil
			}
		}
		out.Text = base
		out.Source = EffectiveFromRealEN
		out.Emitted = true
		if base == "" {
			out.Reason = "intentional_empty_real_en"
		}
		return out, nil
	}
	if edition == EditionNoHonorifics {
		if _, ok := overlay[rec.RecordID]; ok {
			return EffectiveText{}, fmt.Errorf("no-honorifics override targets a record without active real_en: %s", rec.RecordID)
		}
	}
	if rec.Text.OfficialEN != nil {
		out.Text = *rec.Text.OfficialEN
		out.Source = EffectiveFromOfficialEN
		out.Emitted = true
		if out.Text == "" {
			out.Reason = "empty_official_en"
		}
		return out, nil
	}
	out.Source = EffectiveNone
	out.Emitted = false
	switch rec.Context.Alignment {
	case "JP_ONLY":
		out.Reason = "jp_only_no_english"
	case "EN_ONLY":
		out.Reason = "en_only_without_usable_text"
	case "BOTH_EMPTY":
		out.Reason = "both_empty"
	case "NO_JP_SOURCE":
		out.Reason = "no_source_text"
	default:
		out.Reason = "no_english_text"
	}
	return out, nil
}

// SelectEffectiveShard applies the selection to every record exactly once.
func SelectEffectiveShard(sh Shard, edition EnglishEdition, overlay map[string]NoHonorificsOverride) ([]EffectiveText, EffectiveReport, error) {
	out := make([]EffectiveText, 0, len(sh.Records))
	for _, rec := range sh.Records {
		sel, err := SelectEffectiveEN(rec, edition, overlay)
		if err != nil {
			return nil, EffectiveReport{}, err
		}
		out = append(out, sel)
	}
	return out, SummarizeEffective(edition, out), nil
}

// SummarizeEffective builds a deterministic selection census.
func SummarizeEffective(edition EnglishEdition, items []EffectiveText) EffectiveReport {
	rep := EffectiveReport{SchemaVersion: 1, Edition: edition, ByReason: map[string]int{}}
	for _, it := range items {
		rep.Records++
		if it.Emitted {
			rep.Emitted++
			if it.Text == "" {
				rep.Empty++
			}
		} else {
			rep.NotEmitted++
		}
		switch it.Source {
		case EffectiveFromRealEN:
			rep.RealEN++
		case EffectiveFromOfficialEN:
			rep.OfficialEN++
		case EffectiveFromOverride:
			rep.Overrides++
		}
		if it.Reason != "" {
			rep.ByReason[it.Reason]++
		}
	}
	return rep
}

func shaHexString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
