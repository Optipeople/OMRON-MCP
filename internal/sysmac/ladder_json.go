package sysmac

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Ladder POU bodies are stored by Sysmac Studio as one compact JSON object per
// rung and line (.NET DataContract style: no whitespace, "/" escaped as "\/",
// control characters as \u00XX, default values such as Ix=0 omitted). The
// ordered tree below keeps every token verbatim, so an unchanged rung renders
// byte-identically; only values that are replaced are re-encoded.

type ladderJSON struct {
	kind  byte   // 'o' object, 'a' array, 's' string, 'n' number/bool/null
	raw   string // raw token for scalars
	keys  []string
	vals  []*ladderJSON
	items []*ladderJSON
}

func parseLadderJSON(text string) (*ladderJSON, error) {
	parser := &ladderJSONParser{text: text}
	value, err := parser.value()
	if err != nil {
		return nil, err
	}
	if parser.pos != len(text) {
		return nil, fmt.Errorf("trailing data at offset %d", parser.pos)
	}
	return value, nil
}

type ladderJSONParser struct {
	text string
	pos  int
}

func (p *ladderJSONParser) value() (*ladderJSON, error) {
	if p.pos >= len(p.text) {
		return nil, errors.New("unexpected end of JSON")
	}
	switch c := p.text[p.pos]; {
	case c == '{':
		p.pos++
		node := &ladderJSON{kind: 'o'}
		if p.pos < len(p.text) && p.text[p.pos] == '}' {
			p.pos++
			return node, nil
		}
		for {
			key, err := p.str()
			if err != nil {
				return nil, err
			}
			if p.pos >= len(p.text) || p.text[p.pos] != ':' {
				return nil, fmt.Errorf("expected : at offset %d", p.pos)
			}
			p.pos++
			value, err := p.value()
			if err != nil {
				return nil, err
			}
			node.keys = append(node.keys, key)
			node.vals = append(node.vals, value)
			if p.pos >= len(p.text) {
				return nil, errors.New("unterminated object")
			}
			if p.text[p.pos] == ',' {
				p.pos++
				continue
			}
			if p.text[p.pos] == '}' {
				p.pos++
				return node, nil
			}
			return nil, fmt.Errorf("expected , or } at offset %d", p.pos)
		}
	case c == '[':
		p.pos++
		node := &ladderJSON{kind: 'a'}
		if p.pos < len(p.text) && p.text[p.pos] == ']' {
			p.pos++
			return node, nil
		}
		for {
			value, err := p.value()
			if err != nil {
				return nil, err
			}
			node.items = append(node.items, value)
			if p.pos >= len(p.text) {
				return nil, errors.New("unterminated array")
			}
			if p.text[p.pos] == ',' {
				p.pos++
				continue
			}
			if p.text[p.pos] == ']' {
				p.pos++
				return node, nil
			}
			return nil, fmt.Errorf("expected , or ] at offset %d", p.pos)
		}
	case c == '"':
		raw, err := p.str()
		if err != nil {
			return nil, err
		}
		return &ladderJSON{kind: 's', raw: raw}, nil
	default:
		start := p.pos
		for p.pos < len(p.text) && !strings.ContainsRune(",]}", rune(p.text[p.pos])) {
			p.pos++
		}
		if start == p.pos {
			return nil, fmt.Errorf("unexpected %q at offset %d", c, p.pos)
		}
		return &ladderJSON{kind: 'n', raw: p.text[start:p.pos]}, nil
	}
}

// str returns the raw string token including quotes.
func (p *ladderJSONParser) str() (string, error) {
	if p.pos >= len(p.text) || p.text[p.pos] != '"' {
		return "", fmt.Errorf("expected string at offset %d", p.pos)
	}
	start := p.pos
	p.pos++
	for p.pos < len(p.text) {
		switch p.text[p.pos] {
		case '\\':
			p.pos += 2
			continue
		case '"':
			p.pos++
			return p.text[start:p.pos], nil
		}
		p.pos++
	}
	return "", errors.New("unterminated string")
}

func (n *ladderJSON) render() string {
	var builder strings.Builder
	n.renderTo(&builder)
	return builder.String()
}

func (n *ladderJSON) renderTo(builder *strings.Builder) {
	switch n.kind {
	case 'o':
		builder.WriteByte('{')
		for index, key := range n.keys {
			if index > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(key)
			builder.WriteByte(':')
			n.vals[index].renderTo(builder)
		}
		builder.WriteByte('}')
	case 'a':
		builder.WriteByte('[')
		for index, item := range n.items {
			if index > 0 {
				builder.WriteByte(',')
			}
			item.renderTo(builder)
		}
		builder.WriteByte(']')
	default:
		builder.WriteString(n.raw)
	}
}

