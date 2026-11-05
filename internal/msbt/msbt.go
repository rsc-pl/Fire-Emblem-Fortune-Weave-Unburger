// Package msbt implements a lossless reader and writer for the Nintendo
// Message Studio Binary Text (MSBT) and Message Studio Binary Project (MSBP)
// formats used by this game's message files.
//
// The implementation is intentionally strict: it rejects malformed headers,
// out-of-bounds section sizes, duplicate sections and impossible offsets. It
// preserves unknown sections byte-for-byte.
package msbt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	MagicMSBT = "MsgStdBn"
	MagicMSBP = "MsgPrjBn"
	Align     = 16
	headerLen = 0x20
	blockHdr  = 16
)

var (
	ErrShortFile       = errors.New("msbt: file is shorter than the 0x20-byte header")
	ErrBadMagic        = errors.New("msbt: unexpected magic")
	ErrBadByteOrder    = errors.New("msbt: unsupported byte order marker")
	ErrBadEncoding     = errors.New("msbt: unsupported text encoding (only UTF-8 is supported)")
	ErrBadVersion      = errors.New("msbt: unsupported format version")
	ErrBadFileSize     = errors.New("msbt: header file size does not match actual length")
	ErrBadSectionSize  = errors.New("msbt: section extends beyond end of file")
	ErrBadAlignment    = errors.New("msbt: section alignment is invalid")
	ErrDuplicateSec    = errors.New("msbt: duplicate section")
	ErrTrailingGarbage = errors.New("msbt: trailing bytes after last section")
)

// Header is the common LMS file header. Magic is either MsgStdBn (MSBT) or
// MsgPrjBn (MSBP). Raw keeps the exact header bytes so a no-op encode is
// byte-identical.
type Header struct {
	Magic        string
	ByteOrder    uint16
	Unknown      uint16
	Encoding     uint8
	Version      uint8
	SectionCount uint16
	Unknown2     uint16
	FileSize     uint32
	Padding      [10]byte
	Raw          [headerLen]byte
}

// Section is one LMS block. Data excludes the 16-byte block header and the
// alignment padding. Reserved and Trailing retain the original bytes so an
// unmodified section round-trips exactly.
type Section struct {
	Magic    string
	Size     uint32
	Reserved [8]byte
	Data     []byte
	Trailing []byte
}

// File is a parsed MSBT or MSBP file.
type File struct {
	Header   Header
	Sections []Section

	// Parsed sections (nil if absent).
	LBL1 *LabelTable
	TXT2 *TXT2
	ATR1 *ATR1
	ATO1 *ATO1
	TSY1 *TSY1
	AOCM *AOCM

	// Unknown section magics, in file order.
	Unknown []string
}

// Parse parses an MSBT file.
func Parse(data []byte) (*File, error) {
	return parseFile(data, MagicMSBT, 3)
}

// ParseMSBP parses an MSBP project file.
func ParseMSBP(data []byte) (*File, error) {
	return parseFile(data, MagicMSBP, 4)
}

