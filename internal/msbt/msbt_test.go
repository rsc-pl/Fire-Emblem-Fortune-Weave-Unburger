package msbt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"fefw/internal/zstd"
)

func sec(magic string, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString(magic)
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(data)))
	b.Write(sz[:])
	b.Write(make([]byte, 8))
	b.Write(data)
	for b.Len()%Align != 0 {
		b.WriteByte(0xAB)
	}
	return b.Bytes()
}

func lbl1Data(labels []string, indices []uint32) []byte {
	var b bytes.Buffer
	var u [4]byte
	binary.LittleEndian.PutUint32(u[:], 1)
	b.Write(u[:])
	binary.LittleEndian.PutUint32(u[:], uint32(len(labels)))
	b.Write(u[:])
	binary.LittleEndian.PutUint32(u[:], 12) // after count + one bucket
	b.Write(u[:])
	for i, l := range labels {
		b.WriteByte(byte(len(l)))
		b.WriteString(l)
		binary.LittleEndian.PutUint32(u[:], indices[i])
		b.Write(u[:])
	}
	return b.Bytes()
}

func txt2Data(texts []string) []byte {
	var b bytes.Buffer
	var u [4]byte
	binary.LittleEndian.PutUint32(u[:], uint32(len(texts)))
	b.Write(u[:])
	off := 4 + 4*len(texts)
	for _, s := range texts {
		binary.LittleEndian.PutUint32(u[:], uint32(off))
		b.Write(u[:])
		off += len(s) + 1
	}
	for _, s := range texts {
		b.WriteString(s)
		b.WriteByte(0)
	}
	return b.Bytes()
}

func atr1Data(n int) []byte {
	var b bytes.Buffer
	var u [4]byte
	binary.LittleEndian.PutUint32(u[:], uint32(n))
	b.Write(u[:])
	binary.LittleEndian.PutUint32(u[:], 4)
	b.Write(u[:])
	b.Write(make([]byte, n*4))
	return b.Bytes()
}

func ato1Data() []byte {
	var u [4]byte
	binary.LittleEndian.PutUint32(u[:], 0)
	return u[:]
}

func tsy1Data(n int) []byte { return make([]byte, n*4) }

func buildMSBT(texts, labels []string, indices []uint32) []byte {
	var b bytes.Buffer
	b.WriteString(MagicMSBT)
	b.Write([]byte{0xFF, 0xFE, 0x00, 0x00, 0x00, 0x03})
	var u [4]byte
	binary.LittleEndian.PutUint16(u[:2], 5)
	b.Write(u[:2])
	binary.LittleEndian.PutUint16(u[:2], 0)
	b.Write(u[:2])
	b.Write(make([]byte, 4)) // file size placeholder
	b.Write(make([]byte, 10))
	b.Write(sec("LBL1", lbl1Data(labels, indices)))
	b.Write(sec("ATO1", ato1Data()))
	b.Write(sec("ATR1", atr1Data(len(texts))))
	b.Write(sec("TSY1", tsy1Data(len(texts))))
	b.Write(sec("TXT2", txt2Data(texts)))
	out := b.Bytes()
	binary.LittleEndian.PutUint32(out[18:22], uint32(len(out)))
	return out
}

func TestRoundtripSynthetic(t *testing.T) {
	raw := buildMSBT([]string{"hello", "世界", ""}, []string{"A", "B", "C"}, []uint32{0, 1, 2})
	f, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc, raw) {
		t.Fatalf("no-op roundtrip differs: %d vs %d bytes", len(enc), len(raw))
	}
}

