package sysmac

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Library replacement reproduces what Sysmac Studio does when a referenced
// library (.slr) is swapped for another one. It was derived from a project
// before and after a manual replacement in Sysmac Studio 1.62:
//
//   - the library entity subtree of the .slr .oem is grafted under the
//     project's ReferredLibraries entity in place of the old library,
//     including nested referred libraries;
//   - TransferInformation entities and the entityVersion/entityRevision
//     attributes are dropped, every entity gets a new id, trackingIds are kept,
//     dateLastModified is set to the import time and the root library entity
//     is named after the .slr file;
//   - payload files are written as <new id>.xml: NullObject payloads and
//     plain-text Inline payloads are omitted, Variables payloads are converted
//     from the XML VariableGroup form to SLWD, DataType payloads lose their Id
//     attributes and the root DataType gets its entity name, XML payloads get a
//     UTF-8 BOM, and the root library payload records the .slr path;
//   - the LibraryModel of the old library in the ReferredLibraries payload is
//     replaced by the LibraryModel of the new library;
//   - all files of the old library subtree are removed.
//
// Build artefacts (NexBuildResults, NexTransferInformation, controller .zip)
// still reference the old library until the project is rebuilt in Sysmac
// Studio; DN (display name) attributes are not generated.

// LibraryReplaceOptions configures ReplaceLibrary.
type LibraryReplaceOptions struct {
	// ProjectRoot is the extracted Solution directory containing the .oem.
	ProjectRoot string
	// LibraryPath is the new library: an .slr archive or an extracted
	// directory containing <GUID>.oem and its payload files.
	LibraryPath string
	// OldLibrary selects the library to replace by entity name, entity id or
	// LibraryId. It may be empty when the project references one library.
	OldLibrary string
	// NewName overrides the library entity name (default: .slr base name).
	NewName string
	// LibraryFilePath is recorded as LibraryFilePath (default: absolute
	// LibraryPath when it is an .slr file).
	LibraryFilePath string
	// Now is used for dateLastModified (default time.Now()).
	Now time.Time
}

// LibraryInfo summarizes a referenced library.
type LibraryInfo struct {
	Name        string `json:"name"`
	EntityID    string `json:"entityId"`
	LibraryID   string `json:"libraryId,omitempty"`
	LibraryName string `json:"libraryName,omitempty"`
	Version     string `json:"version,omitempty"`
	Entities    int    `json:"entities"`
}

// LibraryReplaceResult reports what ReplaceLibrary changed.
type LibraryReplaceResult struct {
	OldLibrary   LibraryInfo `json:"oldLibrary"`
	NewLibrary   LibraryInfo `json:"newLibrary"`
	RemovedFiles []string    `json:"removedFiles"`
	WrittenFiles []string    `json:"writtenFiles"`
	SkippedFiles []string    `json:"skippedPayloads,omitempty"`
	Warnings     []string    `json:"warnings,omitempty"`
}

type librarySource struct {
	oemName string
	files   map[string][]byte // payload files by base name
}

func loadLibrarySource(path string) (*librarySource, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat library: %w", err)
	}
	source := &librarySource{files: map[string][]byte{}}
	if info.IsDir() {
		oem, err := findOEM(path)
		if err != nil {
			return nil, err
		}
		dir := filepath.Dir(oem)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				return nil, err
			}
			source.files[entry.Name()] = data
		}
		source.oemName = filepath.Base(oem)
		return source, nil
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open library archive: %w", err)
	}
	defer archive.Close()
	oemDir := ""
	for _, file := range archive.File {
		if strings.EqualFold(filepath.Ext(file.Name), ".oem") {
			if oemDir != "" {
				return nil, errors.New("library archive contains more than one .oem index")
			}
			oemDir = strings.TrimSuffix(file.Name, filepath.Base(file.Name))
			source.oemName = filepath.Base(file.Name)
		}
	}
	if source.oemName == "" {
		return nil, errors.New("library archive does not contain a .oem index")
	}
	for _, file := range archive.File {
		if file.FileInfo().IsDir() || !strings.HasPrefix(file.Name, oemDir) || strings.Contains(strings.TrimPrefix(file.Name, oemDir), "/") {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return nil, err
		}
		source.files[filepath.Base(file.Name)] = data
	}
	return source, nil
}

