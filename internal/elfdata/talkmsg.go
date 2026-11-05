// Package elfdata reads the small amount of ELF metadata used by the
// message-call-site index in TalkMsg.elf. It is not a general ELF loader.
package elfdata

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	elfClass64    = 2
	elfDataLE     = 1
	elfTypeRel    = 1
	elfAArch64    = 183
	rAArch64Abs64 = 257
)

type section struct {
	name    string
	typ     uint32
	offset  uint64
	size    uint64
	link    uint32
	info    uint32
	entsize uint64
}

type symbol struct {
	name  string
	shndx uint16
	value uint64
	size  uint64
}

type reloc struct {
	offset uint64
	info   uint64
	addend int64
}

// TalkMsg maps message labels (MID_*) to the byte in talkSection/demoSection
// that references them. A label present in this index is an asset-referenced
// message, not a runtime-proof of visibility.
type TalkMsg struct {
	Labels     []string
	TalkFlags  []byte
	DemoLabels []string
	DemoFlags  []byte
	TalkIndex  map[string]int
	DemoIndex  map[string]int
	// Info is the raw 40-byte info symbol when present.
	Info []byte
}

// Lookup reports whether a message label is referenced by TalkMsg.
func (t *TalkMsg) Lookup(label string) (kind string, index int, flag byte, ok bool) {
	if t == nil {
		return "", 0, 0, false
	}
	if i, ok := t.TalkIndex[label]; ok {
		var f byte
		if i < len(t.TalkFlags) {
			f = t.TalkFlags[i]
		}
		return "talk", i, f, true
	}
	if i, ok := t.DemoIndex[label]; ok {
		var f byte
		if i < len(t.DemoFlags) {
			f = t.DemoFlags[i]
		}
		return "demo", i, f, true
	}
	return "", 0, 0, false
}

// ParseTalkMsg parses the decompressed TalkMsg.elf relocatable object.
func ParseTalkMsg(data []byte) (*TalkMsg, error) {
	if len(data) < 64 {
		return nil, fmt.Errorf("elf: file shorter than 64 bytes")
	}
	if !bytes.Equal(data[0:4], []byte{0x7f, 'E', 'L', 'F'}) {
		return nil, fmt.Errorf("elf: bad magic")
	}
	if data[4] != elfClass64 || data[5] != elfDataLE {
		return nil, fmt.Errorf("elf: want ELF64 little-endian")
	}
	if binary.LittleEndian.Uint16(data[16:18]) != elfTypeRel {
		return nil, fmt.Errorf("elf: want relocatable object")
	}
	if binary.LittleEndian.Uint16(data[18:20]) != elfAArch64 {
		return nil, fmt.Errorf("elf: want AArch64")
	}
	shoff := binary.LittleEndian.Uint64(data[0x28:0x30])
	shentsize := binary.LittleEndian.Uint16(data[0x3A:0x3C])
	shnum := binary.LittleEndian.Uint16(data[0x3C:0x3E])
	shstrndx := binary.LittleEndian.Uint16(data[0x3E:0x40])
	if shentsize < 64 || shnum == 0 {
		return nil, fmt.Errorf("elf: invalid section table")
	}
	if shoff+uint64(shentsize)*uint64(shnum) > uint64(len(data)) {
		return nil, fmt.Errorf("elf: section table out of range")
	}
	secs := make([]section, shnum)
	for i := 0; i < int(shnum); i++ {
		p := int(shoff) + i*int(shentsize)
		s := section{
			typ:     binary.LittleEndian.Uint32(data[p+4 : p+8]),
			offset:  binary.LittleEndian.Uint64(data[p+24 : p+32]),
			size:    binary.LittleEndian.Uint64(data[p+32 : p+40]),
			link:    binary.LittleEndian.Uint32(data[p+40 : p+44]),
			info:    binary.LittleEndian.Uint32(data[p+44 : p+48]),
			entsize: binary.LittleEndian.Uint64(data[p+56 : p+64]),
		}
		if s.offset+s.size > uint64(len(data)) {
			return nil, fmt.Errorf("elf: section %d out of range", i)
		}
		secs[i] = s
	}
	if int(shstrndx) >= len(secs) {
		return nil, fmt.Errorf("elf: invalid section-name table index")
	}
	strtab := secs[shstrndx]
	for i := range secs {
		nameOff := binary.LittleEndian.Uint32(data[int(shoff)+i*int(shentsize) : int(shoff)+i*int(shentsize)+4])
		if uint64(nameOff) >= strtab.size {
			return nil, fmt.Errorf("elf: section %d name out of range", i)
		}
		p := int(strtab.offset) + int(nameOff)
		end := int(strtab.offset + strtab.size)
		e := bytes.IndexByte(data[p:end], 0)
		if e < 0 {
			return nil, fmt.Errorf("elf: section %d name has no NUL", i)
		}
		secs[i].name = string(data[p : p+e])
	}
	byName := map[string]int{}
	for i, s := range secs {
		byName[s.name] = i
	}
	dataIdx, ok := byName[".data"]
	if !ok {
		return nil, fmt.Errorf("elf: no .data section")
	}
	roIdx, ok := byName[".rodata.str1.1"]
	if !ok {
		return nil, fmt.Errorf("elf: no .rodata.str1.1 section")
	}
	symIdx, ok := byName[".symtab"]
	if !ok {
		return nil, fmt.Errorf("elf: no .symtab section")
	}
	relaIdx, ok := byName[".rela.data"]
	if !ok {
		return nil, fmt.Errorf("elf: no .rela.data section")
	}
	syms, err := parseSymbols(data, secs, symIdx)
	if err != nil {
		return nil, err
	}
	symByName := map[string]symbol{}
	for _, s := range syms {
		symByName[s.name] = s
	}
	relocs, err := parseRelocs(data, secs[relaIdx])
	if err != nil {
		return nil, err
	}
	ro := secs[roIdx]
	readString := func(addend int64) (string, error) {
		if addend < 0 || uint64(addend) >= ro.size {
			return "", fmt.Errorf("elf: string addend %d out of range", addend)
		}
		p := int(ro.offset) + int(addend)
		end := int(ro.offset + ro.size)
		e := bytes.IndexByte(data[p:end], 0)
		if e < 0 {
			return "", fmt.Errorf("elf: string at %d has no NUL", p)
		}
		return string(data[p : p+e]), nil
	}
	tm := &TalkMsg{TalkIndex: map[string]int{}, DemoIndex: map[string]int{}}
	if s, ok := symByName["talkLabel"]; ok {
		var flags symbol
		if fs, ok := symByName["talkSection"]; ok {
			flags = fs
		}
		tm.Labels, tm.TalkFlags, err = buildLabelIndex(data, secs, dataIdx, s, flags, relocs, readString)
		if err != nil {
			return nil, fmt.Errorf("talkLabel: %w", err)
		}
		for i, l := range tm.Labels {
			tm.TalkIndex[l] = i
		}
	}
	if s, ok := symByName["demoLabel"]; ok {
		var flags symbol
		if fs, ok := symByName["demoSection"]; ok {
			flags = fs
		}
		tm.DemoLabels, tm.DemoFlags, err = buildLabelIndex(data, secs, dataIdx, s, flags, relocs, readString)
		if err != nil {
			return nil, fmt.Errorf("demoLabel: %w", err)
		}
		for i, l := range tm.DemoLabels {
			tm.DemoIndex[l] = i
		}
	}
	if s, ok := symByName["info"]; ok {
		if s.size > 0 && s.shndx < uint16(len(secs)) && s.value+s.size <= secs[s.shndx].size {
			start := int(secs[s.shndx].offset + s.value)
			tm.Info = append([]byte(nil), data[start:start+int(s.size)]...)
		}
	}
	return tm, nil
}