func parseFile(data []byte, wantMagic string, wantVersion uint8) (*File, error) {
	if len(data) < headerLen {
		return nil, ErrShortFile
	}
	f := &File{}
	h := &f.Header
	copy(h.Raw[:], data[:headerLen])
	h.Magic = string(h.Raw[0:8])
	if h.Magic != wantMagic {
		return nil, fmt.Errorf("%w: got %q, want %q", ErrBadMagic, h.Magic, wantMagic)
	}
	// The LMS endianness field is the byte sequence FF FE for little-endian,
	// which is 0xFFFE when read big-endian.
	h.ByteOrder = binary.BigEndian.Uint16(h.Raw[8:10])
	if h.ByteOrder != 0xFFFE {
		return nil, fmt.Errorf("%w: 0x%04x", ErrBadByteOrder, h.ByteOrder)
	}
	h.Unknown = binary.LittleEndian.Uint16(h.Raw[10:12])
	h.Encoding = h.Raw[12]
	h.Version = h.Raw[13]
	if h.Encoding != 0 {
		return nil, fmt.Errorf("%w: %d", ErrBadEncoding, h.Encoding)
	}
	if h.Version != wantVersion {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrBadVersion, h.Version, wantVersion)
	}
	h.SectionCount = binary.LittleEndian.Uint16(h.Raw[14:16])
	h.Unknown2 = binary.LittleEndian.Uint16(h.Raw[16:18])
	h.FileSize = binary.LittleEndian.Uint32(h.Raw[18:22])
	copy(h.Padding[:], h.Raw[22:32])
	if int(h.FileSize) != len(data) {
		return nil, fmt.Errorf("%w: header=%d actual=%d", ErrBadFileSize, h.FileSize, len(data))
	}

	off := headerLen
	seen := make(map[string]bool, h.SectionCount)
	for i := 0; i < int(h.SectionCount); i++ {
		if off+blockHdr > len(data) {
			return nil, fmt.Errorf("%w: block %d header at 0x%x", ErrBadSectionSize, i, off)
		}
		magic := string(data[off : off+4])
		if !printableASCII(magic) {
			return nil, fmt.Errorf("%w: block %d magic %q", ErrBadMagic, i, magic)
		}
		if seen[magic] {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateSec, magic)
		}
		seen[magic] = true
		size := binary.LittleEndian.Uint32(data[off+4 : off+8])
		dataStart := off + blockHdr
		dataEnd := dataStart + int(size)
		if dataEnd < dataStart || dataEnd > len(data) {
			return nil, fmt.Errorf("%w: %s size %d at 0x%x", ErrBadSectionSize, magic, size, off)
		}
		next := align16(dataEnd)
		if next > len(data) {
			return nil, fmt.Errorf("%w: %s padding to 0x%x beyond 0x%x", ErrBadAlignment, magic, next, len(data))
		}
		sec := Section{
			Magic:    magic,
			Size:     size,
			Data:     data[dataStart:dataEnd],
			Trailing: append([]byte(nil), data[dataEnd:next]...),
		}
		copy(sec.Reserved[:], data[off+8:off+16])
		f.Sections = append(f.Sections, sec)
		off = next
	}
	if off != len(data) {
		return nil, fmt.Errorf("%w: ended at 0x%x, length 0x%x", ErrTrailingGarbage, off, len(data))
	}

	// Parse known sections. TXT2 must be available before LBL1 label-index
	// validation.
	for i := range f.Sections {
		s := &f.Sections[i]
		switch s.Magic {
		case "TXT2":
			t, err := parseTXT2(s.Data)
			if err != nil {
				return nil, fmt.Errorf("TXT2: %w", err)
			}
			f.TXT2 = t
		case "ATR1":
			a, err := parseATR1(s.Data)
			if err != nil {
				return nil, fmt.Errorf("ATR1: %w", err)
			}
			f.ATR1 = a
		case "ATO1":
			a, err := parseATO1(s.Data)
			if err != nil {
				return nil, fmt.Errorf("ATO1: %w", err)
			}
			f.ATO1 = a
		case "TSY1":
			t, err := parseTSY1(s.Data)
			if err != nil {
				return nil, fmt.Errorf("TSY1: %w", err)
			}
			f.TSY1 = t
		case "AOCM":
			a, err := parseAOCM(s.Data)
			if err != nil {
				return nil, fmt.Errorf("AOCM: %w", err)
			}
			f.AOCM = a
		}
	}
	for i := range f.Sections {
		s := &f.Sections[i]
		if s.Magic != "LBL1" {
			continue
		}
		l, err := parseLBL1(s.Data, f.textCount())
		if err != nil {
			return nil, fmt.Errorf("LBL1: %w", err)
		}
		f.LBL1 = l
	}
	if f.TSY1 != nil && f.TXT2 != nil && len(f.TSY1.Values) != len(f.TXT2.Strings) {
		return nil, fmt.Errorf("TSY1: style count %d does not match text count %d", len(f.TSY1.Values), len(f.TXT2.Strings))
	}
	if f.AOCM != nil && f.TXT2 != nil && int(f.AOCM.Count) != len(f.TXT2.Strings) {
		return nil, fmt.Errorf("AOCM: flag count %d does not match text count %d", f.AOCM.Count, len(f.TXT2.Strings))
	}
	f.collectUnknown()
	return f, nil
}