func (doc *oemTextDocument) entityType(entity *oemTextEntity) string {
	return doc.attr(entity, "type")
}

// topLevelLibraries returns Library entities referenced directly by the
// project (not nested inside another library).
func (doc *oemTextDocument) topLevelLibraries() []*oemTextEntity {
	var result []*oemTextEntity
	for _, entity := range doc.order {
		if doc.entityType(entity) != "Library" || entity.parent == nil || doc.entityType(entity.parent) != "ReferredLibraries" {
			continue
		}
		nested := false
		for ancestor := entity.parent; ancestor != nil; ancestor = ancestor.parent {
			if doc.entityType(ancestor) == "Library" {
				nested = true
				break
			}
		}
		if !nested {
			result = append(result, entity)
		}
	}
	return result
}

func subtreeEntities(entity *oemTextEntity) []*oemTextEntity {
	result := []*oemTextEntity{entity}
	for _, child := range entity.children {
		result = append(result, subtreeEntities(child)...)
	}
	return result
}

var (
	libraryIDPattern   = regexp.MustCompile(`<LibraryId>([^<]*)</LibraryId>`)
	libraryNamePattern = regexp.MustCompile(`<Name>([^<]*)</Name>`)
	libraryVerPattern  = regexp.MustCompile(`<Version>([^<]*)</Version>`)
)

func firstSubmatch(pattern *regexp.Regexp, data []byte) string {
	match := pattern.FindSubmatch(data)
	if match == nil {
		return ""
	}
	return string(match[1])
}

