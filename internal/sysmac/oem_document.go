package sysmac

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The native .oem index is written by Sysmac Studio as UTF-8 with BOM, CRLF
// line endings, two space indentation and one element per line:
//
//	<sysmac schemaVersion="1">
//	  <Entity type="Solution" ... trackingId="...">
//	    <AccessInfos />
//	    <ChildEntities>
//	      <Entity ...>
//
// Mutations are performed as text splices on the original bytes so that
// everything outside the touched entity stays byte-identical. The XML parser
// is only used to locate element boundaries.

// oemElementSpan records byte offsets of an element in the document.
type oemElementSpan struct {
	start       int // offset of '<'
	startTagEnd int // offset just after the start tag ('>' or '/>')
	closeStart  int // offset of the end tag '</', equals startTagEnd for self-closing elements
	end         int // offset just after the element
	selfClosing bool
}

type oemTextEntity struct {
	id            string
	span          oemElementSpan
	childEntities *oemElementSpan
	parent        *oemTextEntity
	children      []*oemTextEntity
	attrs         []xml.Attr
}

type oemTextDocument struct {
	data     []byte
	root     *oemTextEntity
	entities map[string]*oemTextEntity
	order    []*oemTextEntity
	eol      string
}

func parseOEMText(data []byte) (*oemTextDocument, error) {
	doc := &oemTextDocument{data: data, entities: map[string]*oemTextEntity{}, eol: "\n"}
	if bytes.Contains(data, []byte("\r\n")) {
		doc.eol = "\r\n"
	}
	offset := 0
	if bytes.HasPrefix(data, []byte(utf8BOM)) {
		offset = len(utf8BOM)
	}
	decoder := xml.NewDecoder(bytes.NewReader(data[offset:]))
	type frame struct {
		name   string
		span   oemElementSpan
		entity *oemTextEntity
	}
	var stack []*frame
	sawRoot := false
	for {
		tokenStart := int(decoder.InputOffset()) + offset
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse OEM document: %w", err)
		}
		tokenEnd := int(decoder.InputOffset()) + offset
		switch value := token.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				if sawRoot {
					return nil, errors.New("OEM document has more than one root element")
				}
				sawRoot = true
				if value.Name.Local != "sysmac" {
					return nil, fmt.Errorf("OEM document root is <%s>, expected <sysmac>", value.Name.Local)
				}
			}
			current := &frame{name: value.Name.Local, span: oemElementSpan{start: tokenStart, startTagEnd: tokenEnd}}
			current.span.selfClosing = bytes.HasSuffix(bytes.TrimRight(data[tokenStart:tokenEnd], " \t\r\n"), []byte("/>"))
			if value.Name.Local == "Entity" {
				entity := &oemTextEntity{attrs: append([]xml.Attr(nil), value.Attr...)}
				for _, attribute := range value.Attr {
					if attribute.Name.Local == "id" {
						entity.id = attribute.Value
					}
				}
				// The parent entity is the nearest Entity frame, reached via its
				// ChildEntities element.
				for index := len(stack) - 1; index >= 0; index-- {
					if stack[index].entity != nil {
						entity.parent = stack[index].entity
						entity.parent.children = append(entity.parent.children, entity)
						break
					}
				}
				if entity.parent == nil {
					if doc.root != nil {
						return nil, errors.New("OEM document has more than one solution entity")
					}
					doc.root = entity
				}
				if entity.id != "" {
					if _, exists := doc.entities[entity.id]; exists {
						return nil, fmt.Errorf("duplicate OEM entity id %q", entity.id)
					}
					doc.entities[entity.id] = entity
				}
				doc.order = append(doc.order, entity)
				current.entity = entity
			}
			stack = append(stack, current)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("invalid OEM XML nesting")
			}
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if current.span.selfClosing {
				current.span.closeStart = current.span.startTagEnd
				current.span.end = current.span.startTagEnd
			} else {
				current.span.closeStart = tokenStart
				current.span.end = tokenEnd
			}
			if current.entity != nil {
				current.entity.span = current.span
			}
			if current.name == "ChildEntities" && len(stack) > 0 && stack[len(stack)-1].entity != nil {
				span := current.span
				stack[len(stack)-1].entity.childEntities = &span
			}
		}
	}
	if doc.root == nil {
		return nil, errors.New("OEM document does not contain an entity")
	}
	return doc, nil
}

func (doc *oemTextDocument) attr(entity *oemTextEntity, name string) string {
	for _, attribute := range entity.attrs {
		if attribute.Name.Local == name {
			return attribute.Value
		}
	}
	return ""
}

