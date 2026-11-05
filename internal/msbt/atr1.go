package msbt

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// ATO1 lists which MSBP attributes are actually present in ATR1 and at which
// byte offset within each fixed-size attribute record. 0xFFFFFFFF means the
// attribute is not stored for this bank.
type ATO1 struct {
	Offsets []uint32
	present map[uint32]bool
}

func parseATO1(data []byte) (*ATO1, error) {
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("section length %d is not a multiple of 4", len(data))
	}
	a := &ATO1{present: map[uint32]bool{}}
	for i := 0; i+4 <= len(data); i += 4 {
		v := binary.LittleEndian.Uint32(data[i : i+4])
		a.Offsets = append(a.Offsets, v)
		if v != 0xFFFFFFFF {
			a.present[v] = true
		}
	}
	return a, nil
}

// PresentOffset reports whether an attribute at the given byte offset is
// stored in ATR1. ATO1's slot order is not the ATI2 attribute order, so the
// offset value (which is unique per attribute) is the reliable key.
func (a *ATO1) PresentOffset(offset uint32) bool {
	if a == nil {
		return false
	}
	return a.present[offset]
}

// ValidateOffsets checks that every non-sentinel ATO1 offset names an ATI2
// attribute. Unknown offsets are a hard error because decoding them would be
// guesswork.
func (a *ATO1) ValidateOffsets(defs []AttributeDef) error {
	if a == nil {
		return nil
	}
	known := map[uint32]bool{}
	for _, d := range defs {
		known[d.Offset] = true
	}
	for i, v := range a.Offsets {
		if v == 0xFFFFFFFF {
			continue
		}
		if !known[v] {
			return fmt.Errorf("ATO1 slot %d offset %d does not match any ATI2 attribute", i, v)
		}
	}
	return nil
}

// ATR1 stores a fixed-size attribute record per message followed by a shared
// string area. String attributes hold a u32 offset relative to the beginning
// of the ATR1 section data.
type ATR1 struct {
	Count             uint32
	BytesPerAttribute uint32
	Attributes        []byte
	Strings           []byte
}

func parseATR1(data []byte) (*ATR1, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("section shorter than 8 bytes")
	}
	a := &ATR1{
		Count:             binary.LittleEndian.Uint32(data[0:4]),
		BytesPerAttribute: binary.LittleEndian.Uint32(data[4:8]),
	}
	need := 8 + uint64(a.Count)*uint64(a.BytesPerAttribute)
	if need > uint64(len(data)) {
		return nil, fmt.Errorf("attribute table needs %d bytes, have %d", need, len(data))
	}
	a.Attributes = data[8:need]
	a.Strings = data[need:]
	return a, nil
}

// String reads a NUL-terminated string at the given section-relative offset.
func (a *ATR1) String(off uint32) (string, error) {
	if int(off) >= len(a.Attributes)+8+len(a.Strings) {
		return "", fmt.Errorf("string offset %d out of range", off)
	}
	// The offset is relative to the whole ATR1 data block, which starts with
	// the two u32 header fields.
	start := int(off) - 8
	if start < 0 {
		return "", fmt.Errorf("string offset %d points inside the ATR1 header", off)
	}
	full := a.Attributes
	if start < len(full) {
		return "", fmt.Errorf("string offset %d points inside the attribute table", off)
	}
	p := start - len(full)
	if p < 0 || p >= len(a.Strings) {
		return "", fmt.Errorf("string offset %d out of range", off)
	}
	e := bytes.IndexByte(a.Strings[p:], 0)
	if e < 0 {
		return "", fmt.Errorf("string at offset %d has no NUL terminator", off)
	}
	return string(a.Strings[p : p+e]), nil
}

// TSY1 contains a style index per message.
type TSY1 struct {
	Values []uint32
}

func parseTSY1(data []byte) (*TSY1, error) {
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("section length %d is not a multiple of 4", len(data))
	}
	t := &TSY1{}
	for i := 0; i+4 <= len(data); i += 4 {
		t.Values = append(t.Values, binary.LittleEndian.Uint32(data[i:i+4]))
	}
	return t, nil
}

// AOCM is a per-message byte flag array (count u16 followed by count bytes).
type AOCM struct {
	Count uint16
	Flags []byte
}

func parseAOCM(data []byte) (*AOCM, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("section shorter than 2 bytes")
	}
	a := &AOCM{Count: binary.LittleEndian.Uint16(data[0:2])}
	if int(a.Count) != len(data)-2 {
		return nil, fmt.Errorf("declares %d flags, has %d bytes", a.Count, len(data)-2)
	}
	a.Flags = append([]byte(nil), data[2:]...)
	return a, nil
}