// ReplaceLibrary replaces a referenced library in an extracted Solution
// directory. Call it on a transaction staging copy.
func ReplaceLibrary(options LibraryReplaceOptions) (LibraryReplaceResult, error) {
	var result LibraryReplaceResult
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	root := options.ProjectRoot
	oemPath, err := findOEM(root)
	if err != nil {
		return result, err
	}
	projectDir := filepath.Dir(oemPath)
	project, err := loadOEMText(oemPath)
	if err != nil {
		return result, err
	}

	// Locate the library to replace.
	candidates := project.topLevelLibraries()
	if len(candidates) == 0 {
		return result, errors.New("project does not reference any library")
	}
	var old *oemTextEntity
	var oldPayload []byte
	var names []string
	for _, candidate := range candidates {
		payload, _ := os.ReadFile(filepath.Join(projectDir, candidate.id+".xml"))
		name := project.attr(candidate, "name")
		names = append(names, name)
		if options.OldLibrary == "" && len(candidates) == 1 ||
			options.OldLibrary != "" && (name == options.OldLibrary || candidate.id == options.OldLibrary || strings.EqualFold(firstSubmatch(libraryIDPattern, payload), options.OldLibrary)) {
			if old != nil {
				return result, fmt.Errorf("library selector %q is ambiguous", options.OldLibrary)
			}
			old, oldPayload = candidate, payload
		}
	}
	if old == nil {
		return result, fmt.Errorf("library %q not found; referenced libraries: %s", options.OldLibrary, strings.Join(names, ", "))
	}
	oldLibraryID := firstSubmatch(libraryIDPattern, oldPayload)
	oldEntities := subtreeEntities(old)
	result.OldLibrary = LibraryInfo{Name: project.attr(old, "name"), EntityID: old.id, LibraryID: oldLibraryID, LibraryName: firstSubmatch(libraryNamePattern, oldPayload), Version: firstSubmatch(libraryVerPattern, oldPayload), Entities: len(oldEntities)}
	referred := old.parent
	referredPayloadPath := filepath.Join(projectDir, referred.id+".xml")
	referredPayload, err := os.ReadFile(referredPayloadPath)
	if err != nil {
		return result, fmt.Errorf("read ReferredLibraries payload: %w", err)
	}

	// Load and prepare the new library.
	source, err := loadLibrarySource(options.LibraryPath)
	if err != nil {
		return result, err
	}
	libraryDoc, err := parseOEMText(source.files[source.oemName])
	if err != nil {
		return result, fmt.Errorf("library index: %w", err)
	}
	if libraryDoc.entityType(libraryDoc.root) != "Library" {
		return result, fmt.Errorf("library index root is %q, expected Library", libraryDoc.entityType(libraryDoc.root))
	}
	for {
		var transfer *oemTextEntity
		for _, entity := range libraryDoc.order {
			if entity.parent != nil && libraryDoc.entityType(entity) == "TransferInformation" {
				transfer = entity
				break
			}
		}
		if transfer == nil {
			break
		}
		data, err := libraryDoc.removeEntity(transfer)
		if err != nil {
			return result, err
		}
		if libraryDoc, err = parseOEMText(data); err != nil {
			return result, err
		}
	}
	newName := options.NewName
	if newName == "" {
		base := filepath.Base(options.LibraryPath)
		newName = strings.TrimSuffix(base, filepath.Ext(base))
	}
	stamp := oemTimestamp(options.Now)
	idMap := map[string]string{}
	block := libraryDoc.subtreeBlock(libraryDoc.root)
	first := true
	block = mapEntityStartTags(block, func(tag string) string {
		oldID := extractAttr(tag, "id")
		newID := newEntityID()
		idMap[oldID] = newID
		tag = setStartTagAttr(tag, "id", newID)
		tag = setStartTagAttr(tag, "dateLastModified", stamp)
		tag = removeStartTagAttr(tag, "entityVersion")
		tag = removeStartTagAttr(tag, "entityRevision")
		if first {
			first = false
			tag = setStartTagAttr(tag, "name", newName)
		}
		return tag
	})

	libraryFilePath := options.LibraryFilePath
	if libraryFilePath == "" {
		if info, err := os.Stat(options.LibraryPath); err == nil && !info.IsDir() {
			libraryFilePath, _ = filepath.Abs(options.LibraryPath)
		}
	}
	relativePath := ""
	if home, err := os.UserHomeDir(); err == nil && libraryFilePath != "" {
		if rel, err := filepath.Rel(home, libraryFilePath); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			relativePath = rel
		}
	}

	rootPayload, ok := source.files[libraryDoc.root.id+".xml"]
	if !ok {
		return result, errors.New("library archive lacks the root library payload")
	}
	rootPayload = setLibraryFilePaths(rootPayload, libraryFilePath, relativePath)
	newLibraryID := firstSubmatch(libraryIDPattern, rootPayload)
	result.NewLibrary = LibraryInfo{Name: newName, EntityID: idMap[libraryDoc.root.id], LibraryID: newLibraryID, LibraryName: firstSubmatch(libraryNamePattern, rootPayload), Version: firstSubmatch(libraryVerPattern, rootPayload), Entities: len(libraryDoc.order)}

	// Update ReferredLibraries payload.
	newReferred, err := replaceReferredLibraryModel(referredPayload, oldLibraryID, rootPayload)
	if err != nil {
		return result, err
	}

	// Graft the subtree into the project index.
	start, end := project.entityBlock(old)
	indent := 0
	if isPretty(project.data, old.span) {
		indent = len(indentOf(project.data, old.span.start))
	}
	replacement := reindentBlock(block, indent)
	if end > old.span.end {
		replacement += project.eol
	}
	updatedOEM := splice(project.data, start, end, []byte(replacement))
	if _, err := parseOEMText(updatedOEM); err != nil {
		return result, fmt.Errorf("grafted index is invalid: %w", err)
	}

	// Prepare payload files for the new entities.
	newFiles := map[string][]byte{}
	for _, entity := range libraryDoc.order {
		newID := idMap[entity.id]
		for name, data := range source.files {
			if !strings.HasPrefix(name, entity.id+".") || name == source.oemName {
				continue
			}
			ext := strings.TrimPrefix(name, entity.id)
			converted, keep, err := convertLibraryPayload(libraryDoc, entity, ext, data)
			if err != nil {
				return result, fmt.Errorf("convert payload %s: %w", name, err)
			}
			if entity == libraryDoc.root && ext == ".xml" {
				converted, keep = addUTF8BOM(rootPayload), true
			}
			if !keep {
				result.SkippedFiles = append(result.SkippedFiles, name)
				continue
			}
			newFiles[newID+ext] = converted
		}
	}

	// Apply: remove old files, write new files, index and ReferredLibraries.
	oldIDs := map[string]bool{}
	for _, entity := range oldEntities {
		oldIDs[entity.id] = true
	}
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		base := entry.Name()
		if index := strings.Index(base, "."); index > 0 && oldIDs[base[:index]] {
			if err := os.Remove(filepath.Join(projectDir, base)); err != nil {
				return result, err
			}
			result.RemovedFiles = append(result.RemovedFiles, base)
		}
	}
	for name, data := range newFiles {
		if err := os.WriteFile(filepath.Join(projectDir, name), data, 0600); err != nil {
			return result, err
		}
		result.WrittenFiles = append(result.WrittenFiles, name)
	}
	if err := os.WriteFile(referredPayloadPath, newReferred, 0600); err != nil {
		return result, err
	}
	if err := os.WriteFile(oemPath, updatedOEM, 0600); err != nil {
		return result, err
	}
	result.WrittenFiles = append(result.WrittenFiles, filepath.Base(referredPayloadPath), filepath.Base(oemPath))
	sort.Strings(result.RemovedFiles)
	sort.Strings(result.WrittenFiles)
	sort.Strings(result.SkippedFiles)
	result.Warnings = append(result.Warnings,
		"Rebuild the project in Sysmac Studio: build artefacts (NexBuildResults, NexTransferInformation, controller .zip) still describe the old library.",
		"Program code that calls POUs of the old library is not changed; renamed or removed function blocks must be adapted manually.")
	return result, nil
}

