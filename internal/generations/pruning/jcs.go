// Package pruning replaces each conversation input that a later input of
// the same conversation repeats with a residual that rebuilds it, and
// rebuilds residuals into the original request.
package pruning

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrNotCanonical marks a body that RFC 8785 canonicalization would alter
// or refuse: malformed JSON, a duplicate key, invalid Unicode, or a number
// outside I-JSON or one whose canonical form changes its value.
var ErrNotCanonical = errors.New("body is outside the RFC 8785 domain")

// Value kinds.
const (
	kindNull = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

// Value is a parsed JSON value that keeps each number's lexeme and each
// object's members in their original order.
type Value struct {
	kind    int
	boolean bool
	text    string // a string's decoded text or a number's lexeme
	items   []*Value
	members []Member
}

// Member is one object property.
type Member struct {
	Key   string
	Value *Value
}

// Limits on numbers, so a lexeme cannot make exact comparison expensive.
const (
	maxNumberLength   = 400
	maxNumberExponent = 400
	maxDepth          = 512
)

// Parse reads one JSON document under RFC 8785's input rules: no duplicate
// property names, valid UTF-8 without lone surrogates, and numbers that are
// finite IEEE 754 doubles whose canonical serialization keeps their value.
// It then requires that canonicalizing, parsing, and canonicalizing again
// yields the same bytes.
func Parse(data []byte) (*Value, error) {
	v, err := parseDocument(data)
	if err != nil {
		return nil, err
	}
	canonical := v.Canonical()
	again, err := parseDocument(canonical)
	if err != nil || !bytes.Equal(again.Canonical(), canonical) {
		return nil, fmt.Errorf("%w: canonical form does not round-trip", ErrNotCanonical)
	}
	return v, nil
}

func parseDocument(data []byte) (*Value, error) {
	p := &parser{data: data}
	p.space()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.data) {
		return nil, p.fail("trailing data")
	}
	return v, nil
}

type parser struct {
	data []byte
	pos  int
}

func (p *parser) fail(what string) error {
	return fmt.Errorf("%w: %s at byte %d", ErrNotCanonical, what, p.pos)
}

func (p *parser) space() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) literal(word string) bool {
	if bytes.HasPrefix(p.data[p.pos:], []byte(word)) {
		p.pos += len(word)
		return true
	}
	return false
}

func (p *parser) value(depth int) (*Value, error) {
	if depth > maxDepth {
		return nil, p.fail("nesting too deep")
	}
	if p.pos >= len(p.data) {
		return nil, p.fail("unexpected end")
	}
	switch c := p.data[p.pos]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"':
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		return &Value{kind: kindString, text: s}, nil
	case c == 't' && p.literal("true"):
		return &Value{kind: kindBool, boolean: true}, nil
	case c == 'f' && p.literal("false"):
		return &Value{kind: kindBool}, nil
	case c == 'n' && p.literal("null"):
		return &Value{kind: kindNull}, nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	return nil, p.fail("unexpected character")
}

func (p *parser) object(depth int) (*Value, error) {
	p.pos++
	v := &Value{kind: kindObject}
	seen := map[string]bool{}
	p.space()
	if p.pos < len(p.data) && p.data[p.pos] == '}' {
		p.pos++
		return v, nil
	}
	for {
		p.space()
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			return nil, p.fail("expected a property name")
		}
		key, err := p.str()
		if err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, p.fail("duplicate property name")
		}
		seen[key] = true
		p.space()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			return nil, p.fail("expected ':'")
		}
		p.pos++
		p.space()
		member, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		v.members = append(v.members, Member{Key: key, Value: member})
		p.space()
		if p.pos >= len(p.data) {
			return nil, p.fail("unexpected end")
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return v, nil
		default:
			return nil, p.fail("expected ',' or '}'")
		}
	}
}

func (p *parser) array(depth int) (*Value, error) {
	p.pos++
	v := &Value{kind: kindArray, items: []*Value{}}
	p.space()
	if p.pos < len(p.data) && p.data[p.pos] == ']' {
		p.pos++
		return v, nil
	}
	for {
		p.space()
		item, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		v.items = append(v.items, item)
		p.space()
		if p.pos >= len(p.data) {
			return nil, p.fail("unexpected end")
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return v, nil
		default:
			return nil, p.fail("expected ',' or ']'")
		}
	}
}

func hex4(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	n, err := strconv.ParseUint(string(b[:4]), 16, 16)
	return rune(n), err == nil
}

