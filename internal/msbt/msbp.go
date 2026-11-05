package msbt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
)

// AttributeDef is one ATI2 attribute declaration.
type AttributeDef struct {
	Type   uint8
	EnumID uint16
	Offset uint32
	Name   string
}

// AttributeSchema binds ATI2 declarations to ALB1 labels and ALI2 enums.
type AttributeSchema struct {
	Defs   []AttributeDef
	Labels *LabelTable
	Enums  [][]string
}

// Color is one CLR1 entry.
type Color struct {
	R, G, B, A uint8
}

// Style is one SYL3 entry.
type Style struct {
	RegionWidth uint32
	LineNum     uint32
	FontIndex   uint32
	BaseColor   uint32
}

// TagGroup is one TGG2 tag group.
type TagGroup struct {
	ID   uint16
	Name string
	Tags []uint16
}

// Tag is one TAG2 control tag.
type Tag struct {
	Name   string
	Params []uint16
}

// TagParam is one TGP2 tag parameter.
type TagParam struct {
	Type        uint8
	Name        string
	EnumMembers []uint16
}

// TagSchema describes the control-tag definitions from an MSBP file.
type TagSchema struct {
	Groups     []TagGroup
	Tags       []Tag
	Params     []TagParam
	StringList []string
}

// Project is the parsed subset of an MSBP file needed by the importer.
type Project struct {
	File     *File
	Schema   *AttributeSchema
	Colors   []Color
	ColorLbl *LabelTable
	Styles   []Style
	StyleLbl *LabelTable
	Tags     *TagSchema
}