func addUTF8BOM(data []byte) []byte {
	if bytes.HasPrefix(data, []byte(utf8BOM)) || !bytes.HasPrefix(data, []byte("<?xml")) {
		return data
	}
	return append([]byte(utf8BOM), data...)
}

func xmlTextEscape(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return strings.NewReplacer("&#xA;", "\n", "&#xD;", "\r", "&#x9;", "\t", "&#34;", `"`, "&#39;", "'").Replace(buffer.String())
}

func setLibraryFilePaths(payload []byte, absolute, relative string) []byte {
	set := func(data []byte, tag, value string) []byte {
		pattern := regexp.MustCompile(`<` + tag + `>[^<]*</` + tag + `>|<` + tag + ` />`)
		replaced := false
		return pattern.ReplaceAllFunc(data, func(match []byte) []byte {
			if replaced {
				return match
			}
			replaced = true
			return []byte("<" + tag + ">" + xmlTextEscape(value) + "</" + tag + ">")
		})
	}
	payload = set(payload, "LibraryFilePath", absolute)
	return set(payload, "LibraryFileRelativePath", relative)
}

// convertLibraryPayload turns a payload file of the .slr into the form
// Sysmac Studio stores in a project. keep=false omits the file.
func convertLibraryPayload(doc *oemTextDocument, entity *oemTextEntity, ext string, data []byte) ([]byte, bool, error) {
	if ext != ".xml" {
		return data, true, nil
	}
	body := bytes.TrimPrefix(data, []byte(utf8BOM))
	if bytes.Contains(body[:min(len(body), 512)], []byte("<NullObject")) {
		return nil, false, nil
	}
	switch doc.entityType(entity) {
	case "Variables":
		if bytes.Contains(body, []byte("<VariableGroup")) {
			converted, err := ConvertVariablesXMLToSLWD(body)
			return converted, true, err
		}
	case "Inline":
		// Library sources are stored protected. Plain-text inline sections are
		// not persisted by Sysmac Studio in the project.
		if !bytes.Contains(body, []byte("EncryptedFile")) {
			return nil, false, nil
		}
		return data, true, nil
	case "DataType":
		// Only the root data type group (without namespace) receives its
		// entity name; namespaced groups keep an empty Name.
		name := ""
		if doc.attr(entity, "namespace") == "" {
			name = doc.attr(entity, "name")
		}
		return addUTF8BOM(normalizeLibraryDataType(body, name)), true, nil
	}
	return addUTF8BOM(data), true, nil
}