// lineStart returns the offset of the first byte of the line containing pos
// when only spaces or tabs precede pos on that line; otherwise pos itself.
func lineStart(data []byte, pos int) int {
	index := pos
	for index > 0 && (data[index-1] == ' ' || data[index-1] == '\t') {
		index--
	}
	if index == 0 || data[index-1] == '\n' {
		return index
	}
	return pos
}

// lineEnd returns the offset after the line terminator following pos when
// only spaces or tabs follow pos on that line; otherwise pos itself.
func lineEnd(data []byte, pos int) int {
	index := pos
	for index < len(data) && (data[index] == ' ' || data[index] == '\t') {
		index++
	}
	if index < len(data) && data[index] == '\r' {
		index++
	}
	if index < len(data) && data[index] == '\n' {
		return index + 1
	}
	if index == len(data) {
		return index
	}
	return pos
}

func indentOf(data []byte, pos int) string {
	start := lineStart(data, pos)
	return string(data[start:pos])
}

// isPretty reports whether the element at span occupies whole lines.
func isPretty(data []byte, span oemElementSpan) bool {
	return lineStart(data, span.start) != span.start || (span.start == 0 || data[span.start-1] == '\n')
}

// entityBlock returns the text of an entity including its indentation and
// trailing line terminator when the entity occupies whole lines.
func (doc *oemTextDocument) entityBlock(entity *oemTextEntity) (int, int) {
	start := lineStart(doc.data, entity.span.start)
	end := entity.span.end
	if start != entity.span.start {
		if candidate := lineEnd(doc.data, end); candidate != end {
			end = candidate
		} else {
			start = entity.span.start
		}
	}
	return start, end
}

func splice(data []byte, start, end int, replacement []byte) []byte {
	result := make([]byte, 0, len(data)-(end-start)+len(replacement))
	result = append(result, data[:start]...)
	result = append(result, replacement...)
	result = append(result, data[end:]...)
	return result
}

// reindentBlock shifts every line of block by delta spaces. Lines are only
// stripped of leading spaces, never of content.
func reindentBlock(block string, delta int) string {
	if delta == 0 {
		return block
	}
	lines := strings.SplitAfter(block, "\n")
	for index, line := range lines {
		if line == "" {
			continue
		}
		if delta > 0 {
			lines[index] = strings.Repeat(" ", delta) + line
			continue
		}
		remove := -delta
		trimmed := 0
		for trimmed < remove && trimmed < len(line) && line[trimmed] == ' ' {
			trimmed++
		}
		lines[index] = line[trimmed:]
	}
	return strings.Join(lines, "")
}

// removeEntity deletes an entity subtree. An emptied ChildEntities element is
// collapsed to "<ChildEntities />" as Sysmac Studio writes it.
func (doc *oemTextDocument) removeEntity(entity *oemTextEntity) ([]byte, error) {
	parent := entity.parent
	if parent == nil {
		return nil, errors.New("OEM solution root cannot be deleted")
	}
	if len(parent.children) == 1 && parent.childEntities != nil && !parent.childEntities.selfClosing {
		container := *parent.childEntities
		return splice(doc.data, container.start, container.end, []byte("<ChildEntities />")), nil
	}
	start, end := doc.entityBlock(entity)
	return splice(doc.data, start, end, nil), nil
}

// insertEntityBlock inserts block (an entity rendered with zero base
// indentation and doc.eol line endings, without trailing terminator) as the
// last child of parent.
func (doc *oemTextDocument) insertEntityBlock(parent *oemTextEntity, block string) ([]byte, error) {
	data := doc.data
	pretty := isPretty(data, parent.span)
	parentIndent := ""
	if pretty {
		parentIndent = indentOf(data, parent.span.start)
	}
	childIndent := parentIndent + "    "
	if parent.childEntities == nil {
		// Insert a ChildEntities element just before the parent's end tag.
		if parent.span.selfClosing {
			return nil, errors.New("cannot add a child to a self-closing OEM entity")
		}
		var text string
		if pretty {
			text = parentIndent + "  <ChildEntities>" + doc.eol + reindentBlock(block, len(childIndent)) + doc.eol + parentIndent + "  </ChildEntities>" + doc.eol
			position := lineStart(data, parent.span.closeStart)
			return splice(data, position, position, []byte(text)), nil
		}
		text = "<ChildEntities>" + compactBlock(block) + "</ChildEntities>"
		return splice(data, parent.span.closeStart, parent.span.closeStart, []byte(text)), nil
	}
	container := *parent.childEntities
	if pretty {
		childIndent = indentOf(data, container.start) + "  "
	}
	if container.selfClosing {
		var text string
		if pretty {
			containerIndent := indentOf(data, container.start)
			text = "<ChildEntities>" + doc.eol + reindentBlock(block, len(childIndent)) + doc.eol + containerIndent + "</ChildEntities>"
		} else {
			text = "<ChildEntities>" + compactBlock(block) + "</ChildEntities>"
		}
		return splice(data, container.start, container.end, []byte(text)), nil
	}
	if pretty && lineStart(data, container.closeStart) != container.closeStart {
		position := lineStart(data, container.closeStart)
		text := reindentBlock(block, len(childIndent)) + doc.eol
		return splice(data, position, position, []byte(text)), nil
	}
	return splice(data, container.closeStart, container.closeStart, []byte(compactBlock(block))), nil
}

