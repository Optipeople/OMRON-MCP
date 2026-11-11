package sysmac

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// stLiteral is a parsed IEC 61131-3 initial value as Sysmac Studio stores it
// in the IV field of an SLWD variable, for example
//
//	(Name := 'x', Tags := [(A := 1), 23((A := 0))], Wait := T#10S)
//
// Scalars keep their source text verbatim so that formatting an unchanged
// literal reproduces the input.
type stLiteral struct {
	kind   byte // 's' scalar, 'r' struct (record), 'a' array
	scalar string
	fields []stField
	items  []stItem
}

type stField struct {
	name  string
	value *stLiteral
}

// stItem is an array element; count > 0 marks a repetition "count(value)".
type stItem struct {
	count int
	value *stLiteral
}

func stScalar(text string) *stLiteral { return &stLiteral{kind: 's', scalar: text} }

func parseSTLiteral(text string) (*stLiteral, error) {
	parser := &stLiteralParser{text: text}
	value, err := parser.value()
	if err != nil {
		return nil, err
	}
	parser.space()
	if parser.pos != len(parser.text) {
		return nil, fmt.Errorf("unexpected %q at offset %d", parser.text[parser.pos:], parser.pos)
	}
	return value, nil
}

type stLiteralParser struct {
	text string
	pos  int
}

func (p *stLiteralParser) space() {
	for p.pos < len(p.text) && strings.ContainsRune(" \t\r\n", rune(p.text[p.pos])) {
		p.pos++
	}
}

func (p *stLiteralParser) peek() byte {
	p.space()
	if p.pos >= len(p.text) {
		return 0
	}
	return p.text[p.pos]
}

func (p *stLiteralParser) expect(c byte) error {
	if p.peek() != c {
		return fmt.Errorf("expected %q at offset %d", c, p.pos)
	}
	p.pos++
	return nil
}

func (p *stLiteralParser) value() (*stLiteral, error) {
	switch p.peek() {
	case '(':
		return p.record()
	case '[':
		return p.array()
	case 0:
		return nil, errors.New("unexpected end of initial value")
	}
	return p.scalar()
}

func (p *stLiteralParser) record() (*stLiteral, error) {
	if err := p.expect('('); err != nil {
		return nil, err
	}
	result := &stLiteral{kind: 'r'}
	if p.peek() == ')' {
		p.pos++
		return result, nil
	}
	for {
		p.space()
		start := p.pos
		for p.pos < len(p.text) && isSTIdentChar(p.text[p.pos]) {
			p.pos++
		}
		name := p.text[start:p.pos]
		if name == "" {
			return nil, fmt.Errorf("expected member name at offset %d", p.pos)
		}
		p.space()
		if !strings.HasPrefix(p.text[p.pos:], ":=") {
			return nil, fmt.Errorf("expected := after %s", name)
		}
		p.pos += 2
		value, err := p.value()
		if err != nil {
			return nil, err
		}
		result.fields = append(result.fields, stField{name: name, value: value})
		switch p.peek() {
		case ',':
			p.pos++
		case ')':
			p.pos++
			return result, nil
		default:
			return nil, fmt.Errorf("expected , or ) at offset %d", p.pos)
		}
	}
}

func (p *stLiteralParser) array() (*stLiteral, error) {
	if err := p.expect('['); err != nil {
		return nil, err
	}
	result := &stLiteral{kind: 'a'}
	if p.peek() == ']' {
		p.pos++
		return result, nil
	}
	for {
		p.space()
		item := stItem{}
		// Repetition: digits immediately followed by '('.
		digits := p.pos
		for digits < len(p.text) && p.text[digits] >= '0' && p.text[digits] <= '9' {
			digits++
		}
		if digits > p.pos && digits < len(p.text) && p.text[digits] == '(' {
			count, _ := strconv.Atoi(p.text[p.pos:digits])
			p.pos = digits + 1
			value, err := p.value()
			if err != nil {
				return nil, err
			}
			if err := p.expect(')'); err != nil {
				return nil, err
			}
			item = stItem{count: count, value: value}
		} else {
			value, err := p.value()
			if err != nil {
				return nil, err
			}
			item.value = value
		}
		result.items = append(result.items, item)
		switch p.peek() {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return result, nil
		default:
			return nil, fmt.Errorf("expected , or ] at offset %d", p.pos)
		}
	}
}