var (
	dataTypeIDAttr    = regexp.MustCompile(`(<DataType\s[^>]*?) Id="[^"]*"`)
	firstDataTypeName = regexp.MustCompile(`<DataType Name=""`)
)

func normalizeLibraryDataType(data []byte, entityName string) []byte {
	for {
		next := dataTypeIDAttr.ReplaceAll(data, []byte("$1"))
		if bytes.Equal(next, data) {
			break
		}
		data = next
	}
	if location := firstDataTypeName.FindIndex(data); location != nil && entityName != "" {
		first := bytes.Index(data, []byte("<DataType "))
		if first == location[0] {
			data = splice(data, location[0], location[1], []byte(`<DataType Name="`+xmlAttrEscape(entityName)+`"`))
		}
	}
	return data
}

type xmlVariables struct {
	Groups []struct {
		Name      string `xml:"Name,attr"`
		Attribute string `xml:"Attribute,attr"`
		ViewType  string `xml:"ViewType,attr"`
		Variables []struct {
			DataTypeName string `xml:"DataTypeName,attr"`
			Name         string `xml:"Name,attr"`
			Address      string `xml:"Address,attr"`
			Retained     string `xml:"Retained,attr"`
			InitialValue string `xml:"InitialValue,attr"`
			Comment      string `xml:"Comment,attr"`
			Edge         string `xml:"Edge,attr"`
			Order        string `xml:"Order,attr"`
			IsReadOnly   string `xml:"IsReadOnly,attr"`
			IsConstant   string `xml:"IsConstant,attr"`
			Group        string `xml:"Group,attr"`
			Element      []struct {
				Key     string `xml:"ElementKey,attr"`
				Comment string `xml:"Comment,attr"`
			} `xml:"ElementComments>ElementComment"`
		} `xml:"Variable"`
	} `xml:"VariableGroup"`
}

// ConvertVariablesXMLToSLWD converts the XML VariableGroup payload used in
// .slr archives to the SLWD text Sysmac Studio stores in projects.
func ConvertVariablesXMLToSLWD(data []byte) ([]byte, error) {
	var model xmlVariables
	if err := xml.Unmarshal(bytes.TrimPrefix(data, []byte(utf8BOM)), &model); err != nil {
		return nil, fmt.Errorf("parse variables XML: %w", err)
	}
	clean := func(value string) string {
		return strings.NewReplacer("\t", " ", "\r\n", " ", "\r", " ", "\n", " ").Replace(value)
	}
	lines := []string{"[SLWD version=1.0]", "_EN=Variables"}
	for _, group := range model.Groups {
		header := []string{"+GN=" + group.Name}
		if group.Attribute != "" {
			header = append(header, "GA="+group.Attribute)
		}
		header = append(header, "GVT="+group.ViewType)
		lines = append(lines, strings.Join(header, "\t"))
		for _, variable := range group.Variables {
			fields := []string{"++D=" + clean(variable.DataTypeName), "N=" + clean(variable.Name)}
			if variable.InitialValue != "" {
				fields = append(fields, "IV="+clean(variable.InitialValue))
			}
			if variable.Address != "" {
				fields = append(fields, "AT="+clean(variable.Address))
			}
			if variable.Retained == "true" {
				fields = append(fields, "R=1")
			}
			if variable.Edge != "" && variable.Edge != "NoEdge" {
				fields = append(fields, "E="+variable.Edge)
			}
			if variable.Order != "" {
				fields = append(fields, "Ord="+variable.Order)
			}
			if variable.IsReadOnly == "true" {
				fields = append(fields, "RO=1")
			}
			if variable.IsConstant == "true" {
				fields = append(fields, "Const=1")
			}
			fields = append(fields, "G="+variable.Group)
			if variable.Comment != "" {
				fields = append(fields, "Com="+clean(variable.Comment))
			}
			if len(variable.Element) > 0 {
				var builder strings.Builder
				builder.WriteString("EC=<ECs>")
				for _, element := range variable.Element {
					builder.WriteString(`<EC EK="` + xmlAttrEscape(element.Key) + `" C="` + xmlAttrEscape(element.Comment) + `" />`)
				}
				builder.WriteString("</ECs>")
				fields = append(fields, clean(builder.String()))
			}
			lines = append(lines, strings.Join(fields, "\t"))
		}
	}
	return []byte(strings.Join(lines, "\r\n") + "\r\n"), nil
}

