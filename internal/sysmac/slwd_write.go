package sysmac

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Native Sysmac SLWD variable sources are line oriented. Every variable line
// is a TAB separated list of KEY=VALUE fields, for example
//
//	++D=ARRAY[0..24] OF sTagData<TAB>N=Tags<TAB>IV=(Name := 'x', Id := 1)<TAB>G=VAR
//
// Values are stored literally (they may contain spaces, quotes and
// parentheses) and lines are terminated by CRLF. The document below keeps
// every line verbatim and only re-renders the fields of a variable that was
// explicitly changed, so an unchanged document round-trips byte-identically.

// slwdField is a single KEY=VALUE field of a variable line. The key of the
// first field keeps its "+" prefix (for example "++D").
type slwdField struct {
	key   string
	value string
}

type slwdLine struct {
	raw      string // line content without the line terminator
	eol      string // "\r\n", "\n", "\r" or "" for the last line
	fields   []slwdField
	variable *SLWDVariable
	dirty    bool
	deleted  bool
}

// SLWDDocument is a loss-free editable SLWD source. Lines that are not
// understood by the parser and variables that are not modified are retained
// byte for byte.
type SLWDDocument struct {
	lines []*slwdLine
	eol   string
}

// SLWDVariablePatch describes a partial variable update. Nil pointers mean
// "keep the current value"; a pointer to an empty string removes an optional
// field (initial value, comment or attribute).
type SLWDVariablePatch struct {
	Name         string
	Type         *string
	InitialValue *string
	Group        *string
	Comment      *string
	Attributes   map[string]*string
	// RenameTo renames an existing variable after the other fields are applied.
	RenameTo *string
}

// slwdCanonicalOrder is the field order Sysmac Studio writes.
var slwdCanonicalOrder = []string{"D", "N", "IV", "AT", "R", "E", "Ord", "RO", "Const", "G", "Com", "EC"}

func slwdFieldRank(key string) int {
	key = strings.TrimLeft(key, "+")
	for index, candidate := range slwdCanonicalOrder {
		if candidate == key {
			return index
		}
	}
	return len(slwdCanonicalOrder)
}

func ParseSLWDDocument(data []byte) (SLWDDocument, error) {
	text := string(data)
	doc := SLWDDocument{}
	for len(text) > 0 {
		index := strings.IndexAny(text, "\r\n")
		line := &slwdLine{}
		if index < 0 {
			line.raw = text
			text = ""
		} else {
			line.raw = text[:index]
			if strings.HasPrefix(text[index:], "\r\n") {
				line.eol = "\r\n"
			} else {
				line.eol = text[index : index+1]
			}
			text = text[index+len(line.eol):]
		}
		if doc.eol == "" && line.eol != "" {
			doc.eol = line.eol
		}
		if isSLWDVariableLine(line.raw) {
			fields, err := splitSLWDFields(line.raw)
			if err != nil {
				return SLWDDocument{}, fmt.Errorf("invalid SLWD variable line %q: %w", line.raw, err)
			}
			variable, err := variableFromSLWDFields(fields)
			if err != nil {
				return SLWDDocument{}, fmt.Errorf("invalid SLWD variable line %q: %w", line.raw, err)
			}
			line.fields = fields
			line.variable = &variable
			// Lines that were not TAB separated (legacy space separated input)
			// are normalized to the native format when they are rewritten.
		}
		doc.lines = append(doc.lines, line)
	}
	if doc.eol == "" {
		doc.eol = "\r\n"
	}
	return doc, nil
}

func isSLWDVariableLine(raw string) bool {
	trimmed := strings.TrimLeft(strings.TrimPrefix(raw, utf8BOM), " \t")
	return strings.HasPrefix(trimmed, "++D=") || strings.HasPrefix(trimmed, "+++D=")
}

