package msbt

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrUnexpectedToken is returned when a review string contains a <#...>
// sequence that is not a valid reversible token.
var ErrUnexpectedToken = errors.New("msbt: unexpected control token")

// TokenStats describes the control sequences seen while encoding a string.
type TokenStats struct {
	Tags         int
	TagEnds      int
	Controls     int
	MalformedTag int
}

// EncodeText converts raw UTF-8 text (which may contain binary control
// sequences) into a reversible, human-reviewable token form. Control tags use
// the documented LMS layout:
//
//	0E <u16 group> <u16 index> <u16 byteLen> <bytes>
//
// and closing tags use 0F <u16 group> <u16 index>. A truncated or otherwise
// unparseable control sequence is represented byte-by-byte as <#CTRL:XX> and
// counted in MalformedTag; no byte is lost.
func EncodeText(raw []byte) (string, TokenStats, error) {
	if !utf8.Valid(raw) {
		return "", TokenStats{}, errors.New("msbt: text is not valid UTF-8")
	}
	var b strings.Builder
	var st TokenStats
	for i := 0; i < len(raw); {
		c := raw[i]
		switch c {
		case 0x0E:
			if i+6 <= len(raw) {
				group := binary.LittleEndian.Uint16(raw[i+1 : i+3])
				index := binary.LittleEndian.Uint16(raw[i+3 : i+5])
				n := int(binary.LittleEndian.Uint16(raw[i+5 : i+7]))
				if i+7+n <= len(raw) {
					params := raw[i+7 : i+7+n]
					fmt.Fprintf(&b, "<#TAG:%04X:%04X:%04X:%s>", group, index, n, strings.ToUpper(hex.EncodeToString(params)))
					st.Tags++
					i += 7 + n
					continue
				}
			}
			// Truncated/incomplete tag: preserve the introducer as a single
			// control byte and let the following bytes be encoded normally.
			st.MalformedTag++
			fmt.Fprintf(&b, "<#CTRL:%02X>", c)
			i++
		case 0x0F:
			if i+5 <= len(raw) {
				group := binary.LittleEndian.Uint16(raw[i+1 : i+3])
				index := binary.LittleEndian.Uint16(raw[i+3 : i+5])
				fmt.Fprintf(&b, "<#TAGEND:%04X:%04X>", group, index)
				st.TagEnds++
				i += 5
				continue
			}
			st.MalformedTag++
			fmt.Fprintf(&b, "<#CTRL:%02X>", c)
			i++
		case '\t', '\n', '\r':
			b.WriteByte(c)
			i++
		case '<':
			if i+1 < len(raw) && raw[i+1] == '#' {
				b.WriteString("<#LIT:3C>")
				i++
				continue
			}
			b.WriteByte(c)
			i++
		default:
			if c < 0x20 {
				st.Controls++
				fmt.Fprintf(&b, "<#CTRL:%02X>", c)
				i++
				continue
			}
			// Copy one complete UTF-8 rune (or the byte if the string was
			// already validated, which it has been).
			_, size := utf8.DecodeRune(raw[i:])
			if size <= 0 {
				size = 1
			}
			b.Write(raw[i : i+size])
			i += size
		}
	}
	return b.String(), st, nil
}

// DecodeText reverses EncodeText. Unknown or malformed <#...> tokens are
// rejected instead of being silently ignored.
func DecodeText(s string) ([]byte, error) {
	var out []byte
	for i := 0; i < len(s); {
		if s[i] != '<' || i+1 >= len(s) || s[i+1] != '#' {
			// Copy one UTF-8 rune.
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				return nil, fmt.Errorf("%w: invalid UTF-8 at byte %d", ErrUnexpectedToken, i)
			}
			out = append(out, s[i:i+size]...)
			i += size
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			return nil, fmt.Errorf("%w: unterminated token at byte %d", ErrUnexpectedToken, i)
		}
		tok := s[i+2 : i+end]
		i += end + 1
		switch {
		case strings.HasPrefix(tok, "TAG:"):
			parts := strings.Split(tok[4:], ":")
			if len(parts) != 4 {
				return nil, fmt.Errorf("%w: TAG needs 4 fields", ErrUnexpectedToken)
			}
			group, err := parseHex16(parts[0])
			if err != nil {
				return nil, fmt.Errorf("%w: TAG group: %v", ErrUnexpectedToken, err)
			}
			index, err := parseHex16(parts[1])
			if err != nil {
				return nil, fmt.Errorf("%w: TAG index: %v", ErrUnexpectedToken, err)
			}
			n, err := parseHex16(parts[2])
			if err != nil {
				return nil, fmt.Errorf("%w: TAG size: %v", ErrUnexpectedToken, err)
			}
			params, err := hex.DecodeString(parts[3])
			if err != nil {
				return nil, fmt.Errorf("%w: TAG params: %v", ErrUnexpectedToken, err)
			}
			if len(params) != int(n) {
				return nil, fmt.Errorf("%w: TAG declares %d params, has %d", ErrUnexpectedToken, n, len(params))
			}
			out = append(out, 0x0E)
			out = binary.LittleEndian.AppendUint16(out, group)
			out = binary.LittleEndian.AppendUint16(out, index)
			out = binary.LittleEndian.AppendUint16(out, n)
			out = append(out, params...)
		case strings.HasPrefix(tok, "TAGEND:"):
			parts := strings.Split(tok[7:], ":")
			if len(parts) != 2 {
				return nil, fmt.Errorf("%w: TAGEND needs 2 fields", ErrUnexpectedToken)
			}
			group, err := parseHex16(parts[0])
			if err != nil {
				return nil, fmt.Errorf("%w: TAGEND group: %v", ErrUnexpectedToken, err)
			}
			index, err := parseHex16(parts[1])
			if err != nil {
				return nil, fmt.Errorf("%w: TAGEND index: %v", ErrUnexpectedToken, err)
			}
			out = append(out, 0x0F)
			out = binary.LittleEndian.AppendUint16(out, group)
			out = binary.LittleEndian.AppendUint16(out, index)
		case strings.HasPrefix(tok, "CTRL:"):
			b, err := parseHexByte(tok[5:])
			if err != nil {
				return nil, fmt.Errorf("%w: CTRL: %v", ErrUnexpectedToken, err)
			}
			out = append(out, b)
		case strings.HasPrefix(tok, "LIT:"):
			b, err := parseHexByte(tok[4:])
			if err != nil {
				return nil, fmt.Errorf("%w: LIT: %v", ErrUnexpectedToken, err)
			}
			out = append(out, b)
		default:
			return nil, fmt.Errorf("%w: unknown token %q", ErrUnexpectedToken, tok)
		}
	}
	return out, nil
}

func parseHex16(s string) (uint16, error) {
	if len(s) != 4 {
		return 0, errors.New("want 4 hex digits")
	}
	v, err := strconv.ParseUint(s, 16, 16)
	return uint16(v), err
}

func parseHexByte(s string) (byte, error) {
	if len(s) != 2 {
		return 0, errors.New("want 2 hex digits")
	}
	v, err := strconv.ParseUint(s, 16, 8)
	return byte(v), err
}