func parseSymbols(data []byte, secs []section, idx int) ([]symbol, error) {
	s := secs[idx]
	if s.link >= uint32(len(secs)) {
		return nil, fmt.Errorf("elf: symtab string table index out of range")
	}
	strSec := secs[s.link]
	out := make([]symbol, 0)
	ent := s.entsize
	if ent == 0 {
		ent = 24
	}
	if ent < 24 {
		return nil, fmt.Errorf("elf: symtab entry size %d", ent)
	}
	for p := s.offset; p+ent <= s.offset+s.size; p += ent {
		nameOff := binary.LittleEndian.Uint32(data[p : p+4])
		sym := symbol{
			shndx: binary.LittleEndian.Uint16(data[p+6 : p+8]),
			value: binary.LittleEndian.Uint64(data[p+8 : p+16]),
			size:  binary.LittleEndian.Uint64(data[p+16 : p+24]),
		}
		if nameOff != 0 {
			if uint64(nameOff) >= strSec.size {
				return nil, fmt.Errorf("elf: symbol name out of range")
			}
			sp := int(strSec.offset) + int(nameOff)
			end := int(strSec.offset + strSec.size)
			e := bytes.IndexByte(data[sp:end], 0)
			if e < 0 {
				return nil, fmt.Errorf("elf: symbol name has no NUL")
			}
			sym.name = string(data[sp : sp+e])
		}
		out = append(out, sym)
	}
	return out, nil
}

func parseRelocs(data []byte, s section) ([]reloc, error) {
	ent := s.entsize
	if ent == 0 {
		ent = 24
	}
	if ent < 24 {
		return nil, fmt.Errorf("elf: rela entry size %d", ent)
	}
	out := make([]reloc, 0)
	for p := s.offset; p+ent <= s.offset+s.size; p += ent {
		out = append(out, reloc{
			offset: binary.LittleEndian.Uint64(data[p : p+8]),
			info:   binary.LittleEndian.Uint64(data[p+8 : p+16]),
			addend: int64(binary.LittleEndian.Uint64(data[p+16 : p+24])),
		})
	}
	return out, nil
}

func buildLabelIndex(data []byte, secs []section, dataIdx int, sym, flagsSym symbol, relocs []reloc, readString func(int64) (string, error)) ([]string, []byte, error) {
	if int(sym.shndx) != dataIdx {
		return nil, nil, fmt.Errorf("label symbol is not in .data")
	}
	count := int(sym.size / 8)
	labels := make([]string, count)
	for _, r := range relocs {
		if r.offset < sym.value || r.offset >= sym.value+sym.size {
			continue
		}
		if r.info&0xffffffff != rAArch64Abs64 {
			continue
		}
		idx := int((r.offset - sym.value) / 8)
		if idx < 0 || idx >= count {
			continue
		}
		s, err := readString(r.addend)
		if err != nil {
			return nil, nil, err
		}
		labels[idx] = s
	}
	var flags []byte
	if flagsSym.size > 0 && flagsSym.shndx < uint16(len(secs)) {
		flagSec := secs[flagsSym.shndx]
		if flagsSym.value+flagsSym.size <= flagSec.size {
			start := int(flagSec.offset + flagsSym.value)
			flags = append([]byte(nil), data[start:start+int(flagsSym.size)]...)
		}
	}
	return labels, flags, nil
}