func splitSLWDFields(raw string) ([]slwdField, error) {
	raw = strings.TrimLeft(strings.TrimPrefix(raw, utf8BOM), " ")
	var parts []string
	if strings.Contains(raw, "\t") {
		parts = strings.Split(raw, "\t")
	} else {
		// Legacy, space separated representation with optional quoting.
		legacy, err := tokenizeLegacySLWDFields(raw)
		if err != nil {
			return nil, err
		}
		parts = legacy
	}
	fields := make([]slwdField, 0, len(parts))
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			// Keep unknown fragments so that they survive a rewrite.
			fields = append(fields, slwdField{key: part, value: "\x00"})
			continue
		}
		fields = append(fields, slwdField{key: key, value: value})
	}
	return fields, nil
}

func variableFromSLWDFields(fields []slwdField) (SLWDVariable, error) {
	var variable SLWDVariable
	for _, field := range fields {
		switch strings.TrimLeft(field.key, "+") {
		case "D":
			variable.Type = field.value
		case "N":
			variable.Name = field.value
		case "IV":
			variable.InitialValue = field.value
		case "G":
			variable.Group = field.value
		case "Com":
			variable.Comment = field.value
		default:
			if field.value == "\x00" {
				continue
			}
			if variable.Attributes == nil {
				variable.Attributes = map[string]string{}
			}
			variable.Attributes[strings.TrimLeft(field.key, "+")] = field.value
		}
	}
	if variable.Name == "" || variable.Type == "" {
		return SLWDVariable{}, errors.New("variable name (N) and type (D) are required")
	}
	return variable, nil
}

func renderSLWDFields(fields []slwdField) string {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		if field.value == "\x00" {
			parts = append(parts, field.key)
			continue
		}
		parts = append(parts, field.key+"="+field.value)
	}
	return strings.Join(parts, "\t")
}

func (d SLWDDocument) Variables() []SLWDVariable {
	var result []SLWDVariable
	for _, line := range d.lines {
		if line.deleted || line.variable == nil {
			continue
		}
		result = append(result, *line.variable)
	}
	return result
}

func validateSLWDValue(name, value string) error {
	if strings.ContainsAny(value, "\t\r\n") {
		return fmt.Errorf("SLWD %s must not contain TAB or line breaks", name)
	}
	return nil
}

func (patch SLWDVariablePatch) validate() error {
	if err := validateSLWDValue("name", patch.Name); err != nil {
		return err
	}
	for name, value := range map[string]*string{"type": patch.Type, "initial value": patch.InitialValue, "group": patch.Group, "comment": patch.Comment} {
		if value != nil {
			if err := validateSLWDValue(name, *value); err != nil {
				return err
			}
		}
	}
	for key, value := range patch.Attributes {
		if key == "" || strings.ContainsAny(key, "=\t\r\n+") {
			return fmt.Errorf("invalid SLWD attribute key %q", key)
		}
		switch key {
		case "D", "N", "IV", "G", "Com":
			return fmt.Errorf("SLWD attribute %q must be set through its dedicated field", key)
		}
		if value != nil {
			if err := validateSLWDValue("attribute "+key, *value); err != nil {
				return err
			}
		}
	}
	return nil
}

// AddVariable appends a new variable to the section of its group.
func (d *SLWDDocument) AddVariable(variable SLWDVariable) error {
	attributes := map[string]*string{}
	for key, value := range variable.Attributes {
		value := value
		attributes[key] = &value
	}
	patch := SLWDVariablePatch{Name: variable.Name, Type: &variable.Type, Attributes: attributes}
	if variable.InitialValue != "" {
		patch.InitialValue = &variable.InitialValue
	}
	if variable.Group != "" {
		patch.Group = &variable.Group
	}
	if variable.Comment != "" {
		patch.Comment = &variable.Comment
	}
	return d.AddVariablePatch(patch)
}