// str reads a string, refusing invalid UTF-8, raw control characters, and
// lone surrogates.
func (p *parser) str() (string, error) {
	p.pos++
	var b strings.Builder
	for {
		if p.pos >= len(p.data) {
			return "", p.fail("unterminated string")
		}
		c := p.data[p.pos]
		switch {
		case c == '"':
			p.pos++
			return b.String(), nil
		case c < 0x20:
			return "", p.fail("control character in string")
		case c == '\\':
			if p.pos+1 >= len(p.data) {
				return "", p.fail("unterminated escape")
			}
			esc := p.data[p.pos+1]
			p.pos += 2
			switch esc {
			case '"', '\\', '/':
				b.WriteByte(esc)
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				r, ok := hex4(p.data[p.pos:])
				if !ok {
					return "", p.fail("invalid \\u escape")
				}
				p.pos += 4
				if utf16.IsSurrogate(r) {
					if r >= 0xdc00 || p.pos+6 > len(p.data) || p.data[p.pos] != '\\' || p.data[p.pos+1] != 'u' {
						return "", p.fail("lone surrogate")
					}
					low, okLow := hex4(p.data[p.pos+2:])
					if !okLow || low < 0xdc00 || low > 0xdfff {
						return "", p.fail("lone surrogate")
					}
					p.pos += 6
					r = utf16.DecodeRune(r, low)
				}
				b.WriteRune(r)
			default:
				return "", p.fail("invalid escape")
			}
		default:
			r, size := utf8.DecodeRune(p.data[p.pos:])
			if r == utf8.RuneError && size <= 1 {
				return "", p.fail("invalid UTF-8")
			}
			b.Write(p.data[p.pos : p.pos+size])
			p.pos += size
		}
	}
}

func digits(b []byte, i int) int {
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	return i
}

// number reads a number and accepts it only when it is a finite double
// whose canonical serialization has the lexeme's exact value.
func (p *parser) number() (*Value, error) {
	start, i := p.pos, p.pos
	if p.data[i] == '-' {
		i++
	}
	switch {
	case i < len(p.data) && p.data[i] == '0':
		i++
	case i < len(p.data) && p.data[i] >= '1' && p.data[i] <= '9':
		i = digits(p.data, i)
	default:
		return nil, p.fail("invalid number")
	}
	if i < len(p.data) && p.data[i] == '.' {
		j := digits(p.data, i+1)
		if j == i+1 {
			return nil, p.fail("invalid number")
		}
		i = j
	}
	if i < len(p.data) && (p.data[i] == 'e' || p.data[i] == 'E') {
		i++
		if i < len(p.data) && (p.data[i] == '+' || p.data[i] == '-') {
			i++
		}
		j := digits(p.data, i)
		if j == i {
			return nil, p.fail("invalid number")
		}
		exponent, err := strconv.Atoi(string(p.data[i:j]))
		if err != nil || exponent > maxNumberExponent {
			return nil, p.fail("number outside I-JSON")
		}
		i = j
	}
	lexeme := string(p.data[start:i])
	p.pos = i
	if len(lexeme) > maxNumberLength {
		return nil, p.fail("number outside I-JSON")
	}
	f, err := strconv.ParseFloat(lexeme, 64)
	if err != nil || math.IsInf(f, 0) {
		return nil, p.fail("number outside I-JSON")
	}
	exact, okExact := new(big.Rat).SetString(lexeme)
	canonical, okCanonical := new(big.Rat).SetString(formatNumber(f))
	if !okExact || !okCanonical || exact.Cmp(canonical) != 0 {
		return nil, p.fail("number whose canonical form changes its value")
	}
	return &Value{kind: kindNumber, text: lexeme}, nil
}

// formatNumber serializes a finite double as ECMAScript's Number toString,
// which RFC 8785 adopts.
func formatNumber(f float64) string {
	if f == 0 {
		return "0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// Shortest round-trip digits as d.ddde±x.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa, exponentText, _ := strings.Cut(e, "e")
	digitText := strings.Replace(mantissa, ".", "", 1)
	exponent, _ := strconv.Atoi(exponentText)
	k, n := len(digitText), exponent+1
	switch {
	case k <= n && n <= 21:
		return sign + digitText + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digitText[:n] + "." + digitText[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digitText
	}
	expSign := "+"
	if n-1 < 0 {
		expSign = "-"
	}
	exp := strconv.Itoa(abs(n - 1))
	if k == 1 {
		return sign + digitText + "e" + expSign + exp
	}
	return sign + digitText[:1] + "." + digitText[1:] + "e" + expSign + exp
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Canonical returns the value's RFC 8785 serialization.
func (v *Value) Canonical() []byte {
	var b bytes.Buffer
	v.write(&b)
	return b.Bytes()
}

func (v *Value) write(b *bytes.Buffer) {
	switch v.kind {
	case kindNull:
		b.WriteString("null")
	case kindBool:
		b.WriteString(strconv.FormatBool(v.boolean))
	case kindNumber:
		f, _ := strconv.ParseFloat(v.text, 64)
		b.WriteString(formatNumber(f))
	case kindString:
		writeString(b, v.text)
	case kindArray:
		b.WriteByte('[')
		for i, item := range v.items {
			if i > 0 {
				b.WriteByte(',')
			}
			item.write(b)
		}
		b.WriteByte(']')
	case kindObject:
		members := slices.Clone(v.members)
		slices.SortFunc(members, func(a, c Member) int { return compareUTF16(a.Key, c.Key) })
		b.WriteByte('{')
		for i, m := range members {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, m.Key)
			b.WriteByte(':')
			m.Value.write(b)
		}
		b.WriteByte('}')
	}
}

// compareUTF16 orders strings by their UTF-16 code units.
func compareUTF16(a, c string) int {
	return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(c)))
}

func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}