func compactBlock(block string) string {
	lines := strings.Split(strings.ReplaceAll(block, "\r\n", "\n"), "\n")
	for index, line := range lines {
		lines[index] = strings.TrimLeft(line, " \t")
	}
	return strings.Join(lines, "")
}

// subtreeBlock returns the entity text re-based to zero indentation, without
// the trailing line terminator.
func (doc *oemTextDocument) subtreeBlock(entity *oemTextEntity) string {
	indent := 0
	if isPretty(doc.data, entity.span) {
		indent = len(indentOf(doc.data, entity.span.start))
	}
	start := entity.span.start - indent
	return reindentBlock(string(doc.data[start:entity.span.end]), -indent)
}

func xmlAttrEscape(value string) string {
	var buffer bytes.Buffer
	for _, r := range value {
		switch r {
		case '&':
			buffer.WriteString("&amp;")
		case '<':
			buffer.WriteString("&lt;")
		case '>':
			buffer.WriteString("&gt;")
		case '"':
			buffer.WriteString("&quot;")
		case '\r':
			buffer.WriteString("&#xD;")
		case '\n':
			buffer.WriteString("&#xA;")
		case '\t':
			buffer.WriteString("&#x9;")
		default:
			buffer.WriteRune(r)
		}
	}
	return buffer.String()
}

var oemAttrPatterns = map[string]*regexp.Regexp{}

func oemAttrPattern(name string) *regexp.Regexp {
	if pattern, ok := oemAttrPatterns[name]; ok {
		return pattern
	}
	pattern := regexp.MustCompile(`(\s` + regexp.QuoteMeta(name) + `=")[^"]*(")`)
	oemAttrPatterns[name] = pattern
	return pattern
}

// setStartTagAttr replaces (or appends) an attribute inside a raw start tag.
func setStartTagAttr(tag, name, value string) string {
	pattern := oemAttrPattern(name)
	escaped := xmlAttrEscape(value)
	if pattern.MatchString(tag) {
		replaced := false
		return pattern.ReplaceAllStringFunc(tag, func(match string) string {
			if replaced {
				return match
			}
			replaced = true
			parts := pattern.FindStringSubmatch(match)
			return parts[1] + escaped + parts[2]
		})
	}
	closing := ">"
	body := strings.TrimSuffix(tag, ">")
	if strings.HasSuffix(body, "/") {
		closing = " />"
		body = strings.TrimRight(strings.TrimSuffix(body, "/"), " ")
	}
	return body + " " + name + `="` + escaped + `"` + closing
}

func removeStartTagAttr(tag, name string) string {
	return regexp.MustCompile(`\s`+regexp.QuoteMeta(name)+`="[^"]*"`).ReplaceAllString(tag, "")
}

var oemEntityStartTag = regexp.MustCompile(`<Entity\s[^>]*>`)

// mapEntityStartTags applies fn to every <Entity ...> start tag in text.
func mapEntityStartTags(text string, fn func(tag string) string) string {
	return oemEntityStartTag.ReplaceAllStringFunc(text, fn)
}

func oemTimestamp(now time.Time) string {
	return now.Format("01/02/2006 15:04:05")
}

func loadOEMText(path string) (*oemTextDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read OEM document: %w", err)
	}
	return parseOEMText(data)
}

func writeOEMText(path string, data []byte, protectedRoot string) error {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if protectedRoot != "" {
		protected, err := filepath.Abs(protectedRoot)
		if err != nil {
			return err
		}
		if isWithin(protected, resolved) {
			return errors.New("refusing write inside protected Sysmac project root")
		}
	}
	// Refuse to persist anything that does not parse as an OEM index.
	if _, err := parseOEMText(data); err != nil {
		return fmt.Errorf("refusing to write invalid OEM document: %w", err)
	}
	original, err := os.ReadFile(resolved)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(resolved), ".sysmac-oem-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(resolved+".backup", original, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, resolved)
}

type OEMMutation struct {
	Operation string
	EntityID  string
	ParentID  string
	Name      string
	Type      string
	Subtype   string
}