// AddVariablePatch inserts a new variable after the last variable of the
// section that owns its group (VAR_INPUT, VAR_OUTPUT and VAR_IN_OUT share the
// VAR_INPUT section in native files).
func (d *SLWDDocument) AddVariablePatch(patch SLWDVariablePatch) error {
	if err := patch.validate(); err != nil {
		return err
	}
	if patch.Name == "" || patch.Type == nil || *patch.Type == "" {
		return errors.New("SLWD variable name and type are required")
	}
	if d.findVariable(patch.Name) >= 0 {
		return fmt.Errorf("SLWD variable %q already exists", patch.Name)
	}
	if patch.RenameTo != nil && *patch.RenameTo != patch.Name {
		return fmt.Errorf("SLWD variable %q does not exist and cannot be renamed", patch.Name)
	}
	group := "VAR"
	if patch.Group != nil && *patch.Group != "" {
		group = *patch.Group
	}
	fields := []slwdField{{key: "++D", value: *patch.Type}, {key: "N", value: patch.Name}, {key: "G", value: group}}
	line := &slwdLine{fields: fields, dirty: true}
	applySLWDPatch(line, SLWDVariablePatch{InitialValue: patch.InitialValue, Comment: patch.Comment, Attributes: patch.Attributes})
	if _, ok := patch.Attributes["Ord"]; !ok && slwdParameterGroup(group) {
		ordValue := strconv.Itoa(d.nextOrder())
		applySLWDPatch(line, SLWDVariablePatch{Attributes: map[string]*string{"Ord": &ordValue}})
	}
	variable, err := variableFromSLWDFields(line.fields)
	if err != nil {
		return err
	}
	line.variable = &variable
	d.insertLine(d.insertionIndex(group), line)
	return nil
}

func slwdParameterGroup(group string) bool {
	switch group {
	case "VAR_INPUT", "VAR_OUTPUT", "VAR_IN_OUT":
		return true
	}
	return false
}

func slwdSectionForGroup(group string) string {
	if slwdParameterGroup(group) {
		return "VAR_INPUT"
	}
	return group
}

var slwdSectionHeaders = map[string]string{
	"VAR":          "+GN=VAR\tGVT=DefaultGroup",
	"VAR_INPUT":    "+GN=VAR_INPUT\tGA=InputOutput\tGVT=EdgeInputOutputGroup",
	"VAR_EXTERNAL": "+GN=VAR_EXTERNAL\tGA=External\tGVT=ExternalGroup",
	"RETURN":       "+GN=RETURN\tGA=Return\tGVT=ReturnGroup",
	"VAR_GLOBAL":   "+GN=VAR_GLOBAL\tGVT=GlobalNamespaceGroup",
}

func slwdSectionName(raw string) (string, bool) {
	if !strings.HasPrefix(raw, "+GN=") {
		return "", false
	}
	name := strings.TrimPrefix(raw, "+GN=")
	if index := strings.IndexAny(name, "\t "); index >= 0 {
		name = name[:index]
	}
	return name, true
}

// insertionIndex returns the line index at which a new variable of the given
// group is inserted. A missing section header is created when it is known.
func (d *SLWDDocument) insertionIndex(group string) int {
	section := slwdSectionForGroup(group)
	header := -1
	for index, line := range d.lines {
		if name, ok := slwdSectionName(line.raw); ok && !line.deleted && name == section {
			header = index
			break
		}
	}
	if header < 0 {
		if text, ok := slwdSectionHeaders[section]; ok {
			index := len(d.lines)
			d.insertLine(index, &slwdLine{raw: text})
			return index + 1
		}
		return len(d.lines)
	}
	index := header + 1
	for index < len(d.lines) {
		if _, ok := slwdSectionName(d.lines[index].raw); ok {
			break
		}
		if d.lines[index].variable == nil && !strings.HasPrefix(d.lines[index].raw, "+") {
			break
		}
		index++
	}
	return index
}

func (d *SLWDDocument) insertLine(index int, line *slwdLine) {
	// The new line takes the document line ending. When it is appended after
	// a last line without terminator, that line receives one.
	if index == len(d.lines) {
		if index > 0 && d.lines[index-1].eol == "" {
			d.lines[index-1].eol = d.eol
			line.eol = ""
		} else {
			line.eol = d.eol
		}
	} else {
		line.eol = d.eol
	}
	d.lines = append(d.lines, nil)
	copy(d.lines[index+1:], d.lines[index:])
	d.lines[index] = line
}

