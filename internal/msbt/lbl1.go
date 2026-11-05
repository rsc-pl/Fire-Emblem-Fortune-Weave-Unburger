package msbt

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// Label is one LBL1/ALB1/SLB1/CLB1 hash-table entry.
type Label struct {
	Text  string
	Index uint32
	// Bucket is the hash bucket the label was stored in.
	Bucket int
}

// LabelTable is a parsed LMS label hash table.
type LabelTable struct {
	BucketCount uint32
	Groups      []LabelGroup
	Labels      []Label
	// ByIndex maps an item index to all labels that point at it. More than
	// one label is a collision and is reported, never silently discarded.
	ByIndex map[uint32][]string
	// BadBucket lists labels stored in a bucket that does not match their
	// computed hash.
	BadBucket []Label
}

// LabelGroup is one hash bucket.
type LabelGroup struct {
	Count  uint32
	Offset uint32
}

// HashLabel implements the LMS label hash used by official files.
func HashLabel(label string, buckets uint32) uint32 {
	if buckets == 0 {
		return 0
	}
	var h uint32
	for _, r := range label {
		h = h*0x492 + uint32(r)
	}
	return h % buckets
}

func parseLBL1(data []byte, textCount int) (*LabelTable, error) {
	t, err := parseLabelTable(data)
	if err != nil {
		return nil, err
	}
	for _, l := range t.Labels {
		if int(l.Index) >= textCount {
			return nil, fmt.Errorf("label %q index %d out of range (text count %d)", l.Text, l.Index, textCount)
		}
	}
	return t, nil
}

func parseLabelTable(data []byte) (*LabelTable, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("label table shorter than 4 bytes")
	}
	n := binary.LittleEndian.Uint32(data[0:4])
	if n > 1<<20 {
		return nil, fmt.Errorf("implausible bucket count %d", n)
	}
	need := 4 + int(n)*8
	if need > len(data) {
		return nil, fmt.Errorf("bucket table needs %d bytes, have %d", need, len(data))
	}
	t := &LabelTable{BucketCount: n, ByIndex: map[uint32][]string{}}
	for i := 0; i < int(n); i++ {
		cnt := binary.LittleEndian.Uint32(data[4+i*8 : 8+i*8])
		off := binary.LittleEndian.Uint32(data[8+i*8 : 12+i*8])
		t.Groups = append(t.Groups, LabelGroup{Count: cnt, Offset: off})
		if off > uint32(len(data)) {
			return nil, fmt.Errorf("bucket %d offset %d out of range", i, off)
		}
		p := int(off)
		for j := 0; j < int(cnt); j++ {
			if p >= len(data) {
				return nil, fmt.Errorf("bucket %d label %d past end", i, j)
			}
			ln := int(data[p])
			p++
			if p+ln+4 > len(data) {
				return nil, fmt.Errorf("bucket %d label %d truncated", i, j)
			}
			text := string(data[p : p+ln])
			p += ln
			idx := binary.LittleEndian.Uint32(data[p : p+4])
			p += 4
			t.Labels = append(t.Labels, Label{Text: text, Index: idx, Bucket: i})
			t.ByIndex[idx] = append(t.ByIndex[idx], text)
			if HashLabel(text, n) != uint32(i) {
				t.BadBucket = append(t.BadBucket, Label{Text: text, Index: idx, Bucket: i})
			}
		}
	}
	sort.SliceStable(t.Labels, func(i, j int) bool {
		if t.Labels[i].Index != t.Labels[j].Index {
			return t.Labels[i].Index < t.Labels[j].Index
		}
		if t.Labels[i].Text != t.Labels[j].Text {
			return t.Labels[i].Text < t.Labels[j].Text
		}
		return t.Labels[i].Bucket < t.Labels[j].Bucket
	})
	return t, nil
}

// CanonicalLabel returns the deterministic label for an item index. It
// prefers the lexicographically smallest label and reports how many aliases
// exist. A count > 1 means the physical slot has a label collision.
func (t *LabelTable) CanonicalLabel(index uint32) (string, int) {
	if t == nil {
		return "", 0
	}
	ls := t.ByIndex[index]
	if len(ls) == 0 {
		return "", 0
	}
	cp := append([]string(nil), ls...)
	sort.Strings(cp)
	return cp[0], len(cp)
}

// Collisions returns indices with more than one label.
func (t *LabelTable) Collisions() []uint32 {
	if t == nil {
		return nil
	}
	var out []uint32
	for idx, ls := range t.ByIndex {
		if len(ls) > 1 {
			out = append(out, idx)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