// ParseProject parses an MSBP file and its schema.
func ParseProject(data []byte) (*Project, error) {
	f, err := ParseMSBP(data)
	if err != nil {
		return nil, err
	}
	p := &Project{File: f}
	if s := f.Section("ALB1"); s != nil {
		p.Schema = &AttributeSchema{}
		p.Schema.Labels, err = parseLabelTable(s.Data)
		if err != nil {
			return nil, fmt.Errorf("ALB1: %w", err)
		}
	}
	if s := f.Section("ATI2"); s != nil {
		if p.Schema == nil {
			p.Schema = &AttributeSchema{}
		}
		p.Schema.Defs, err = parseATI2(s.Data)
		if err != nil {
			return nil, fmt.Errorf("ATI2: %w", err)
		}
		for i := range p.Schema.Defs {
			if p.Schema.Labels != nil {
				p.Schema.Defs[i].Name, _ = p.Schema.Labels.CanonicalLabel(uint32(i))
			}
		}
	}
	if s := f.Section("ALI2"); s != nil {
		if p.Schema == nil {
			p.Schema = &AttributeSchema{}
		}
		p.Schema.Enums, err = parseALI2(s.Data)
		if err != nil {
			return nil, fmt.Errorf("ALI2: %w", err)
		}
	}
	if s := f.Section("CLR1"); s != nil {
		p.Colors, err = parseCLR1(s.Data)
		if err != nil {
			return nil, fmt.Errorf("CLR1: %w", err)
		}
	}
	if s := f.Section("CLB1"); s != nil {
		p.ColorLbl, err = parseLabelTable(s.Data)
		if err != nil {
			return nil, fmt.Errorf("CLB1: %w", err)
		}
	}
	if s := f.Section("SYL3"); s != nil {
		p.Styles, err = parseSYL3(s.Data)
		if err != nil {
			return nil, fmt.Errorf("SYL3: %w", err)
		}
	}
	if s := f.Section("SLB1"); s != nil {
		p.StyleLbl, err = parseLabelTable(s.Data)
		if err != nil {
			return nil, fmt.Errorf("SLB1: %w", err)
		}
	}
	p.Tags, err = parseTagSchema(f)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// EnumName resolves an ATI2 enum value for the given attribute.
func (s *AttributeSchema) EnumName(attrIndex int, value uint32) string {
	if s == nil || attrIndex < 0 || attrIndex >= len(s.Defs) {
		return ""
	}
	id := int(s.Defs[attrIndex].EnumID)
	if id < 0 || id >= len(s.Enums) {
		return ""
	}
	if int(value) < len(s.Enums[id]) {
		return s.Enums[id][value]
	}
	return ""
}

// AttrValue is a decoded ATR1 attribute value.
type AttrValue struct {
	Index int
	Name  string
	Type  uint8
	// String is set for type 8. Enum is set for type 9.
	String   string
	Enum     uint32
	EnumName string
	Int      int64
	Uint     uint64
	Float    float64
	Raw      []byte
}

// ExtractAttributes decodes all attributes declared present by ATO1 using the
// supplied schema. It validates every offset and string reference.
func (a *ATR1) ExtractAttributes(ato *ATO1, schema *AttributeSchema) ([][]AttrValue, error) {
	if a == nil || schema == nil {
		return nil, nil
	}
	if ato != nil {
		if err := ato.ValidateOffsets(schema.Defs); err != nil {
			return nil, err
		}
	}
	out := make([][]AttrValue, 0, a.Count)
	for rec := 0; rec < int(a.Count); rec++ {
		base := rec * int(a.BytesPerAttribute)
		if base+int(a.BytesPerAttribute) > len(a.Attributes) {
			return nil, fmt.Errorf("record %d exceeds attribute table", rec)
		}
		row := make([]AttrValue, 0, len(schema.Defs))
		for i, def := range schema.Defs {
			if ato != nil && !ato.PresentOffset(def.Offset) {
				continue
			}
			off := def.Offset
			size := typeSize(def.Type)
			if size == 0 {
				return nil, fmt.Errorf("attribute %d (%s) has unsupported type %d", i, def.Name, def.Type)
			}
			if uint64(off)+uint64(size) > uint64(a.BytesPerAttribute) {
				return nil, fmt.Errorf("attribute %d (%s) offset %d size %d exceeds record size %d", i, def.Name, off, size, a.BytesPerAttribute)
			}
			p := base + int(off)
			v := AttrValue{Index: i, Name: def.Name, Type: def.Type}
			switch def.Type {
			case 0:
				v.Int = int64(int8(a.Attributes[p]))
			case 1:
				v.Uint = uint64(a.Attributes[p])
				v.Int = int64(a.Attributes[p])
			case 2:
				v.Uint = uint64(binary.LittleEndian.Uint16(a.Attributes[p : p+2]))
				v.Int = int64(int16(v.Uint))
			case 3:
				v.Uint = uint64(binary.LittleEndian.Uint16(a.Attributes[p : p+2]))
				v.Int = int64(v.Uint)
			case 4:
				v.Uint = uint64(binary.LittleEndian.Uint32(a.Attributes[p : p+4]))
				v.Int = int64(int32(v.Uint))
			case 5:
				v.Uint = uint64(binary.LittleEndian.Uint32(a.Attributes[p : p+4]))
				v.Int = int64(v.Uint)
			case 6:
				v.Uint = uint64(binary.LittleEndian.Uint32(a.Attributes[p : p+4]))
				v.Float = float64(float32frombits(uint32(v.Uint)))
			case 7:
				v.Uint = binary.LittleEndian.Uint64(a.Attributes[p : p+8])
				v.Float = float64frombits(v.Uint)
			case 8:
				so := binary.LittleEndian.Uint32(a.Attributes[p : p+4])
				s, err := a.String(so)
				if err != nil {
					return nil, fmt.Errorf("record %d attribute %d (%s): %w", rec, i, def.Name, err)
				}
				v.String = s
			case 9:
				// Enum values are one byte wide. Their offsets in ATI2 are
				// adjacent for adjacent enum attributes.
				v.Enum = uint32(a.Attributes[p])
				v.Uint = uint64(v.Enum)
				v.Int = int64(v.Enum)
				v.EnumName = schema.EnumName(i, v.Enum)
			default:
				return nil, fmt.Errorf("attribute %d (%s) unsupported type %d", i, def.Name, def.Type)
			}
			row = append(row, v)
		}
		out = append(out, row)
	}
	return out, nil
}

func typeSize(t uint8) int {
	switch t {
	case 0, 1, 9:
		return 1
	case 2, 3:
		return 2
	case 4, 5, 6, 8:
		return 4
	case 7:
		return 8
	default:
		return 0
	}
}

func float32frombits(v uint32) float32 { return math.Float32frombits(v) }
func float64frombits(v uint64) float64 { return math.Float64frombits(v) }

func parseATI2(data []byte) ([]AttributeDef, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("section shorter than 4 bytes")
	}
	n := binary.LittleEndian.Uint32(data[0:4])
	if n > 1<<16 {
		return nil, fmt.Errorf("implausible attribute count %d", n)
	}
	need := 4 + int(n)*8
	if need > len(data) {
		return nil, fmt.Errorf("attribute table needs %d bytes, have %d", need, len(data))
	}
	out := make([]AttributeDef, 0, n)
	for i := 0; i < int(n); i++ {
		p := 4 + i*8
		out = append(out, AttributeDef{
			Type:   data[p],
			EnumID: binary.LittleEndian.Uint16(data[p+2 : p+4]),
			Offset: binary.LittleEndian.Uint32(data[p+4 : p+8]),
		})
	}
	return out, nil
}