func (d *SLWDDocument) nextOrder() int {
	next := 0
	for _, line := range d.lines {
		if line.deleted || line.variable == nil {
			continue
		}
		if value, err := strconv.Atoi(line.variable.Attributes["Ord"]); err == nil && value+1 > next {
			next = value + 1
		}
	}
	return next
}

// UpdateVariable updates the non-empty fields of variable on the existing
// variable with the same name. Other fields are preserved.
func (d *SLWDDocument) UpdateVariable(variable SLWDVariable) error {
	patch := SLWDVariablePatch{Name: variable.Name}
	if variable.Type != "" {
		patch.Type = &variable.Type
	}
	if variable.InitialValue != "" {
		patch.InitialValue = &variable.InitialValue
	}
	if variable.Group != "" {
		patch.Group = &variable.Group
	}
	if variable.Comment != "" {
		patch.Comment = &variable.Comment
	}
	if len(variable.Attributes) > 0 {
		patch.Attributes = map[string]*string{}
		for key, value := range variable.Attributes {
			value := value
			patch.Attributes[key] = &value
		}
	}
	return d.PatchVariable(patch)
}

// PatchVariable applies a partial update to an existing variable. Only the
// fields named in the patch are touched; their position is preserved.
func (d *SLWDDocument) PatchVariable(patch SLWDVariablePatch) error {
	if patch.Name == "" {
		return errors.New("SLWD variable name is required")
	}
	if err := patch.validate(); err != nil {
		return err
	}
	if patch.Type != nil && *patch.Type == "" {
		return errors.New("SLWD variable type cannot be empty")
	}
	if patch.Group != nil && *patch.Group == "" {
		return errors.New("SLWD variable group cannot be empty")
	}
	index := d.findVariable(patch.Name)
	if index < 0 {
		return fmt.Errorf("SLWD variable %q not found", patch.Name)
	}
	line := d.lines[index]
	if applySLWDPatch(line, patch) {
		variable, err := variableFromSLWDFields(line.fields)
		if err != nil {
			return err
		}
		line.variable = &variable
	}
	if patch.RenameTo != nil && *patch.RenameTo != patch.Name {
		return d.RenameVariable(patch.Name, *patch.RenameTo)
	}
	return nil
}

// HasVariable reports whether a variable with the given name exists.
func (d *SLWDDocument) HasVariable(name string) bool { return d.findVariable(name) >= 0 }

// applySLWDPatch updates the fields of a line and reports whether anything
// changed.
func applySLWDPatch(line *slwdLine, patch SLWDVariablePatch) bool {
	changed := false
	set := func(key string, value *string) {
		if value == nil {
			return
		}
		if setSLWDField(line, key, *value) {
			changed = true
		}
	}
	set("D", patch.Type)
	set("IV", patch.InitialValue)
	set("G", patch.Group)
	set("Com", patch.Comment)
	keys := make([]string, 0, len(patch.Attributes))
	for key := range patch.Attributes {
		keys = append(keys, key)
	}
	sortSLWDKeys(keys)
	for _, key := range keys {
		set(key, patch.Attributes[key])
	}
	if changed {
		line.dirty = true
	}
	return changed
}

func sortSLWDKeys(keys []string) {
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && (slwdFieldRank(keys[j]) < slwdFieldRank(keys[j-1]) || (slwdFieldRank(keys[j]) == slwdFieldRank(keys[j-1]) && keys[j] < keys[j-1])); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
}

// setSLWDField sets, inserts or (for an empty value) removes a field.
func setSLWDField(line *slwdLine, key, value string) bool {
	for index, field := range line.fields {
		if strings.TrimLeft(field.key, "+") != key {
			continue
		}
		if value == "" {
			if index == 0 {
				return false
			}
			line.fields = append(line.fields[:index], line.fields[index+1:]...)
			return true
		}
		if field.value == value {
			return false
		}
		line.fields[index].value = value
		return true
	}
	if value == "" {
		return false
	}
	rank := slwdFieldRank(key)
	position := len(line.fields)
	for index, field := range line.fields {
		if index > 0 && slwdFieldRank(field.key) > rank {
			position = index
			break
		}
	}
	line.fields = append(line.fields, slwdField{})
	copy(line.fields[position+1:], line.fields[position:])
	line.fields[position] = slwdField{key: key, value: value}
	return true
}