func TestReplaceTextPreservesOtherSections(t *testing.T) {
	raw := buildMSBT([]string{"hello", "world"}, []string{"A", "B"}, []uint32{0, 1})
	f, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	oldATR := append([]byte(nil), f.Section("ATR1").Data...)
	oldLBL := append([]byte(nil), f.Section("LBL1").Data...)
	if err := f.ReplaceText(1, []byte("changed")); err != nil {
		t.Fatal(err)
	}
	enc, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	re, err := Parse(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got := re.TXT2.Strings[1]; string(got) != "changed" {
		t.Fatalf("text not replaced: %q", got)
	}
	if !bytes.Equal(re.Section("ATR1").Data, oldATR) {
		t.Fatal("ATR1 changed")
	}
	if !bytes.Equal(re.Section("LBL1").Data, oldLBL) {
		t.Fatal("LBL1 changed")
	}
}

func TestBadSectionSize(t *testing.T) {
	raw := buildMSBT([]string{"x"}, []string{"A"}, []uint32{0})
	copyRaw := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint32(copyRaw[0x24:0x28], 0xFFFFFF)
	if _, err := Parse(copyRaw); err == nil {
		t.Fatal("expected malformed section size error")
	}
}

func TestLBL1Collision(t *testing.T) {
	raw := buildMSBT([]string{"x"}, []string{"A", "B"}, []uint32{0, 0})
	f, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(f.LBL1.Collisions()); got != 1 {
		t.Fatalf("collisions = %d, want 1", got)
	}
	label, n := f.LBL1.CanonicalLabel(0)
	if n != 2 || label != "A" {
		t.Fatalf("canonical label = %q/%d, want A/2", label, n)
	}
}

func TestTokenCodec(t *testing.T) {
	raw := append([]byte("abc<#literal"), 0x0E, 0x01, 0x00, 0x02, 0x00, 0x03, 0x00, 0x41, 0x42, 0x43, 0x0F, 0x01, 0x00, 0x02, 0x00, 0x01)
	enc, st, err := EncodeText(raw)
	if err != nil {
		t.Fatal(err)
	}
	if st.Tags != 1 || st.TagEnds != 1 || st.Controls != 1 {
		t.Fatalf("stats = %+v", st)
	}
	dec, err := DecodeText(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec, raw) {
		t.Fatalf("token roundtrip mismatch\nraw=%x\ndec=%x\nenc=%s", raw, dec, enc)
	}
	if !strings.Contains(enc, "<#LIT:3C>") {
		t.Fatalf("literal <# not escaped: %s", enc)
	}
}

func TestTruncatedTagAndUnexpectedToken(t *testing.T) {
	raw := append([]byte("x"), 0x0E)
	enc, st, err := EncodeText(raw)
	if err != nil {
		t.Fatal(err)
	}
	if st.MalformedTag != 1 {
		t.Fatalf("malformed = %d, want 1", st.MalformedTag)
	}
	dec, err := DecodeText(enc)
	if err != nil || !bytes.Equal(dec, raw) {
		t.Fatalf("truncated tag roundtrip failed: %v", err)
	}
	if _, err := DecodeText("<#TAG:0000>"); !errors.Is(err, ErrUnexpectedToken) {
		t.Fatalf("expected ErrUnexpectedToken, got %v", err)
	}
}

func TestDecompressCorruptZstd(t *testing.T) {
	r := zstd.NewReader(bytes.NewReader([]byte("not a zstd stream")))
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("expected corrupt zstd error")
	}
}

func TestATO1UnknownOffsetRejected(t *testing.T) {
	schema := &AttributeSchema{Defs: []AttributeDef{{Type: 8, Offset: 12}}}
	ato := &ATO1{Offsets: []uint32{99}}
	atr := &ATR1{Count: 1, BytesPerAttribute: 16, Attributes: make([]byte, 16)}
	if _, err := atr.ExtractAttributes(ato, schema); err == nil {
		t.Fatal("expected ATO1 offset outside ATI2 to be rejected")
	}
}

func TestATR1StringOffsetOutOfRange(t *testing.T) {
	schema := &AttributeSchema{Defs: []AttributeDef{{Type: 8, Offset: 12}}}
	atoBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(atoBytes, 12)
	ato, _ := parseATO1(atoBytes)
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[12:16], 0x7FFFFFFF)
	atr := &ATR1{Count: 1, BytesPerAttribute: 16, Attributes: b}
	if _, err := atr.ExtractAttributes(ato, schema); err == nil {
		t.Fatal("expected out-of-range string offset to be rejected")
	}
}