func parseALI2(data []byte) ([][]string, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("section shorter than 4 bytes")
	}
	n := binary.LittleEndian.Uint32(data[0:4])
	if n > 1<<16 {
		return nil, fmt.Errorf("implausible enum count %d", n)
	}
	if 4+int(n)*4 > len(data) {
		return nil, fmt.Errorf("enum offset table out of range")
	}
	out := make([][]string, 0, n)
	for i := 0; i < int(n); i++ {
		off := binary.LittleEndian.Uint32(data[4+i*4 : 8+i*4])
		if int(off) >= len(data) {
			return nil, fmt.Errorf("enum %d offset %d out of range", i, off)
		}
		p := int(off)
		if p+4 > len(data) {
			return nil, fmt.Errorf("enum %d declaration truncated", i)
		}
		cn := binary.LittleEndian.Uint32(data[p : p+4])
		if p+4+int(cn)*4 > len(data) {
			return nil, fmt.Errorf("enum %d member table out of range", i)
		}
		var members []string
		for j := 0; j < int(cn); j++ {
			so := binary.LittleEndian.Uint32(data[p+4+j*4 : p+8+j*4])
			if p+int(so) >= len(data) {
				return nil, fmt.Errorf("enum %d member %d offset out of range", i, j)
			}
			sp := p + int(so)
			e := bytes.IndexByte(data[sp:], 0)
			if e < 0 {
				return nil, fmt.Errorf("enum %d member %d has no NUL", i, j)
			}
			members = append(members, string(data[sp:sp+e]))
		}
		out = append(out, members)
	}
	return out, nil
}

func parseCLR1(data []byte) ([]Color, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("section shorter than 4 bytes")
	}
	n := binary.LittleEndian.Uint32(data[0:4])
	if n > 1<<20 || 4+int(n)*4 > len(data) {
		return nil, fmt.Errorf("color table out of range")
	}
	out := make([]Color, 0, n)
	for i := 0; i < int(n); i++ {
		p := 4 + i*4
		out = append(out, Color{R: data[p], G: data[p+1], B: data[p+2], A: data[p+3]})
	}
	return out, nil
}

func parseSYL3(data []byte) ([]Style, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("section shorter than 4 bytes")
	}
	n := binary.LittleEndian.Uint32(data[0:4])
	if n > 1<<20 || 4+int(n)*16 > len(data) {
		return nil, fmt.Errorf("style table out of range")
	}
	out := make([]Style, 0, n)
	for i := 0; i < int(n); i++ {
		p := 4 + i*16
		out = append(out, Style{
			RegionWidth: binary.LittleEndian.Uint32(data[p : p+4]),
			LineNum:     binary.LittleEndian.Uint32(data[p+4 : p+8]),
			FontIndex:   binary.LittleEndian.Uint32(data[p+8 : p+12]),
			BaseColor:   binary.LittleEndian.Uint32(data[p+12 : p+16]),
		})
	}
	return out, nil
}

func parseTagSchema(f *File) (*TagSchema, error) {
	t := &TagSchema{}
	var err error
	if s := f.Section("TGL2"); s != nil {
		t.StringList, err = parseStringList(s.Data)
		if err != nil {
			return nil, fmt.Errorf("TGL2: %w", err)
		}
	}
	if s := f.Section("TGP2"); s != nil {
		t.Params, err = parseTagParams(s.Data)
		if err != nil {
			return nil, fmt.Errorf("TGP2: %w", err)
		}
	}
	if s := f.Section("TAG2"); s != nil {
		t.Tags, err = parseTags(s.Data)
		if err != nil {
			return nil, fmt.Errorf("TAG2: %w", err)
		}
	}
	if s := f.Section("TGG2"); s != nil {
		t.Groups, err = parseTagGroups(s.Data)
		if err != nil {
			return nil, fmt.Errorf("TGG2: %w", err)
		}
	}
	return t, nil
}

func parseStringList(data []byte) ([]string, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("section shorter than 4 bytes")
	}
	n := binary.LittleEndian.Uint16(data[0:2])
	if 4+int(n)*4 > len(data) {
		return nil, fmt.Errorf("offset table out of range")
	}
	out := make([]string, 0, n)
	for i := 0; i < int(n); i++ {
		off := binary.LittleEndian.Uint32(data[4+i*4 : 8+i*4])
		if int(off) >= len(data) {
			return nil, fmt.Errorf("string %d out of range", i)
		}
		p := int(off)
		e := bytes.IndexByte(data[p:], 0)
		if e < 0 {
			return nil, fmt.Errorf("string %d has no NUL", i)
		}
		out = append(out, string(data[p:p+e]))
	}
	return out, nil
}

