package msbt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

// TXT2 is the text section. Strings stores the exact bytes between the
// terminating NUL bytes. Dirty is set by SetRaw and causes Encode to rebuild
// the offset table and section.
type TXT2 struct {
	Strings [][]byte
	Data    []byte
	Dirty   bool
}

func parseTXT2(data []byte) (*TXT2, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("section shorter than count field")
	}
	n := binary.LittleEndian.Uint32(data[0:4])
	if n > 1<<24 {
		return nil, fmt.Errorf("implausible text count %d", n)
	}
	table := 4 + int(n)*4
	if table > len(data) {
		return nil, fmt.Errorf("offset table needs %d bytes, have %d", table, len(data))
	}
	t := &TXT2{Data: data}
	for i := 0; i < int(n); i++ {
		off := binary.LittleEndian.Uint32(data[4+i*4 : 8+i*4])
		if int(off) < table || int(off) >= len(data) {
			return nil, fmt.Errorf("string %d offset %d out of range (table ends at %d, section is %d bytes)", i, off, table, len(data))
		}
		p := int(off)
		e := bytes.IndexByte(data[p:], 0)
		if e < 0 {
			return nil, fmt.Errorf("string %d at 0x%x has no NUL terminator", i, p)
		}
		raw := append([]byte(nil), data[p:p+e]...)
		if !utf8.Valid(raw) {
			return nil, fmt.Errorf("string %d is not valid UTF-8", i)
		}
		t.Strings = append(t.Strings, raw)
	}
	return t, nil
}

// EncodeData rebuilds the TXT2 data block from Strings.
func (t *TXT2) EncodeData() []byte {
	n := len(t.Strings)
	table := 4 + n*4
	total := table
	for _, s := range t.Strings {
		total += len(s) + 1
	}
	out := make([]byte, total)
	binary.LittleEndian.PutUint32(out[0:4], uint32(n))
	p := table
	for i, s := range t.Strings {
		binary.LittleEndian.PutUint32(out[4+i*4:8+i*4], uint32(p))
		copy(out[p:], s)
		p += len(s)
		out[p] = 0
		p++
	}
	return out
}

// SetRaw replaces one string and marks the section dirty.
func (t *TXT2) SetRaw(index int, raw []byte) error {
	if index < 0 || index >= len(t.Strings) {
		return fmt.Errorf("text index %d out of range", index)
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return fmt.Errorf("text %d contains an embedded NUL", index)
	}
	if !utf8.Valid(raw) {
		return fmt.Errorf("text %d is not valid UTF-8", index)
	}
	t.Strings[index] = append([]byte(nil), raw...)
	t.Dirty = true
	return nil
}

// Encoded returns the reversible review representation of one string.
func (t *TXT2) Encoded(index int) (string, TokenStats, error) {
	if index < 0 || index >= len(t.Strings) {
		return "", TokenStats{}, fmt.Errorf("text index %d out of range", index)
	}
	return EncodeText(t.Strings[index])
}