// replaceReferredLibraryModel swaps the <LibraryModel> of the referred
// library with LibraryId oldID for the LibraryModel of libraryPayload.
func replaceReferredLibraryModel(referred []byte, oldID string, libraryPayload []byte) ([]byte, error) {
	if oldID == "" {
		return nil, errors.New("old library payload has no LibraryId")
	}
	marker := []byte("<LibraryId>" + oldID + "</LibraryId>")
	at := bytes.Index(referred, marker)
	if at < 0 {
		return nil, fmt.Errorf("ReferredLibraries payload does not reference LibraryId %s", oldID)
	}
	modelStart := bytes.LastIndex(referred[:at], []byte("<LibraryModel>"))
	modelEndRel := bytes.Index(referred[at:], []byte("</LibraryModel>"))
	if modelStart < 0 || modelEndRel < 0 {
		return nil, errors.New("malformed ReferredLibraries payload")
	}
	modelEnd := at + modelEndRel + len("</LibraryModel>")
	sourceStart := bytes.Index(libraryPayload, []byte("<LibraryModel>"))
	sourceEnd := bytes.Index(libraryPayload, []byte("</LibraryModel>"))
	if sourceStart < 0 || sourceEnd < 0 {
		return nil, errors.New("library payload has no LibraryModel")
	}
	sourceEnd += len("</LibraryModel>")
	delta := len(indentOf(referred, modelStart)) - len(indentOf(libraryPayload, sourceStart))
	model := reindentXMLMarkup(libraryPayload[sourceStart:sourceEnd], delta)
	model = bytes.ReplaceAll(bytes.ReplaceAll(model, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte(detectEOL(referred)))
	return splice(referred, modelStart, modelEnd, model), nil
}

func detectEOL(data []byte) string {
	if bytes.Contains(data, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

// reindentXMLMarkup shifts the indentation of markup lines by delta spaces
// while leaving lines that continue a multi-line text node untouched.
func reindentXMLMarkup(snippet []byte, delta int) []byte {
	if delta == 0 {
		return append([]byte(nil), snippet...)
	}
	type span struct{ start, end int }
	var textSpans []span
	decoder := xml.NewDecoder(bytes.NewReader(snippet))
	for {
		start := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err != nil {
			break
		}
		if text, ok := token.(xml.CharData); ok && len(bytes.TrimSpace(text)) > 0 {
			textSpans = append(textSpans, span{start, int(decoder.InputOffset())})
		}
	}
	inText := func(offset int) bool {
		for _, candidate := range textSpans {
			if offset > candidate.start && offset < candidate.end {
				return true
			}
		}
		return false
	}
	var out bytes.Buffer
	lineBegin := 0
	for lineBegin <= len(snippet) {
		next := bytes.IndexByte(snippet[lineBegin:], '\n')
		lineEndOffset := len(snippet)
		if next >= 0 {
			lineEndOffset = lineBegin + next + 1
		}
		line := snippet[lineBegin:lineEndOffset]
		if lineBegin == 0 || inText(lineBegin) {
			out.Write(line)
		} else if delta > 0 {
			out.WriteString(strings.Repeat(" ", delta))
			out.Write(line)
		} else {
			trim := 0
			for trim < -delta && trim < len(line) && line[trim] == ' ' {
				trim++
			}
			out.Write(line[trim:])
		}
		if next < 0 {
			break
		}
		lineBegin = lineEndOffset
	}
	return out.Bytes()
}