func (p *stLiteralParser) scalar() (*stLiteral, error) {
	p.space()
	start := p.pos
	if p.text[p.pos] == '\'' || p.text[p.pos] == '"' {
		quote := p.text[p.pos]
		p.pos++
		for p.pos < len(p.text) {
			c := p.text[p.pos]
			if c == '$' {
				p.pos += 2
				continue
			}
			p.pos++
			if c == quote {
				return stScalar(p.text[start:p.pos]), nil
			}
		}
		return nil, errors.New("unterminated string literal")
	}
	for p.pos < len(p.text) && !strings.ContainsRune(",)]", rune(p.text[p.pos])) {
		p.pos++
	}
	text := strings.TrimRight(p.text[start:p.pos], " \t")
	if text == "" {
		return nil, fmt.Errorf("empty value at offset %d", start)
	}
	return stScalar(text), nil
}

func isSTIdentChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// format renders the literal the way Sysmac Studio writes initial values.
func (v *stLiteral) format() string {
	switch v.kind {
	case 'r':
		parts := make([]string, len(v.fields))
		for index, field := range v.fields {
			parts[index] = field.name + " := " + field.value.format()
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case 'a':
		parts := make([]string, len(v.items))
		for index, item := range v.items {
			if item.count > 0 {
				parts[index] = strconv.Itoa(item.count) + "(" + item.value.format() + ")"
			} else {
				parts[index] = item.value.format()
			}
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return v.scalar
}

// field returns the member value of a struct literal (case-insensitive).
func (v *stLiteral) field(name string) *stLiteral {
	if v == nil || v.kind != 'r' {
		return nil
	}
	for _, field := range v.fields {
		if strings.EqualFold(field.name, name) {
			return field.value
		}
	}
	return nil
}

// expand returns the array elements with repetitions unrolled.
func (v *stLiteral) expand() []*stLiteral {
	if v == nil || v.kind != 'a' {
		return nil
	}
	var result []*stLiteral
	for _, item := range v.items {
		count := item.count
		if count == 0 {
			count = 1
		}
		for index := 0; index < count; index++ {
			result = append(result, item.value)
		}
	}
	return result
}

// compressArray builds an array literal and folds runs of identical trailing
// elements into a repetition, as Sysmac Studio does.
func compressArray(values []*stLiteral) *stLiteral {
	result := &stLiteral{kind: 'a'}
	for index := 0; index < len(values); {
		run := index + 1
		for run < len(values) && values[run].format() == values[index].format() {
			run++
		}
		if run-index > 1 {
			result.items = append(result.items, stItem{count: run - index, value: values[index]})
		} else {
			result.items = append(result.items, stItem{value: values[index]})
		}
		index = run
	}
	return result
}

// stString unquotes a single quoted ST string literal.
func stString(value *stLiteral) string {
	if value == nil || value.kind != 's' {
		return ""
	}
	text := value.scalar
	if len(text) >= 2 && (text[0] == '\'' || text[0] == '"') {
		text = text[1 : len(text)-1]
	}
	return strings.NewReplacer("$'", "'", "$$", "$").Replace(text)
}

func stQuote(text string) string {
	return "'" + strings.NewReplacer("$", "$$", "'", "$'").Replace(text) + "'"
}

// stIsZero reports whether a scalar is a zero number or zero duration.
func stIsZero(value *stLiteral) bool {
	if value == nil {
		return true
	}
	if value.kind != 's' {
		return false
	}
	text := strings.ToUpper(strings.TrimSpace(value.scalar))
	if strings.HasPrefix(text, "TIME#") {
		text = "T#" + strings.TrimPrefix(text, "TIME#")
	}
	if strings.HasPrefix(text, "T#") {
		return !strings.ContainsAny(strings.TrimPrefix(text, "T#"), "123456789")
	}
	number, err := strconv.ParseFloat(text, 64)
	return err == nil && number == 0
}