// ApplyOEMMutation edits the .oem index in place, keeping the native layout.
func ApplyOEMMutation(path string, mutation OEMMutation, protectedRoot string) (string, error) {
	doc, err := loadOEMText(path)
	if err != nil {
		return "", err
	}
	updated, changedID, err := applyOEMMutationText(doc, mutation, time.Now())
	if err != nil {
		return "", err
	}
	if err := writeOEMText(path, updated, protectedRoot); err != nil {
		return "", err
	}
	return changedID, nil
}

func applyOEMMutationText(doc *oemTextDocument, mutation OEMMutation, now time.Time) ([]byte, string, error) {
	entity := doc.entities[mutation.EntityID]
	changedID := mutation.EntityID
	switch mutation.Operation {
	case "rename":
		if entity == nil {
			return nil, "", fmt.Errorf("OEM entity %q not found", mutation.EntityID)
		}
		if mutation.Name == "" {
			return nil, "", errors.New("entity name is required")
		}
		tag := string(doc.data[entity.span.start:entity.span.startTagEnd])
		tag = setStartTagAttr(tag, "name", mutation.Name)
		return splice(doc.data, entity.span.start, entity.span.startTagEnd, []byte(tag)), changedID, nil
	case "create":
		parent := doc.entities[mutation.ParentID]
		if parent == nil {
			return nil, "", fmt.Errorf("parent entity %q not found", mutation.ParentID)
		}
		if mutation.Name == "" || mutation.Type == "" {
			return nil, "", errors.New("entity name and type are required")
		}
		if mutation.EntityID == "" {
			mutation.EntityID = newEntityID()
			changedID = mutation.EntityID
		}
		if entity != nil {
			return nil, "", fmt.Errorf("entity %q already exists", mutation.EntityID)
		}
		stamp := oemTimestamp(now)
		block := `<Entity type="` + xmlAttrEscape(mutation.Type) + `" subtype="` + xmlAttrEscape(mutation.Subtype) + `" id="` + xmlAttrEscape(mutation.EntityID) + `" name="` + xmlAttrEscape(mutation.Name) + `" version="0" dateCreated="` + stamp + `" dateLastModified="` + stamp + `" trackingId="` + newEntityID() + `">` + doc.eol +
			"  <AccessInfos />" + doc.eol +
			"  <ChildEntities />" + doc.eol +
			"</Entity>"
		updated, err := doc.insertEntityBlock(parent, block)
		return updated, changedID, err
	case "move":
		if entity == nil {
			return nil, "", fmt.Errorf("OEM entity %q not found", mutation.EntityID)
		}
		parent := doc.entities[mutation.ParentID]
		if parent == nil {
			return nil, "", fmt.Errorf("parent entity %q not found", mutation.ParentID)
		}
		if entity.parent == nil || entity == parent || oemContains(entity, parent) {
			return nil, "", errors.New("invalid OEM move hierarchy")
		}
		if entity.parent == parent {
			return doc.data, changedID, nil
		}
		block := doc.subtreeBlock(entity)
		removed, err := doc.removeEntity(entity)
		if err != nil {
			return nil, "", err
		}
		next, err := parseOEMText(removed)
		if err != nil {
			return nil, "", err
		}
		updated, err := next.insertEntityBlock(next.entities[mutation.ParentID], block)
		return updated, changedID, err
	case "clone":
		if entity == nil {
			return nil, "", fmt.Errorf("OEM entity %q not found", mutation.EntityID)
		}
		parent := doc.entities[mutation.ParentID]
		if parent == nil {
			return nil, "", fmt.Errorf("parent entity %q not found", mutation.ParentID)
		}
		block := doc.subtreeBlock(entity)
		first := true
		block = mapEntityStartTags(block, func(tag string) string {
			tag = setStartTagAttr(tag, "id", newEntityID())
			tag = setStartTagAttr(tag, "trackingId", newEntityID())
			if first {
				first = false
				if mutation.Name != "" {
					tag = setStartTagAttr(tag, "name", mutation.Name)
				}
			}
			return tag
		})
		changedID = extractAttr(block, "id")
		updated, err := doc.insertEntityBlock(parent, block)
		return updated, changedID, err
	case "delete":
		if entity == nil {
			return nil, "", fmt.Errorf("OEM entity %q not found", mutation.EntityID)
		}
		updated, err := doc.removeEntity(entity)
		return updated, changedID, err
	default:
		return nil, "", fmt.Errorf("unsupported OEM mutation %q", mutation.Operation)
	}
}

// extractAttr returns the first value of attribute name in text.
func extractAttr(text, name string) string {
	match := oemAttrPattern(name).FindStringSubmatchIndex(" " + text)
	if match == nil {
		return ""
	}
	full := " " + text
	value := full[match[3]:match[4]]
	return value
}

func oemContains(root, target *oemTextEntity) bool {
	for _, child := range root.children {
		if child == target || oemContains(child, target) {
			return true
		}
	}
	return false
}