func (f *File) collectUnknown() {
	known := map[string]bool{
		"LBL1": true, "TXT2": true, "ATR1": true, "ATO1": true, "TSY1": true, "AOCM": true,
	}
	for _, s := range f.Sections {
		if !known[s.Magic] {
			f.Unknown = append(f.Unknown, s.Magic)
		}
	}
}

func (f *File) textCount() int {
	if f.TXT2 == nil {
		return 0
	}
	return len(f.TXT2.Strings)
}

// Section returns the section with the given magic, or nil.
func (f *File) Section(magic string) *Section {
	for i := range f.Sections {
		if f.Sections[i].Magic == magic {
			return &f.Sections[i]
		}
	}
	return nil
}

// Encode serializes the file. Unmodified sections are written byte-for-byte
// (including their original trailing alignment bytes). The TXT2 section is
// rebuilt from parsed strings whenever it has been marked dirty.
func (f *File) Encode() ([]byte, error) {
	if int(f.Header.SectionCount) != len(f.Sections) {
		return nil, fmt.Errorf("msbt: header section count %d does not match %d sections", f.Header.SectionCount, len(f.Sections))
	}
	var body bytes.Buffer
	for i := range f.Sections {
		s := &f.Sections[i]
		if len(s.Magic) != 4 {
			return nil, fmt.Errorf("msbt: invalid section magic %q", s.Magic)
		}
		data := s.Data
		dirtyTXT2 := s.Magic == "TXT2" && f.TXT2 != nil && f.TXT2.Dirty
		var trailing []byte
		if dirtyTXT2 {
			data = f.TXT2.EncodeData()
			need := align16(blockHdr + len(data))
			trailing = bytes.Repeat([]byte{0xAB}, need-blockHdr-len(data))
		} else {
			if len(data) != int(s.Size) {
				return nil, fmt.Errorf("msbt: section %s size %d does not match data length %d", s.Magic, s.Size, len(data))
			}
			trailing = s.Trailing
		}
		body.WriteString(s.Magic)
		var sz [4]byte
		binary.LittleEndian.PutUint32(sz[:], uint32(len(data)))
		body.Write(sz[:])
		body.Write(s.Reserved[:])
		body.Write(data)
		body.Write(trailing)
	}
	fileSize := headerLen + body.Len()
	if fileSize > int(^uint32(0)) {
		return nil, errors.New("msbt: encoded file too large")
	}
	out := make([]byte, headerLen+body.Len())
	copy(out, f.Header.Raw[:])
	binary.LittleEndian.PutUint16(out[14:16], uint16(len(f.Sections)))
	binary.LittleEndian.PutUint32(out[18:22], uint32(fileSize))
	copy(out[headerLen:], body.Bytes())
	return out, nil
}

// ReplaceText replaces one TXT2 string with raw UTF-8 bytes (control bytes
// included). The writer recomputes offsets and section sizes; all other
// sections remain byte-identical.
func (f *File) ReplaceText(index int, raw []byte) error {
	if f.TXT2 == nil {
		return errors.New("msbt: no TXT2 section")
	}
	return f.TXT2.SetRaw(index, raw)
}

func align16(n int) int {
	return (n + Align - 1) &^ (Align - 1)
}

func printableASCII(s string) bool {
	if len(s) != 4 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7E {
			return false
		}
	}
	return true
}