func parseTagParams(data []byte) ([]TagParam, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("section shorter than 4 bytes")
	}
	n := binary.LittleEndian.Uint16(data[0:2])
	if 4+int(n)*4 > len(data) {
		return nil, fmt.Errorf("offset table out of range")
	}
	out := make([]TagParam, 0, n)
	for i := 0; i < int(n); i++ {
		off := binary.LittleEndian.Uint32(data[4+i*4 : 8+i*4])
		if int(off) >= len(data) {
			return nil, fmt.Errorf("parameter %d out of range", i)
		}
		p := int(off)
		if p >= len(data) {
			return nil, fmt.Errorf("parameter %d out of range", i)
		}
		param := TagParam{Type: data[p]}
		p++
		if param.Type == 9 {
			// Enum parameter: type, padding, u16 member count, u16 members,
			// then the NUL-terminated parameter name.
			if p+3 > len(data) {
				return nil, fmt.Errorf("enum parameter %d truncated", i)
			}
			p++ // padding
			cnt := binary.LittleEndian.Uint16(data[p : p+2])
			p += 2
			if p+int(cnt)*2 > len(data) {
				return nil, fmt.Errorf("enum parameter %d members out of range", i)
			}
			for j := 0; j < int(cnt); j++ {
				param.EnumMembers = append(param.EnumMembers, binary.LittleEndian.Uint16(data[p+j*2:p+j*2+2]))
			}
			p += int(cnt) * 2
		}
		e := bytes.IndexByte(data[p:], 0)
		if e < 0 {
			return nil, fmt.Errorf("parameter %d name has no NUL", i)
		}
		param.Name = string(data[p : p+e])
		out = append(out, param)
	}
	return out, nil
}

func parseTags(data []byte) ([]Tag, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("section shorter than 4 bytes")
	}
	n := binary.LittleEndian.Uint16(data[0:2])
	if 4+int(n)*4 > len(data) {
		return nil, fmt.Errorf("offset table out of range")
	}
	out := make([]Tag, 0, n)
	for i := 0; i < int(n); i++ {
		off := binary.LittleEndian.Uint32(data[4+i*4 : 8+i*4])
		if int(off) >= len(data) {
			return nil, fmt.Errorf("tag %d out of range", i)
		}
		p := int(off)
		cnt := binary.LittleEndian.Uint16(data[p : p+2])
		p += 2
		if p+int(cnt)*2 > len(data) {
			return nil, fmt.Errorf("tag %d parameter list out of range", i)
		}
		tag := Tag{}
		for j := 0; j < int(cnt); j++ {
			tag.Params = append(tag.Params, binary.LittleEndian.Uint16(data[p+j*2:p+j*2+2]))
		}
		p += int(cnt) * 2
		e := bytes.IndexByte(data[p:], 0)
		if e < 0 {
			return nil, fmt.Errorf("tag %d name has no NUL", i)
		}
		tag.Name = string(data[p : p+e])
		out = append(out, tag)
	}
	return out, nil
}

func parseTagGroups(data []byte) ([]TagGroup, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("section shorter than 4 bytes")
	}
	n := binary.LittleEndian.Uint16(data[0:2])
	if 4+int(n)*4 > len(data) {
		return nil, fmt.Errorf("offset table out of range")
	}
	out := make([]TagGroup, 0, n)
	for i := 0; i < int(n); i++ {
		off := binary.LittleEndian.Uint32(data[4+i*4 : 8+i*4])
		if int(off) >= len(data) {
			return nil, fmt.Errorf("group %d out of range", i)
		}
		p := int(off)
		g := TagGroup{}
		g.ID = binary.LittleEndian.Uint16(data[p : p+2])
		cnt := binary.LittleEndian.Uint16(data[p+2 : p+4])
		p += 4
		if p+int(cnt)*2 > len(data) {
			return nil, fmt.Errorf("group %d tag list out of range", i)
		}
		for j := 0; j < int(cnt); j++ {
			g.Tags = append(g.Tags, binary.LittleEndian.Uint16(data[p+j*2:p+j*2+2]))
		}
		p += int(cnt) * 2
		e := bytes.IndexByte(data[p:], 0)
		if e < 0 {
			return nil, fmt.Errorf("group %d name has no NUL", i)
		}
		g.Name = string(data[p : p+e])
		out = append(out, g)
	}
	return out, nil
}