func (d *SLWDDocument) RenameVariable(oldName, newName string) error {
	if newName == "" {
		return errors.New("new SLWD variable name is required")
	}
	if err := validateSLWDValue("name", newName); err != nil {
		return err
	}
	index := d.findVariable(oldName)
	if index < 0 {
		return fmt.Errorf("SLWD variable %q not found", oldName)
	}
	if oldName != newName && d.findVariable(newName) >= 0 {
		return fmt.Errorf("SLWD variable %q already exists", newName)
	}
	line := d.lines[index]
	if setSLWDField(line, "N", newName) {
		line.dirty = true
		updated := *line.variable
		updated.Name = newName
		line.variable = &updated
	}
	return nil
}

func (d *SLWDDocument) DeleteVariable(name string) error {
	index := d.findVariable(name)
	if index < 0 {
		return fmt.Errorf("SLWD variable %q not found", name)
	}
	line := d.lines[index]
	line.deleted = true
	// Keep the document terminator layout when the last line disappears.
	if line.eol == "" {
		for previous := index - 1; previous >= 0; previous-- {
			if !d.lines[previous].deleted {
				d.lines[previous].eol = ""
				break
			}
		}
	}
	return nil
}

func MarshalSLWDDocument(document SLWDDocument) ([]byte, error) {
	var builder strings.Builder
	for _, line := range document.lines {
		if line.deleted {
			continue
		}
		if line.dirty {
			builder.WriteString(renderSLWDFields(line.fields))
		} else {
			builder.WriteString(line.raw)
		}
		builder.WriteString(line.eol)
	}
	return []byte(builder.String()), nil
}

func (d *SLWDDocument) findVariable(name string) int {
	for index, line := range d.lines {
		if !line.deleted && line.variable != nil && line.variable.Name == name {
			return index
		}
	}
	return -1
}

// tokenizeLegacySLWDFields splits the space separated representation used by
// earlier versions of this tool. Native Sysmac files are TAB separated.
func tokenizeLegacySLWDFields(raw string) ([]string, error) {
	var fields []string
	var current strings.Builder
	inQuotes := false
	escaped := false
	for _, r := range raw {
		if escaped {
			if r != '\\' && r != '"' {
				return nil, fmt.Errorf("unsupported escape \\%c", r)
			}
			current.WriteRune(r)
			escaped = false
			continue
		}
		if inQuotes && r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			inQuotes = !inQuotes
			continue
		}
		if !inQuotes && r == ' ' {
			if current.Len() > 0 {
				fields = append(fields, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if escaped {
		return nil, errors.New("dangling escape")
	}
	if inQuotes {
		return nil, errors.New("unterminated quote")
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}
	return fields, nil
}

func WriteSLWDAtomic(path string, document SLWDDocument, protectedRoot string) error {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve SLWD path: %w", err)
	}
	if protectedRoot != "" {
		protected, err := filepath.Abs(protectedRoot)
		if err != nil {
			return fmt.Errorf("resolve protected root: %w", err)
		}
		if isWithin(protected, resolved) {
			return errors.New("refusing write inside protected Sysmac project root")
		}
	}
	original, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("read SLWD before write: %w", err)
	}
	updated, err := MarshalSLWDDocument(document)
	if err != nil {
		return err
	}
	backup := resolved + ".backup"
	if err := os.WriteFile(backup, original, 0600); err != nil {
		return fmt.Errorf("write SLWD backup: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(resolved), ".sysmac-slwd-*.tmp")
	if err != nil {
		return fmt.Errorf("create SLWD temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(updated); err != nil {
		tmp.Close()
		return fmt.Errorf("write SLWD temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flush SLWD temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close SLWD temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, resolved); err != nil {
		return fmt.Errorf("replace SLWD atomically: %w", err)
	}
	return nil
}

const utf8BOM = "\xef\xbb\xbf"