func encodeLadderString(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		switch {
		case r == '"':
			builder.WriteString(`\"`)
		case r == '\\':
			builder.WriteString(`\\`)
		case r == '/':
			builder.WriteString(`\/`)
		case r < 0x20:
			builder.WriteString(fmt.Sprintf(`\u%04x`, r))
		default:
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

func ladderString(value string) *ladderJSON {
	return &ladderJSON{kind: 's', raw: encodeLadderString(value)}
}

func ladderNumber(value int) *ladderJSON {
	return &ladderJSON{kind: 'n', raw: strconv.Itoa(value)}
}

func ladderBool(value bool) *ladderJSON {
	return &ladderJSON{kind: 'n', raw: strconv.FormatBool(value)}
}

// get returns the value of key in an object, nil when absent.
func (n *ladderJSON) get(key string) *ladderJSON {
	if n == nil || n.kind != 'o' {
		return nil
	}
	quoted := `"` + key + `"`
	for index, candidate := range n.keys {
		if candidate == quoted {
			return n.vals[index]
		}
	}
	return nil
}

func (n *ladderJSON) str(key string) string {
	value := n.get(key)
	if value == nil || value.kind != 's' {
		return ""
	}
	var decoded string
	if err := json.Unmarshal([]byte(value.raw), &decoded); err != nil {
		return ""
	}
	return decoded
}

func (n *ladderJSON) num(key string) (int, bool) {
	value := n.get(key)
	if value == nil || value.kind != 'n' {
		return 0, false
	}
	number, err := strconv.Atoi(value.raw)
	return number, err == nil
}

// set replaces or inserts key. New keys are inserted before the first key in
// after (to follow the serializer's member order) or appended.
func (n *ladderJSON) set(key string, value *ladderJSON, before ...string) {
	quoted := `"` + key + `"`
	for index, candidate := range n.keys {
		if candidate == quoted {
			n.vals[index] = value
			return
		}
	}
	position := len(n.keys)
	for index, candidate := range n.keys {
		for _, name := range before {
			if candidate == `"`+name+`"` && index < position {
				position = index
			}
		}
	}
	n.keys = append(n.keys, "")
	n.vals = append(n.vals, nil)
	copy(n.keys[position+1:], n.keys[position:])
	copy(n.vals[position+1:], n.vals[position:])
	n.keys[position] = quoted
	n.vals[position] = value
}

func (n *ladderJSON) remove(key string) {
	quoted := `"` + key + `"`
	for index, candidate := range n.keys {
		if candidate == quoted {
			n.keys = append(n.keys[:index], n.keys[index+1:]...)
			n.vals = append(n.vals[:index], n.vals[index+1:]...)
			return
		}
	}
}

// walk calls fn for every object in the tree (depth first, parents first).
func (n *ladderJSON) walk(fn func(object *ladderJSON)) {
	switch n.kind {
	case 'o':
		fn(n)
		for _, value := range n.vals {
			value.walk(fn)
		}
	case 'a':
		for _, item := range n.items {
			item.walk(fn)
		}
	}
}

// ladderLine is one rung (line) of a ladder body payload.
type ladderLine struct {
	raw  string
	eol  string
	node *ladderJSON // nil for blank lines
}

type ladderBody struct {
	bom   string
	lines []*ladderLine
}

func parseLadderBody(data []byte) (*ladderBody, error) {
	text := string(data)
	body := &ladderBody{}
	if strings.HasPrefix(text, utf8BOM) {
		body.bom = utf8BOM
		text = text[len(utf8BOM):]
	}
	for len(text) > 0 {
		line := &ladderLine{}
		index := strings.IndexAny(text, "\r\n")
		if index < 0 {
			line.raw, text = text, ""
		} else {
			line.raw = text[:index]
			if strings.HasPrefix(text[index:], "\r\n") {
				line.eol = "\r\n"
			} else {
				line.eol = text[index : index+1]
			}
			text = text[index+len(line.eol):]
		}
		if strings.TrimSpace(line.raw) != "" {
			node, err := parseLadderJSON(line.raw)
			if err != nil {
				return nil, fmt.Errorf("rung %d: %w", len(body.lines), err)
			}
			if node.render() != line.raw {
				return nil, fmt.Errorf("rung %d does not round-trip", len(body.lines))
			}
			line.node = node
		}
		body.lines = append(body.lines, line)
	}
	return body, nil
}

// rungs returns the JSON lines in order (the rung index used in link info).
func (b *ladderBody) rungs() []*ladderLine {
	var result []*ladderLine
	for _, line := range b.lines {
		if line.node != nil {
			result = append(result, line)
		}
	}
	return result
}

func (b *ladderBody) marshal() []byte {
	var builder strings.Builder
	builder.WriteString(b.bom)
	for _, line := range b.lines {
		if line.node != nil {
			builder.WriteString(line.node.render())
		} else {
			builder.WriteString(line.raw)
		}
		builder.WriteString(line.eol)
	}
	return []byte(builder.String())
}
