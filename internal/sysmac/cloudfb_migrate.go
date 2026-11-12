package sysmac

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MigrateCloudFBProgram adapts the user program of a project whose Cloud_FB
// 2.0.x library reference was already replaced by NX-IIOT_OptiCloud_V2 (see
// ReplaceLibrary). It reproduces the manual migration guide:
//
//   - instances of fbCloudConnect, fbCloud_TagGroup, fbCloud_EventGroup,
//     fbCloud_Telemetry and fbCloud_Batch are deleted and replaced by
//     ConnectionManager (fbConnectionManager), ProductionCounterService
//     (fbOpti_ProductionData_service), EventService (fbOpti_Event_Service),
//     TelemetryService (fbOpti_Telemetry_Service) and BatchService
//     (fbOpti_Batch_Service), appended to the VAR section;
//   - renamed struct members are renamed in code and initial values
//     (sBatchData.ExpectedLineSpeed → ExpectedSpeed);
//   - event tags the code uses beyond the per machine triple are kept as
//     extra events (never dropped), and EventTags[n].EventCode becomes
//     Stop_Cause where the tag is a Stop_Registered event and every write is
//     a stop cause literal;
//   - sTagGroupSetting/sTagData become sPCTagGroupSetting/sPCTagData, the
//     buffer bound to the tag group FB becomes ARRAY OF sPCSample, and unused
//     local sTagGroupSetting variables (tagGroupCountersCFG) are deleted;
//   - initial values of the part counter settings and the machine state event
//     group are converted (3 events RUN/Stop_UnRegistered/Stop_Registered per
//     machine, machines derived from the old project);
//   - ladder FB calls are renamed and their pins re-synchronised with the FB
//     signature of the referenced library (pin renames, new pins, Ix shift,
//     pin types), instance references are renamed in every rung;
//   - the PouBodySourceHolder link info is shifted with the Ix values;
//   - the IOalarm section that only feeds EventTags[].EventCode (removed in
//     V2) is deleted, or reported when it does anything else.
//
// Inline ST boxes (IST) are never modified; references inside them are only
// reported. Call it on a transaction staging copy.

const cloudNamespace = `Toolbox\Communication\Cloud`

type cloudFBMapping struct {
	OldFB, NewFB, Instance string
	// Pins maps lower-case old pin names to new pin names.
	Pins map[string]string
}

var cloudFBMappings = []*cloudFBMapping{
	{OldFB: "fbCloudConnect", NewFB: "fbConnectionManager", Instance: "ConnectionManager"},
	{OldFB: "fbCloud_TagGroup", NewFB: "fbOpti_ProductionData_service", Instance: "ProductionCounterService", Pins: map[string]string{
		"taggroupsetting":       "ServiceSetting",
		"taggroupbuffer":        "Buffer",
		"taggroupbuffersetting": "BufferSetting",
		"samplingrate":          "Sampling_Rate",
		"clear_taggroupbuffer":  "Clear_Buffer",
	}},
	{OldFB: "fbCloud_EventGroup", NewFB: "fbOpti_Event_Service", Instance: "EventService", Pins: map[string]string{
		"eventgroup":              "Event_Settings",
		"eventbuffer":             "Buffer",
		"eventgroupbuffersetting": "BufferSetting",
	}},
	// Same parameters as the old FB apart from the three renamed group pins
	// (signatures of Cloud_FB 2.0.13 and NX-IIOT_OptiCloud_V2_7).
	{OldFB: "fbCloud_Telemetry", NewFB: "fbOpti_Telemetry_Service", Instance: "TelemetryService", Pins: map[string]string{
		"telemetrygroupsetting":       "ServiceSetting",
		"telemetrygroupbuffer":        "Buffer",
		"telemetrygroupbuffersetting": "BufferSetting",
	}},
	// Same parameter names; V2 has no Product_Info input (the pin is
	// removed and its binding reported).
	{OldFB: "fbCloud_Batch", NewFB: "fbOpti_Batch_Service", Instance: "BatchService"},
}

var cloudTypeRenames = []struct{ Old, New string }{
	{"sTagGroupSetting", "sPCTagGroupSetting"},
	{"sTagData", "sPCTagData"},
}

// cloudMemberRenames are struct members renamed between Cloud_FB 2.0.x and
// V2 (sBatchData.ExpectedLineSpeed UINT → sBatchData.ExpectedSpeed UINT).
var cloudMemberRenames = []struct{ Type, Old, New string }{
	{"sBatchData", "ExpectedLineSpeed", "ExpectedSpeed"},
}

const (
	cloudSampleOld = "sSample"
	cloudSampleNew = "sPCSample"
)

// Event types in the order of the per machine triple.
var cloudEventTypes = []string{"RUN", "Stop_UnRegistered", "Stop_Registered"}

// CloudFBMigrationOptions configures MigrateCloudFBProgram.
type CloudFBMigrationOptions struct {
	ProjectRoot string
	// IOAlarm is "delete" (default: delete an IOalarm section that only feeds
	// EventCode) or "keep" (never delete, report only).
	IOAlarm string
	// MachineIDs overrides the machines that get event tags.
	MachineIDs []int
	Now        time.Time
}

// CloudFBChange is one entry of the change log.
type CloudFBChange struct {
	Target string `json:"target"`
	Change string `json:"change"`
}

// CloudFBMigrationResult reports what MigrateCloudFBProgram changed.
type CloudFBMigrationResult struct {
	Library       string          `json:"library"`
	Machines      []int           `json:"machines,omitempty"`
	MachineSource string          `json:"machineSource,omitempty"`
	Changes       []CloudFBChange `json:"changes"`
	// Unresolved lists findings that probably still fail the build.
	Unresolved   []string `json:"unresolved,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
	WrittenFiles []string `json:"writtenFiles"`
	RemovedFiles []string `json:"removedFiles,omitempty"`
}

type fbParam struct {
	Name, Group, Type, Edge string
	Ord                     int
}

type libType struct {
	Name, Base string
	Members    []*libType
}

type cloudVars struct {
	entity  *oemTextEntity
	label   string
	doc     SLWDDocument
	changed bool
}

type cloudBody struct {
	entity  *oemTextEntity
	label   string
	ladder  *ladderBody
	text    string // non-ladder bodies (scan only)
	pbsh    *oemTextEntity
	shifts  map[int][]func(int) int
	changed bool
	deleted bool
}

type cloudInstance struct {
	OldName string
	NewName string
	Mapping *cloudFBMapping
}

type cloudPOU struct {
	entity    *oemTextEntity
	name      string
	vars      *cloudVars
	bodies    []*cloudBody
	instances []*cloudInstance
}

type cloudEventRemap struct {
	variable string
	index    map[int]int
	// stopCause marks old indices whose EventCode references become
	// Stop_Cause (the tag is a Stop_Registered event in V2).
	stopCause map[int]bool
}

// eventCodeMemberPattern matches ".EventCode" after "EventTags[n]".
var eventCodeMemberPattern = regexp.MustCompile(`(?i)^\s*\.\s*(EventCode)\b`)

type cloudMigration struct {
	options    CloudFBMigrationOptions
	result     *CloudFBMigrationResult
	dir        string
	oemPath    string
	doc        *oemTextDocument
	sigs       map[string][]fbParam
	types      map[string]*libType
	pous       []*cloudPOU
	globals    []*cloudVars
	eventMaps  []*cloudEventRemap
	bufferVars map[string]bool
	machineRef string
	scopes     map[*cloudPOU]map[string]string
	// readOnly loads a project for analysis (sysmac_program_compare): the
	// Cloud_FB library is accepted and nothing is written.
	readOnly bool
}

// MigrateCloudFBProgram runs the Cloud_FB → NX-IIOT_OptiCloud_V2 program
// migration on an extracted Solution directory.
func MigrateCloudFBProgram(options CloudFBMigrationOptions) (CloudFBMigrationResult, error) {
	result := CloudFBMigrationResult{}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	switch strings.ToLower(options.IOAlarm) {
	case "":
		options.IOAlarm = "delete"
	case "delete", "keep":
		options.IOAlarm = strings.ToLower(options.IOAlarm)
	default:
		return result, fmt.Errorf("ioalarm must be delete or keep, got %q", options.IOAlarm)
	}
	m := &cloudMigration{options: options, result: &result, bufferVars: map[string]bool{}}
	if err := m.load(); err != nil {
		return result, err
	}
	m.analyzeInstances()
	m.deleteIOAlarm()
	if err := m.migrateEventGroups(); err != nil {
		return result, err
	}
	if err := m.migrateLadder(); err != nil {
		return result, err
	}
	if err := m.migrateVariables(); err != nil {
		return result, err
	}
	m.scanLeftovers()
	if err := m.write(); err != nil {
		return result, err
	}
	if len(result.Changes) == 0 {
		result.Warnings = append(result.Warnings, "nothing to migrate: no Cloud_FB usage found (already migrated?)")
	}
	result.Warnings = append(result.Warnings, "Rebuild the controller in Sysmac Studio: the PouBodySourceHolder link info and build artefacts are regenerated by the build.")
	return result, nil
}

func (m *cloudMigration) change(target, format string, args ...any) {
	m.result.Changes = append(m.result.Changes, CloudFBChange{Target: target, Change: fmt.Sprintf(format, args...)})
}

func (m *cloudMigration) unresolved(format string, args ...any) {
	m.result.Unresolved = append(m.result.Unresolved, fmt.Sprintf(format, args...))
}

func (m *cloudMigration) warn(format string, args ...any) {
	m.result.Warnings = append(m.result.Warnings, fmt.Sprintf(format, args...))
}

func (m *cloudMigration) payloadPath(entity *oemTextEntity) string {
	return filepath.Join(m.dir, entity.id+".xml")
}

func inLibrary(doc *oemTextDocument, entity *oemTextEntity) bool {
	for ancestor := entity; ancestor != nil; ancestor = ancestor.parent {
		if doc.entityType(ancestor) == "Library" {
			return true
		}
	}
	return false
}

func findChildFB(doc *oemTextDocument, root *oemTextEntity, name string) *oemTextEntity {
	for _, entity := range subtreeEntities(root) {
		if doc.entityType(entity) == "FunctionBlock" && strings.EqualFold(doc.attr(entity, "name"), name) {
			return entity
		}
	}
	return nil
}

func (m *cloudMigration) load() error {
	oemPath, err := findOEM(m.options.ProjectRoot)
	if err != nil {
		return err
	}
	m.oemPath, m.dir = oemPath, filepath.Dir(oemPath)
	if m.doc, err = loadOEMText(oemPath); err != nil {
		return err
	}
	var library *oemTextEntity
	for _, candidate := range m.doc.topLevelLibraries() {
		if findChildFB(m.doc, candidate, "fbCloudConnect") != nil {
			if !m.readOnly {
				return fmt.Errorf("the project still references %s with fbCloudConnect; run sysmac_library_replace first", m.doc.attr(candidate, "name"))
			}
			library = candidate
		}
		if findChildFB(m.doc, candidate, "fbConnectionManager") != nil {
			library = candidate
		}
	}
	m.sigs, m.types = map[string][]fbParam{}, map[string]*libType{}
	if library == nil && !m.readOnly {
		return errors.New("no referenced library with fbConnectionManager (NX-IIOT_OptiCloud_V2) found; run sysmac_library_replace first")
	}
	var libraryEntities []*oemTextEntity
	if library != nil {
		m.result.Library = m.doc.attr(library, "name")
		libraryEntities = subtreeEntities(library)
	}
	for _, entity := range libraryEntities {
		switch m.doc.entityType(entity) {
		case "FunctionBlock", "Function":
			for _, child := range entity.children {
				if m.doc.entityType(child) != "Variables" {
					continue
				}
				params, err := m.loadSignature(child)
				if err != nil {
					return fmt.Errorf("signature of %s: %w", m.doc.attr(entity, "name"), err)
				}
				m.sigs[strings.ToLower(m.doc.attr(entity, "name"))] = params
			}
		case "DataType":
			if err := m.loadTypes(entity); err != nil {
				return err
			}
		}
	}
	for _, entity := range m.doc.order {
		if inLibrary(m.doc, entity) {
			continue
		}
		switch m.doc.entityType(entity) {
		case "Program", "FunctionBlock", "Function":
			pou, err := m.loadPOU(entity)
			if err != nil {
				return err
			}
			m.pous = append(m.pous, pou)
		case "Variables":
			if m.doc.attr(entity, "subtype") == "Global" {
				vars, err := m.loadVars(entity, m.doc.attr(entity, "name"))
				if err != nil {
					return err
				}
				if vars != nil {
					m.globals = append(m.globals, vars)
				}
			}
		}
	}
	return nil
}

func (m *cloudMigration) loadSignature(entity *oemTextEntity) ([]fbParam, error) {
	data, err := os.ReadFile(m.payloadPath(entity))
	if err != nil {
		return nil, nil
	}
	data = bytes.TrimPrefix(data, []byte(utf8BOM))
	if bytes.Contains(data, []byte("<VariableGroup")) {
		if data, err = ConvertVariablesXMLToSLWD(data); err != nil {
			return nil, err
		}
	}
	doc, err := ParseSLWDDocument(data)
	if err != nil {
		return nil, err
	}
	var params []fbParam
	for _, variable := range doc.Variables() {
		if !slwdParameterGroup(variable.Group) {
			continue
		}
		order, _ := strconv.Atoi(variable.Attributes["Ord"])
		params = append(params, fbParam{Name: variable.Name, Group: variable.Group, Type: variable.Type, Edge: variable.Attributes["E"], Ord: order})
	}
	sort.SliceStable(params, func(a, b int) bool { return params[a].Ord < params[b].Ord })
	return params, nil
}

type xmlLibType struct {
	Name     string       `xml:"Name,attr"`
	BaseType string       `xml:"BaseType,attr"`
	Children []xmlLibType `xml:"DataType"`
}

func (m *cloudMigration) loadTypes(entity *oemTextEntity) error {
	data, err := os.ReadFile(m.payloadPath(entity))
	if err != nil {
		return nil
	}
	var root struct {
		Types []xmlLibType `xml:"DataType"`
	}
	if err := xml.Unmarshal(bytes.TrimPrefix(data, []byte(utf8BOM)), &root); err != nil {
		return fmt.Errorf("library data types %s: %w", entity.id, err)
	}
	namespace := m.doc.attr(entity, "namespace")
	var convert func(source xmlLibType) *libType
	convert = func(source xmlLibType) *libType {
		result := &libType{Name: source.Name, Base: source.BaseType}
		for _, child := range source.Children {
			result.Members = append(result.Members, convert(child))
		}
		return result
	}
	for _, group := range root.Types {
		for _, definition := range group.Children {
			key := strings.ToLower(definition.Name)
			if _, exists := m.types[key]; exists && namespace != cloudNamespace {
				continue
			}
			m.types[key] = convert(definition)
		}
	}
	return nil
}

func (m *cloudMigration) loadVars(entity *oemTextEntity, label string) (*cloudVars, error) {
	data, err := os.ReadFile(m.payloadPath(entity))
	if err != nil {
		return nil, nil
	}
	if !bytes.HasPrefix(bytes.TrimPrefix(data, []byte(utf8BOM)), []byte("[SLWD")) {
		return nil, nil
	}
	doc, err := ParseSLWDDocument(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return &cloudVars{entity: entity, label: label, doc: doc}, nil
}

func (m *cloudMigration) loadPOU(entity *oemTextEntity) (*cloudPOU, error) {
	pou := &cloudPOU{entity: entity, name: m.doc.attr(entity, "name")}
	for _, child := range entity.children {
		switch m.doc.entityType(child) {
		case "Variables":
			vars, err := m.loadVars(child, pou.name+"/Variables")
			if err != nil {
				return nil, err
			}
			pou.vars = vars
		case "PouBody":
			body := &cloudBody{entity: child, label: pou.name + "/" + m.doc.attr(child, "name"), shifts: map[int][]func(int) int{}}
			for _, grandchild := range child.children {
				if m.doc.entityType(grandchild) == "PouBodySourceHolder" {
					body.pbsh = grandchild
				}
			}
			data, err := os.ReadFile(m.payloadPath(child))
			if err != nil {
				pou.bodies = append(pou.bodies, body)
				continue
			}
			if m.doc.attr(child, "subtype") == "Ladder" {
				ladder, err := parseLadderBody(data)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", body.label, err)
				}
				body.ladder = ladder
			} else {
				body.text = string(data)
			}
			pou.bodies = append(pou.bodies, body)
		}
	}
	return pou, nil
}

// ---- type name helpers ----

var typeNamePattern = regexp.MustCompile(`(?i)^(ARRAY\s*\[[^\]]*\]\s+OF\s+)?((?:[A-Za-z0-9_]+\\)*)([A-Za-z0-9_]+)$`)

// splitTypeName returns the array prefix, namespace prefix and short name.
func splitTypeName(typ string) (string, string, string, bool) {
	match := typeNamePattern.FindStringSubmatch(strings.TrimSpace(typ))
	if match == nil {
		return "", "", "", false
	}
	return match[1], match[2], match[3], true
}

func isCloudType(typ, name string) bool {
	_, namespace, short, ok := splitTypeName(typ)
	return ok && strings.EqualFold(short, name) && (namespace == "" || strings.EqualFold(strings.TrimSuffix(namespace, `\`), cloudNamespace))
}

func isCloudScalarType(typ, name string) bool {
	array, _, _, _ := splitTypeName(typ)
	return array == "" && isCloudType(typ, name)
}

func renameCloudType(typ, from, to string) (string, bool) {
	array, namespace, short, ok := splitTypeName(typ)
	if !ok || !strings.EqualFold(short, from) || !(namespace == "" || strings.EqualFold(strings.TrimSuffix(namespace, `\`), cloudNamespace)) {
		return typ, false
	}
	return array + namespace + to, true
}

func (m *cloudMigration) lookupType(typ string) *libType {
	_, _, short, ok := splitTypeName(typ)
	if !ok {
		return nil
	}
	return m.types[strings.ToLower(short)]
}

var arrayBoundsPattern = regexp.MustCompile(`(?i)^ARRAY\s*\[\s*(-?\d+)\s*\.\.\s*(-?\d+)\s*\]\s+OF\s+(.+)$`)

// defaultLiteral returns the zero initial value of a type as Sysmac writes it.
func (m *cloudMigration) defaultLiteral(typ string) (*stLiteral, error) {
	typ = strings.TrimSpace(typ)
	if match := arrayBoundsPattern.FindStringSubmatch(typ); match != nil {
		low, _ := strconv.Atoi(match[1])
		high, _ := strconv.Atoi(match[2])
		element, err := m.defaultLiteral(match[3])
		if err != nil {
			return nil, err
		}
		return &stLiteral{kind: 'a', items: []stItem{{count: high - low + 1, value: element}}}, nil
	}
	upper := strings.ToUpper(typ)
	switch {
	case upper == "BOOL":
		return stScalar("False"), nil
	case upper == "REAL" || upper == "LREAL":
		return stScalar("0.0"), nil
	case strings.HasPrefix(upper, "STRING"):
		return stScalar("''"), nil
	case upper == "TIME":
		return stScalar("T#0S"), nil
	case upper == "DATE_AND_TIME" || upper == "DT":
		return stScalar("DT#1970-01-01-00:00:00"), nil
	case upper == "DATE" || upper == "D":
		return stScalar("D#1970-01-01"), nil
	case upper == "TIME_OF_DAY" || upper == "TOD":
		return stScalar("TOD#00:00:00"), nil
	}
	switch upper {
	case "SINT", "INT", "DINT", "LINT", "USINT", "UINT", "UDINT", "ULINT", "BYTE", "WORD", "DWORD", "LWORD":
		return stScalar("0"), nil
	}
	definition := m.lookupType(typ)
	if definition == nil {
		return nil, fmt.Errorf("unknown data type %s", typ)
	}
	switch strings.ToUpper(definition.Base) {
	case "ENUM":
		if len(definition.Members) == 0 {
			return nil, fmt.Errorf("enum %s has no values", typ)
		}
		return stScalar(definition.Members[0].Name), nil
	case "STRUCT":
		record := &stLiteral{kind: 'r'}
		for _, member := range definition.Members {
			value, err := m.defaultLiteral(member.Base)
			if err != nil {
				return nil, err
			}
			record.fields = append(record.fields, stField{name: member.Name, value: value})
		}
		return record, nil
	}
	return m.defaultLiteral(definition.Base)
}

// convertLiteral converts an old value to typ: struct members are matched by
// name (missing members get their default), arrays element by element.
func (m *cloudMigration) convertLiteral(old *stLiteral, typ, path string) (*stLiteral, error) {
	if old == nil {
		return m.defaultLiteral(typ)
	}
	if match := arrayBoundsPattern.FindStringSubmatch(strings.TrimSpace(typ)); match != nil && old.kind == 'a' {
		low, _ := strconv.Atoi(match[1])
		high, _ := strconv.Atoi(match[2])
		elements := old.expand()
		values := make([]*stLiteral, 0, high-low+1)
		for index := 0; index <= high-low; index++ {
			var source *stLiteral
			if index < len(elements) {
				source = elements[index]
			}
			value, err := m.convertLiteral(source, match[3], fmt.Sprintf("%s[%d]", path, index+low))
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		if len(elements) > high-low+1 {
			m.warn("%s: %d array elements dropped (new size %d)", path, len(elements)-(high-low+1), high-low+1)
		}
		return compressArray(values), nil
	}
	definition := m.lookupType(typ)
	if definition != nil && strings.EqualFold(definition.Base, "STRUCT") && old.kind == 'r' {
		record := &stLiteral{kind: 'r'}
		used := map[string]bool{}
		for _, member := range definition.Members {
			source := old.field(member.Name)
			used[strings.ToLower(member.Name)] = source != nil
			value, err := m.convertLiteral(source, member.Base, path+"."+member.Name)
			if err != nil {
				return nil, err
			}
			record.fields = append(record.fields, stField{name: member.Name, value: value})
		}
		for _, field := range old.fields {
			if !used[strings.ToLower(field.name)] {
				if def, err := m.defaultLiteral(m.memberType(typ, field.name)); err != nil || def.format() != field.value.format() {
					m.warn("%s.%s := %s dropped (member does not exist in %s)", path, field.name, field.value.format(), definition.Name)
				}
			}
		}
		return record, nil
	}
	if definition != nil && strings.EqualFold(definition.Base, "ENUM") && old.kind == 's' {
		for _, member := range definition.Members {
			if strings.EqualFold(member.Name, old.scalar) {
				return old, nil
			}
		}
		m.unresolved("%s: value %s is not a member of %s", path, old.scalar, definition.Name)
	}
	return old, nil
}

func (m *cloudMigration) memberType(typ, member string) string {
	if definition := m.lookupType(typ); definition != nil {
		for _, candidate := range definition.Members {
			if strings.EqualFold(candidate.Name, member) {
				return candidate.Base
			}
		}
	}
	return "?"
}

// ---- analysis ----

var oldFBTypes = func() map[string]*cloudFBMapping {
	result := map[string]*cloudFBMapping{}
	for _, mapping := range cloudFBMappings {
		result[strings.ToLower(mapping.OldFB)] = mapping
	}
	return result
}()

func oldFBMapping(typ string) *cloudFBMapping {
	array, _, short, ok := splitTypeName(typ)
	if !ok || array != "" || !isCloudType(typ, short) {
		return nil
	}
	return oldFBTypes[strings.ToLower(short)]
}

func (m *cloudMigration) analyzeInstances() {
	for _, pou := range m.pous {
		if pou.vars == nil {
			continue
		}
		used := map[string]int{}
		for _, variable := range pou.vars.doc.Variables() {
			mapping := oldFBMapping(variable.Type)
			if mapping == nil {
				continue
			}
			if variable.Group != "VAR" {
				m.unresolved("%s: %s (%s) is declared in %s; only VAR instances are migrated", pou.vars.label, variable.Name, mapping.OldFB, variable.Group)
				continue
			}
			used[mapping.Instance]++
			name := mapping.Instance
			if used[mapping.Instance] > 1 {
				name += strconv.Itoa(used[mapping.Instance])
			}
			pou.instances = append(pou.instances, &cloudInstance{OldName: variable.Name, NewName: name, Mapping: mapping})
		}
	}
	// Variables bound to the buffer pin of the tag group FB become sPCSample
	// arrays; the machine id list is taken from the event FB.
	for _, pou := range m.pous {
		for _, body := range pou.bodies {
			if body.ladder == nil {
				continue
			}
			for _, line := range body.ladder.rungs() {
				line.node.walk(func(object *ladderJSON) {
					if object.str("__type") != "FB" {
						return
					}
					short := fbShortName(object.str("Name"))
					for _, pins := range []*ladderJSON{object.get("In"), object.get("Out")} {
						if pins == nil {
							continue
						}
						for _, pin := range pins.items {
							arg := strings.ToLower(pin.str("Arg"))
							variable := strings.TrimSpace(pin.str("Var"))
							switch {
							case strings.EqualFold(short, "fbCloud_TagGroup") && arg == "taggroupbuffer" && isIdentifier(variable):
								m.bufferVars[strings.ToLower(variable)] = true
							case strings.EqualFold(short, "fbCloud_EventGroup") && arg == "machineid_ref" && isIdentifier(variable):
								m.machineRef = variable
							}
						}
					}
				})
			}
		}
	}
}

func fbShortName(name string) string {
	if index := strings.LastIndex(name, `\`); index >= 0 {
		return name[index+1:]
	}
	return name
}

func isIdentifier(text string) bool {
	if text == "" || text[0] >= '0' && text[0] <= '9' {
		return false
	}
	for index := 0; index < len(text); index++ {
		if !isSTIdentChar(text[index]) {
			return false
		}
	}
	return true
}

// ladderVars returns every "Var" string of a rung outside inline ST boxes.
func ladderVars(node *ladderJSON) []string {
	var result []string
	node.walk(func(object *ladderJSON) {
		if object.str("__type") == "IST" {
			return
		}
		if value := object.get("Var"); value != nil && value.kind == 's' {
			result = append(result, object.str("Var"))
		}
	})
	return result
}

func istTexts(node *ladderJSON) []string {
	var result []string
	node.walk(func(object *ladderJSON) {
		if object.str("__type") == "IST" {
			result = append(result, object.str("TXT"))
		}
	})
	return result
}

// identifierUsed reports whether name occurs as an identifier in text.
func identifierUsed(text, name string) bool {
	found := false
	scanIdentifiers(text, func(start, end int, prevDot bool) bool {
		if strings.EqualFold(text[start:end], name) && !prevDot {
			found = true
			return false
		}
		return true
	})
	return found
}

// scanIdentifiers calls fn for each identifier outside string literals.
func scanIdentifiers(text string, fn func(start, end int, prevDot bool) bool) {
	for index := 0; index < len(text); {
		c := text[index]
		if c == '\'' || c == '"' {
			end := index + 1
			for end < len(text) && text[end] != c {
				if text[end] == '$' {
					end++
				}
				end++
			}
			index = end + 1
			continue
		}
		if isSTIdentChar(c) && !(c >= '0' && c <= '9') {
			end := index
			for end < len(text) && isSTIdentChar(text[end]) {
				end++
			}
			prevDot := index > 0 && text[index-1] == '.'
			if !fn(index, end, prevDot) {
				return
			}
			index = end
			continue
		}
		if c >= '0' && c <= '9' {
			for index < len(text) && isSTIdentChar(text[index]) {
				index++
			}
			continue
		}
		index++
	}
}

func (body *cloudBody) usesIdentifier(name string) bool {
	if body.deleted {
		return false
	}
	if body.ladder == nil {
		return identifierUsed(body.text, name)
	}
	for _, line := range body.ladder.rungs() {
		for _, value := range append(ladderVars(line.node), istTexts(line.node)...) {
			if identifierUsed(value, name) {
				return true
			}
		}
	}
	return false
}

// ---- IOalarm ----

var eventCodeRef = regexp.MustCompile(`(?i)\.EventTags\s*\[[^\]]*\]\s*\.\s*EventCode\b`)

func (m *cloudMigration) deleteIOAlarm() {
	for _, pou := range m.pous {
		for _, body := range pou.bodies {
			if body.ladder == nil {
				continue
			}
			usesEventCode := false
			for _, line := range body.ladder.rungs() {
				for _, value := range ladderVars(line.node) {
					if eventCodeRef.MatchString(value) {
						usesEventCode = true
					}
				}
			}
			if !usesEventCode {
				continue
			}
			name := m.doc.attr(body.entity, "name")
			reason := m.ioAlarmBlocker(body)
			// Sections that are kept get their EventCode references migrated
			// to Stop_Cause where that is unambiguous (see eventCodeUsage);
			// the rest is reported by scanLeftovers.
			switch {
			case !strings.EqualFold(name, "IOalarm"):
			case reason != "":
				m.warn("%s uses EventTags[].EventCode but also %s; section kept", body.label, reason)
			case m.options.IOAlarm == "keep":
				m.warn("%s only feeds EventTags[].EventCode/Trig; kept because ioalarm=keep", body.label)
			default:
				body.deleted = true
				m.change(body.label, "section deleted (it only fed EventTags[].EventCode/Trig, which V2 replaces by Event_Type/Stop_Cause)")
			}
		}
	}
}

// ioAlarmBlocker returns why a section does more than feeding event tags.
func (m *cloudMigration) ioAlarmBlocker(body *cloudBody) string {
	reason := ""
	for index, line := range body.ladder.rungs() {
		line.node.walk(func(object *ladderJSON) {
			if reason != "" {
				return
			}
			kind := object.str("__type")
			switch kind {
			case "", "PF", "PRM", "LD":
				// Contacts and pins only read; writes are checked below.
			case "ST":
				if !isEventTagRef(object.str("Var")) {
					reason = fmt.Sprintf("a coil on %s (rung %d)", object.str("Var"), index)
				}
			case "F":
				if !strings.EqualFold(object.str("Name"), "MOVE") {
					reason = fmt.Sprintf("function %s (rung %d)", object.str("Name"), index)
					return
				}
				if out := object.get("Out"); out != nil {
					for _, pin := range out.items {
						if pin.str("__type") == "PRM" && pin.str("Var") != "" && !isEventTagRef(pin.str("Var")) {
							reason = fmt.Sprintf("a MOVE to %s (rung %d)", pin.str("Var"), index)
						}
					}
				}
			default:
				reason = fmt.Sprintf("a %s element (rung %d)", kind, index)
			}
		})
	}
	return reason
}

var eventTagRefPattern = regexp.MustCompile(`(?i)^[A-Za-z_][A-Za-z0-9_]*\.EventTags\s*\[\s*\d+\s*\]\.(EventCode|Trig|Value|SampleTime_Offset)$`)

func isEventTagRef(value string) bool {
	return eventTagRefPattern.MatchString(strings.TrimSpace(value))
}

// ---- event group ----

var eventIndexPattern = regexp.MustCompile(`(?i)^\s*\[\s*(\d+)\s*\]`)

func (m *cloudMigration) allVars() []*cloudVars {
	result := append([]*cloudVars{}, m.globals...)
	for _, pou := range m.pous {
		if pou.vars != nil {
			result = append(result, pou.vars)
		}
	}
	return result
}

// eventRefs returns the EventTags indices referenced by code for variable;
// sections marked for deletion count only when includeDeleted is set.
func (m *cloudMigration) eventRefs(variable string, includeDeleted bool) map[int]bool {
	result := map[int]bool{}
	for _, pou := range m.pous {
		for _, body := range pou.bodies {
			if body.deleted && !includeDeleted {
				continue
			}
			var texts []string
			if body.ladder != nil {
				for _, line := range body.ladder.rungs() {
					texts = append(texts, ladderVars(line.node)...)
				}
			} else {
				texts = append(texts, body.text)
			}
			for _, text := range texts {
				scanIdentifiers(text, func(start, end int, prevDot bool) bool {
					if prevDot || !strings.EqualFold(text[start:end], variable) {
						return true
					}
					rest := text[end:]
					if !strings.HasPrefix(strings.ToLower(rest), ".eventtags") {
						return true
					}
					if match := eventIndexPattern.FindStringSubmatch(rest[len(".eventtags"):]); match != nil {
						index, _ := strconv.Atoi(match[1])
						result[index] = true
					}
					return true
				})
			}
		}
	}
	return result
}

func (m *cloudMigration) machineIDsFromRef() ([]int, bool) {
	name := m.machineRef
	if name == "" {
		name = "machineIdRef"
	}
	for _, vars := range m.globals {
		for _, variable := range vars.doc.Variables() {
			if !strings.EqualFold(variable.Name, name) || variable.InitialValue == "" {
				continue
			}
			literal, err := parseSTLiteral(variable.InitialValue)
			if err != nil || literal.kind != 'a' {
				return nil, false
			}
			var ids []int
			for index, element := range literal.expand() {
				if strings.TrimSpace(stString(element)) != "" {
					ids = append(ids, index)
				}
			}
			return ids, true
		}
	}
	return nil, false
}

func (m *cloudMigration) migrateEventGroups() error {
	for _, vars := range m.allVars() {
		for _, variable := range vars.doc.Variables() {
			if !isCloudScalarType(variable.Type, "sEventGroup") || variable.Group == "VAR_EXTERNAL" {
				continue
			}
			if err := m.migrateEventGroup(vars, variable); err != nil {
				return fmt.Errorf("%s %s: %w", vars.label, variable.Name, err)
			}
		}
	}
	return nil
}

type oldEvent struct {
	index int
	value *stLiteral
}

func (m *cloudMigration) migrateEventGroup(vars *cloudVars, variable SLWDVariable) error {
	groupType := m.lookupType(variable.Type)
	if groupType == nil {
		return errors.New("sEventGroup is not defined by the new library")
	}
	tagsType := m.memberType(variable.Type, "EventTags")
	match := arrayBoundsPattern.FindStringSubmatch(tagsType)
	if match == nil {
		return fmt.Errorf("unexpected EventTags type %s", tagsType)
	}
	low, _ := strconv.Atoi(match[1])
	high, _ := strconv.Atoi(match[2])
	eventType := match[3]
	var old *stLiteral
	if variable.InitialValue != "" {
		parsed, err := parseSTLiteral(variable.InitialValue)
		if err != nil {
			return fmt.Errorf("parse initial value: %w", err)
		}
		old = parsed
	}
	for _, element := range old.field("EventTags").expand() {
		if element.field("Event_Type") != nil {
			return nil // already in the V2 layout
		}
	}
	// Old events grouped per machine in order of appearance.
	perMachine := map[int][]oldEvent{}
	var machineOrder []int
	for index, element := range old.field("EventTags").expand() {
		if stString(element.field("Name")) == "" {
			continue
		}
		machine := 0
		if value := element.field("MachineId"); value != nil {
			machine, _ = strconv.Atoi(strings.TrimSpace(value.scalar))
		}
		if _, ok := perMachine[machine]; !ok {
			machineOrder = append(machineOrder, machine)
		}
		perMachine[machine] = append(perMachine[machine], oldEvent{index: index + low, value: element})
	}
	// refs (incl. sections that are deleted) decide the machines as before;
	// used are the tags the remaining code still needs.
	refs := m.eventRefs(variable.Name, true)
	used := m.eventRefs(variable.Name, false)
	istRefs := m.istEventRefs(variable.Name)
	for index := range istRefs {
		used[index] = true
	}
	usage := m.eventCodeUsage(variable.Name)
	machines, source := m.options.MachineIDs, "machine_ids argument"
	if len(machines) == 0 {
		set := map[int]bool{}
		fromCode := false
		ids, ok := m.machineIDsFromRef()
		if ok {
			for _, id := range ids {
				set[id] = true
			}
			source = "machineIdRef entries"
		}
		for machine, events := range perMachine {
			for _, event := range events {
				if refs[event.index] && !set[machine] {
					set[machine] = true
					fromCode = true
				}
			}
		}
		if fromCode {
			source += " + event tags used in code"
		}
		if len(set) == 0 {
			for _, machine := range machineOrder {
				set[machine] = true
			}
			source = "old event tags (machineIdRef and code references not found)"
		}
		for machine := range set {
			machines = append(machines, machine)
		}
		sort.Ints(machines)
	}
	if 3*len(machines) > high-low+1 {
		return fmt.Errorf("%d machines need %d event tags, EventTags holds %d", len(machines), 3*len(machines), high-low+1)
	}
	m.result.Machines, m.result.MachineSource = machines, source

	remap := &cloudEventRemap{variable: variable.Name, index: map[int]int{}, stopCause: map[int]bool{}}
	newTypes := map[int]string{}
	var tags []*stLiteral
	build := func(machine int, eventName string, source *oldEvent) error {
		newIndex := low + len(tags)
		values := map[string]*stLiteral{
			"name":       stScalar(stQuote("state")),
			"event_type": stScalar(eventName),
			"stop_cause": stScalar("''"),
			"machineid":  stScalar(strconv.Itoa(machine)),
		}
		if source != nil {
			remap.index[source.index] = newIndex
			for _, field := range source.value.fields {
				key := strings.ToLower(field.name)
				if key == "name" || key == "value" || key == "trig" || key == "sampletime_offset" {
					values[key] = field.value
				}
			}
			if code := stString(source.value.field("EventCode")); eventName == "Stop_Registered" && code != "" {
				values["stop_cause"] = stScalar(stQuote(code))
			}
		}
		tag, err := m.recordWith(eventType, values)
		if err != nil {
			return err
		}
		tags = append(tags, tag)
		newTypes[newIndex] = eventName
		return nil
	}
	type extraEvent struct {
		machine int
		event   oldEvent
	}
	var extras []extraEvent
	for _, machine := range machines {
		events := perMachine[machine]
		taken := make([]bool, len(events))
		pick := func(kind int) *oldEvent {
			for index, event := range events {
				if taken[index] {
					continue
				}
				if eventTypeForCode(stString(event.value.field("EventCode"))) == cloudEventTypes[kind] {
					taken[index] = true
					return &events[index]
				}
			}
			return nil
		}
		for kind, eventName := range cloudEventTypes {
			if err := build(machine, eventName, pick(kind)); err != nil {
				return err
			}
		}
		for index, event := range events {
			switch {
			case taken[index]:
			case used[event.index]:
				extras = append(extras, extraEvent{machine, event})
			default:
				m.warn("%s: EventTags[%d] (machine %d, EventCode %s) is not used in code and has no V2 counterpart; dropped", variable.Name, event.index, machine, stQuote(stString(event.value.field("EventCode"))))
			}
		}
	}
	listed := map[int]bool{}
	for _, machine := range machines {
		listed[machine] = true
	}
	for _, machine := range machineOrder {
		if listed[machine] {
			continue
		}
		for _, event := range perMachine[machine] {
			if used[event.index] {
				continue // reported as used without an event
			}
			m.warn("%s: EventTags[%d] (machine %d, EventCode %s) dropped: machine %d is not in the machine list %v and the tag is not used in code", variable.Name, event.index, machine, stQuote(stString(event.value.field("EventCode"))), machine, machines)
		}
	}
	// Event tags the code uses beyond the RUN/Stop_UnRegistered/
	// Stop_Registered triple are kept as extra events after all triples.
	for _, item := range extras {
		if len(tags) >= high-low+1 {
			return fmt.Errorf("EventTags holds %d events; %d machine triple(s) plus %d event tag(s) used in code do not fit", high-low+1, len(machines), len(extras))
		}
		code := stString(item.event.value.field("EventCode"))
		eventName := eventTypeForCode(code)
		newIndex := low + len(tags)
		if err := build(item.machine, eventName, &item.event); err != nil {
			return err
		}
		m.change(variable.Name, "EventTags[%d] (machine %d, EventCode %s) is used in code: kept as extra %s event EventTags[%d]", item.event.index, item.machine, stQuote(code), eventName, newIndex)
		if use := usage[item.event.index]; code == "" && (use == nil || len(use.literals) == 0) {
			m.unresolved("%s.EventTags[%d] (machine %d) has no EventCode and the code never sets one; kept as Stop_Registered with an empty Stop_Cause, check the intended event type", variable.Name, item.event.index, item.machine)
		}
	}
	filler, err := m.defaultLiteral(eventType)
	if err != nil {
		return err
	}
	noOfEvent := len(tags)
	for len(tags) < high-low+1 {
		tags = append(tags, filler)
	}
	// EventCode references become Stop_Cause where the target is a
	// Stop_Registered event and every write is a stop cause literal.
	var codeIndices []int
	for index := range usage {
		codeIndices = append(codeIndices, index)
	}
	sort.Ints(codeIndices)
	for _, index := range codeIndices {
		use := usage[index]
		if index < 0 {
			m.unresolved("%s.EventTags[<variable index>].EventCode is used in code (%s); not migrated automatically (V2: Event_Type/Stop_Cause)", variable.Name, strings.Join(use.unsafe, "; "))
			continue
		}
		target, ok := remap.index[index]
		switch {
		case !ok:
			// reported below as a used tag without an event
		case newTypes[target] != "Stop_Registered":
			m.unresolved("%s.EventTags[%d].EventCode is used in code, but the tag becomes a %s event, which has no stop cause; adapt manually", variable.Name, index, newTypes[target])
		case len(use.unsafe) > 0:
			m.unresolved("%s.EventTags[%d].EventCode: %s; not migrated automatically (V2: Stop_Cause of the Stop_Registered event EventTags[%d])", variable.Name, index, strings.Join(use.unsafe, "; "), target)
		default:
			remap.stopCause[index] = true
			m.change(variable.Name, "EventTags[%d].EventCode → EventTags[%d].Stop_Cause (Stop_Registered event; %d read(s), %d write(s) of stop cause literals %s)", index, target, use.reads, len(use.literals), strings.Join(uniqueQuoted(use.literals), ", "))
		}
	}
	var usedIndices []int
	for index := range used {
		usedIndices = append(usedIndices, index)
	}
	sort.Ints(usedIndices)
	for _, index := range usedIndices {
		target, ok := remap.index[index]
		switch {
		case !ok:
			m.unresolved("%s.EventTags[%d] is used in code but has no event in the new layout (machines %v)", variable.Name, index, machines)
		case target != index:
			m.change(variable.Name, "EventTags[%d] moves to EventTags[%d]; code references are renumbered", index, target)
			if istRefs[index] {
				m.unresolved("%s.EventTags[%d] moves to EventTags[%d], but inline ST references it; renumber the inline ST by hand", variable.Name, index, target)
			}
		}
	}
	m.eventMaps = append(m.eventMaps, remap)

	retry := old.field("NoOf_Retry_before_cancel_message")
	if stIsZero(retry) {
		retry = stScalar("10")
	}
	wait := old.field("WaitTime_before_retry")
	if stIsZero(wait) {
		wait = stScalar("T#60S")
	}
	groupName := old.field("Name")
	if stString(groupName) != "MachineState" {
		m.change(variable.Name, "Name %s → 'MachineState'", stQuote(stString(groupName)))
	}
	literal, err := m.recordWith(variable.Type, map[string]*stLiteral{
		"name":                             stScalar("'MachineState'"),
		"noof_event":                       stScalar(strconv.Itoa(noOfEvent)),
		"eventtags":                        compressArray(tags),
		"noof_retry_before_cancel_message": retry,
		"waittime_before_retry":            wait,
	})
	if err != nil {
		return err
	}
	value := literal.format()
	if value != variable.InitialValue {
		if err := vars.doc.PatchVariable(SLWDVariablePatch{Name: variable.Name, InitialValue: &value}); err != nil {
			return err
		}
		vars.changed = true
		extra := ""
		if len(extras) > 0 {
			extra = fmt.Sprintf(" + %d extra event(s) used in code", len(extras))
		}
		m.change(vars.label+"/"+variable.Name, "initial value: %d machine(s) %v × RUN/Stop_UnRegistered/Stop_Registered%s, NoOf_Event %d, retry %s / %s (machines from %s)", len(machines), machines, extra, noOfEvent, retry.format(), wait.format(), m.result.MachineSource)
	}
	return nil
}

// eventTypeForCode maps a Cloud_FB EventCode to the V2 event type: Runtime
// and Downtime are machine states, any other code is a registered stop
// whose cause is the code.
func eventTypeForCode(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "runtime":
		return "RUN"
	case "downtime":
		return "Stop_UnRegistered"
	}
	return "Stop_Registered"
}

func uniqueQuoted(values []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, stQuote(value))
		}
	}
	return result
}

// eventCodeUse describes how code uses EventTags[n].EventCode.
type eventCodeUse struct {
	reads    int
	literals []string // string literals moved into EventCode
	unsafe   []string // uses that cannot be mapped to Stop_Cause safely
}

func eventCodeRefPattern(variable string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_.])` + regexp.QuoteMeta(variable) + `\s*\.\s*EventTags\s*\[\s*([^\]]*?)\s*\]\s*\.\s*EventCode\b`)
}

// eventCodeUsage classifies every EventCode reference of variable in the
// sections that are kept, by old EventTags index (-1: non-constant index).
func (m *cloudMigration) eventCodeUsage(variable string) map[int]*eventCodeUse {
	pattern := eventCodeRefPattern(variable)
	result := map[int]*eventCodeUse{}
	get := func(raw string) *eventCodeUse {
		index, err := strconv.Atoi(raw)
		if err != nil {
			index = -1
		}
		if result[index] == nil {
			result[index] = &eventCodeUse{}
		}
		return result[index]
	}
	each := func(text string, fn func(use *eventCodeUse)) {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			fn(get(match[1]))
		}
	}
	for _, pou := range m.pous {
		for _, body := range pou.bodies {
			if body.deleted {
				continue
			}
			if body.ladder == nil {
				each(body.text, func(use *eventCodeUse) {
					use.unsafe = append(use.unsafe, body.label+" (structured text) uses it")
				})
				continue
			}
			for rung, line := range body.ladder.rungs() {
				where := fmt.Sprintf("%s rung %d", body.label, rung)
				line.node.walk(func(object *ladderJSON) {
					switch kind := object.str("__type"); kind {
					case "IST":
						each(object.str("TXT"), func(use *eventCodeUse) {
							use.unsafe = append(use.unsafe, where+": inline ST uses it")
						})
					case "F", "FB":
						literal, isMove := "", kind == "F" && strings.EqualFold(object.str("Name"), "MOVE")
						if in := object.get("In"); in != nil {
							for _, pin := range in.items {
								if strings.EqualFold(pin.str("Arg"), "In") {
									literal = strings.TrimSpace(pin.str("Var"))
								}
								each(pin.str("Var"), func(use *eventCodeUse) { use.reads++ })
							}
						}
						if out := object.get("Out"); out != nil {
							for _, pin := range out.items {
								each(pin.str("Var"), func(use *eventCodeUse) {
									switch {
									case !isMove:
										use.unsafe = append(use.unsafe, fmt.Sprintf("%s: written by %s", where, object.str("Name")))
									case !strings.HasPrefix(literal, "'") || !strings.HasSuffix(literal, "'") || len(literal) < 2:
										use.unsafe = append(use.unsafe, fmt.Sprintf("%s: MOVE from %s (not a string literal)", where, literal))
									case eventTypeForCode(stString(stScalar(literal))) != "Stop_Registered":
										use.unsafe = append(use.unsafe, fmt.Sprintf("%s: MOVE %s (an event type, not a stop cause)", where, literal))
									default:
										use.literals = append(use.literals, stString(stScalar(literal)))
									}
								})
							}
						}
						if value := object.get("Var"); value != nil && value.kind == 's' {
							each(object.str("Var"), func(use *eventCodeUse) { use.reads++ })
						}
					case "PRM", "PF":
						// handled with their F/FB box
					default:
						if value := object.get("Var"); value != nil && value.kind == 's' {
							each(object.str("Var"), func(use *eventCodeUse) {
								if kind == "ST" {
									use.unsafe = append(use.unsafe, where+": coil on it")
								} else {
									use.reads++
								}
							})
						}
					}
				})
			}
		}
	}
	return result
}

// istEventRefs returns the EventTags indices of variable referenced in
// inline ST boxes of the sections that are kept.
func (m *cloudMigration) istEventRefs(variable string) map[int]bool {
	pattern := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_.])` + regexp.QuoteMeta(variable) + `\s*\.\s*EventTags\s*\[\s*(\d+)\s*\]`)
	result := map[int]bool{}
	for _, pou := range m.pous {
		for _, body := range pou.bodies {
			if body.deleted || body.ladder == nil {
				continue
			}
			for _, line := range body.ladder.rungs() {
				for _, text := range istTexts(line.node) {
					for _, match := range pattern.FindAllStringSubmatch(text, -1) {
						index, _ := strconv.Atoi(match[1])
						result[index] = true
					}
				}
			}
		}
	}
	return result
}

// recordWith builds a struct literal of typ with the given member values
// (keys lower-case); other members get their default value.
func (m *cloudMigration) recordWith(typ string, values map[string]*stLiteral) (*stLiteral, error) {
	definition := m.lookupType(typ)
	if definition == nil || !strings.EqualFold(definition.Base, "STRUCT") {
		return nil, fmt.Errorf("%s is not a struct of the new library", typ)
	}
	record := &stLiteral{kind: 'r'}
	for _, member := range definition.Members {
		value := values[strings.ToLower(member.Name)]
		if value == nil {
			var err error
			if value, err = m.defaultLiteral(member.Base); err != nil {
				return nil, err
			}
		}
		record.fields = append(record.fields, stField{name: member.Name, value: value})
	}
	return record, nil
}

// ---- ladder ----

// varType returns the declared type of name as seen from pou (local or
// external declaration first, then the global variables), "" if unknown.
func (m *cloudMigration) varType(pou *cloudPOU, name string) string {
	if m.scopes == nil {
		m.scopes = map[*cloudPOU]map[string]string{}
	}
	scope, ok := m.scopes[pou]
	if !ok {
		scope = map[string]string{}
		for _, vars := range m.globals {
			for _, variable := range vars.doc.Variables() {
				scope[strings.ToLower(variable.Name)] = variable.Type
			}
		}
		if pou != nil && pou.vars != nil {
			for _, variable := range pou.vars.doc.Variables() {
				scope[strings.ToLower(variable.Name)] = variable.Type
			}
		}
		m.scopes[pou] = scope
	}
	return scope[strings.ToLower(name)]
}

// renameRefs renames old FB instances (and their members) and renumbers
// event tag indices in a variable reference.
func (m *cloudMigration) renameRefs(pou *cloudPOU, text string) string {
	type edit struct {
		start, end int
		value      string
	}
	var edits []edit
	scanIdentifiers(text, func(start, end int, prevDot bool) bool {
		if prevDot {
			return true
		}
		word := text[start:end]
		if typ := m.varType(pou, word); typ != "" && end < len(text) && text[end] == '.' {
			memberEnd := end + 1
			for memberEnd < len(text) && isSTIdentChar(text[memberEnd]) {
				memberEnd++
			}
			for _, rename := range cloudMemberRenames {
				if isCloudScalarType(typ, rename.Type) && strings.EqualFold(text[end+1:memberEnd], rename.Old) {
					edits = append(edits, edit{end + 1, memberEnd, rename.New})
				}
			}
		}
		for _, instance := range pou.instances {
			if !strings.EqualFold(word, instance.OldName) {
				continue
			}
			edits = append(edits, edit{start, end, instance.NewName})
			if end < len(text) && text[end] == '.' {
				memberEnd := end + 1
				for memberEnd < len(text) && isSTIdentChar(text[memberEnd]) {
					memberEnd++
				}
				if renamed, ok := instance.Mapping.Pins[strings.ToLower(text[end+1:memberEnd])]; ok {
					edits = append(edits, edit{end + 1, memberEnd, renamed})
				}
			}
			return true
		}
		for _, remap := range m.eventMaps {
			if !strings.EqualFold(word, remap.variable) || !strings.HasPrefix(strings.ToLower(text[end:]), ".eventtags") {
				continue
			}
			offset := end + len(".eventtags")
			if match := eventIndexPattern.FindStringSubmatchIndex(text[offset:]); match != nil {
				index, _ := strconv.Atoi(text[offset+match[2] : offset+match[3]])
				if target, ok := remap.index[index]; ok && target != index {
					edits = append(edits, edit{offset + match[2], offset + match[3], strconv.Itoa(target)})
				}
				if remap.stopCause[index] {
					rest := offset + match[1]
					if member := eventCodeMemberPattern.FindStringSubmatchIndex(text[rest:]); member != nil {
						edits = append(edits, edit{rest + member[2], rest + member[3], "Stop_Cause"})
					}
				}
			}
		}
		return true
	})
	for index := len(edits) - 1; index >= 0; index-- {
		text = text[:edits[index].start] + edits[index].value + text[edits[index].end:]
	}
	return text
}

func (m *cloudMigration) migrateLadder() error {
	for _, pou := range m.pous {
		for _, body := range pou.bodies {
			if body.ladder == nil || body.deleted {
				continue
			}
			for index, line := range body.ladder.rungs() {
				before := line.node.render()
				if err := m.migrateRung(pou, body, index, line.node); err != nil {
					return fmt.Errorf("%s rung %d: %w", body.label, index, err)
				}
				for _, text := range istTexts(line.node) {
					for _, instance := range pou.instances {
						if identifierUsed(text, instance.OldName) {
							m.unresolved("%s rung %d: inline ST references %s; inline ST is not changed automatically, rename it to %s by hand", body.label, index, instance.OldName, instance.NewName)
						}
					}
				}
				if line.node.render() != before {
					body.changed = true
				}
			}
		}
	}
	return nil
}

func (m *cloudMigration) migrateRung(pou *cloudPOU, body *cloudBody, rung int, node *ladderJSON) error {
	var fbs []*ladderJSON
	node.walk(func(object *ladderJSON) {
		if object.str("__type") == "FB" {
			fbs = append(fbs, object)
		}
	})
	for _, fb := range fbs {
		name := fb.str("Name")
		short := fbShortName(name)
		mapping := oldFBTypes[strings.ToLower(short)]
		target := short
		if mapping != nil {
			target = mapping.NewFB
		}
		params, ok := m.sigs[strings.ToLower(target)]
		if !ok || !strings.Contains(strings.ToLower(name), strings.ToLower(cloudNamespace)) {
			if mapping != nil {
				return fmt.Errorf("%s not found in library %s", target, m.result.Library)
			}
			continue
		}
		var pins map[string]string
		if mapping != nil {
			pins = mapping.Pins
			fb.set("Name", ladderString(name[:len(name)-len(short)]+target))
		}
		for _, binding := range droppedBindings(fb, params, pins) {
			m.warn("%s rung %d: %s has no parameter %s; its binding to %s is removed (check that the function is not needed)", body.label, rung, target, binding[0], binding[1])
		}
		description, shift, err := resyncFBPins(node, fb, params, pins)
		if err != nil {
			return fmt.Errorf("%s: %w", short, err)
		}
		if shift != nil {
			body.shifts[rung] = append(body.shifts[rung], shift)
		}
		label := fmt.Sprintf("%s rung %d", body.label, rung)
		if mapping != nil {
			instance := fb.str("Var")
			newInstance := m.renameRefs(pou, instance)
			m.change(label, "%s → %s, instance %s → %s%s", short, target, instance, newInstance, description)
		} else if description != "" {
			m.change(label, "%s %s pins synchronised with library%s", short, fb.str("Var"), description)
		}
	}
	// Rename instance and member references and renumber event tags.
	node.walk(func(object *ladderJSON) {
		if object.str("__type") == "IST" {
			return
		}
		value := object.get("Var")
		if value == nil || value.kind != 's' {
			return
		}
		old := object.str("Var")
		if renamed := m.renameRefs(pou, old); renamed != old {
			object.set("Var", ladderString(renamed))
			if object.str("__type") != "FB" {
				m.change(fmt.Sprintf("%s rung %d", body.label, rung), "%s → %s", old, renamed)
			}
		}
	})
	return nil
}

// droppedBindings returns [pin, binding] for bound PRM pins of an FB box that
// have no parameter in the new signature.
func droppedBindings(fb *ladderJSON, params []fbParam, renames map[string]string) [][2]string {
	var result [][2]string
	seen := map[string]bool{}
	for _, list := range []*ladderJSON{fb.get("In"), fb.get("Out")} {
		if list == nil {
			continue
		}
		for _, pin := range list.items {
			arg, binding := pin.str("Arg"), strings.TrimSpace(pin.str("Var"))
			if pin.str("__type") != "PRM" || binding == "" || seen[strings.ToLower(arg)] {
				continue
			}
			name := arg
			if renamed, ok := renames[strings.ToLower(arg)]; ok {
				name = renamed
			}
			found := false
			for _, param := range params {
				found = found || strings.EqualFold(param.Name, name)
			}
			if !found {
				seen[strings.ToLower(arg)] = true
				result = append(result, [2]string{arg, binding})
			}
		}
	}
	return result
}

func pinIx(pin *ladderJSON) int {
	value, _ := pin.num("Ix")
	return value
}

func setPinIx(pin *ladderJSON, ix int) {
	if ix == 0 {
		pin.remove("Ix")
		return
	}
	pin.set("Ix", ladderNumber(ix), "Not", "Type", "Up", "Var")
}

// resyncFBPins rebuilds the In/Out pins of an FB box from the library
// signature: old pins are matched by (renamed) name and keep their binding,
// new parameters get empty pins, and the Ix values of the rung are shifted
// when the number of pins changes. It returns a change description and the
// Ix shift (nil when Ix values are unchanged).
func resyncFBPins(rung, fb *ladderJSON, params []fbParam, renames map[string]string) (string, func(int) int, error) {
	inList, outList := fb.get("In"), fb.get("Out")
	if inList == nil || outList == nil {
		return "", nil, errors.New("FB box without In/Out pins")
	}
	var oldPRM []*ladderJSON
	for _, list := range []*ladderJSON{inList, outList} {
		for _, pin := range list.items {
			if pin.str("__type") == "PRM" {
				oldPRM = append(oldPRM, pin)
			}
		}
	}
	low := 0
	if len(oldPRM) > 0 {
		low = pinIx(oldPRM[0])
		for index, pin := range oldPRM {
			if pinIx(pin) != low+index {
				return "", nil, errors.New("pin Ix values are not consecutive; cannot renumber safely")
			}
		}
	}
	high := low + len(oldPRM) - 1
	mapName := func(arg string) string {
		if renamed, ok := renames[strings.ToLower(arg)]; ok {
			return renamed
		}
		return arg
	}
	var renamed, added, retyped, dropped []string
	consumed := map[*ladderJSON]bool{}
	var newPRM []*ladderJSON
	// Inputs and in-outs are listed in declaration order. On the output side
	// an in-out pin sits on the same row as on the input side, or after the
	// last output when the output side is shorter.
	var inParams, outParams []fbParam
	for _, param := range params {
		if param.Group == "VAR_INPUT" || param.Group == "VAR_IN_OUT" {
			inParams = append(inParams, param)
		}
		if param.Group == "VAR_OUTPUT" {
			outParams = append(outParams, param)
		}
	}
	for row, param := range inParams {
		if param.Group != "VAR_IN_OUT" {
			continue
		}
		position := min(row, len(outParams))
		outParams = append(outParams, fbParam{})
		copy(outParams[position+1:], outParams[position:])
		outParams[position] = param
	}
	build := func(list *ladderJSON, input bool) {
		oldPins := list.items
		var items []*ladderJSON
		powerFlowSet := false
		ordered := outParams
		if input {
			ordered = inParams
		}
		for _, param := range ordered {
			io := param.Group == "VAR_IN_OUT"
			wantGroup := "VAR_OUTPUT"
			if input {
				wantGroup = "VAR_INPUT"
			}
			if !powerFlowSet && param.Group == wantGroup && strings.EqualFold(param.Type, "BOOL") {
				powerFlowSet = true
				var pin *ladderJSON
				for _, candidate := range oldPins {
					if candidate.str("__type") == "PF" && !consumed[candidate] {
						pin = candidate
						break
					}
				}
				if pin == nil {
					pin = &ladderJSON{kind: 'o'}
					pin.set("__type", ladderString("PF"))
				} else if old := pin.str("Arg"); !strings.EqualFold(mapName(old), param.Name) {
					renamed = append(renamed, old+"→"+param.Name+" (power flow)")
				}
				consumed[pin] = true
				pin.set("Arg", ladderString(param.Name))
				items = append(items, pin)
				continue
			}
			var pin *ladderJSON
			for _, candidate := range oldPins {
				if candidate.str("__type") == "PRM" && !consumed[candidate] && strings.EqualFold(mapName(candidate.str("Arg")), param.Name) {
					pin = candidate
					break
				}
			}
			if pin == nil {
				pin = &ladderJSON{kind: 'o'}
				pin.set("__type", ladderString("PRM"))
				pin.set("Arg", ladderString(param.Name))
				if io {
					pin.set("IO", ladderBool(true))
				}
				pin.set("Type", ladderString(param.Type))
				if strings.EqualFold(param.Edge, "UpDirection") && input {
					pin.set("Up", ladderBool(true))
				}
				pin.set("Var", ladderString(""))
				if input || !io {
					added = append(added, param.Name)
				}
			} else {
				if old := pin.str("Arg"); old != param.Name {
					pin.set("Arg", ladderString(param.Name))
					if input || !io {
						renamed = append(renamed, old+"→"+param.Name)
					}
				}
				if old := pin.str("Type"); old != param.Type {
					pin.set("Type", ladderString(param.Type))
					if input || !io {
						retyped = append(retyped, param.Name+": "+old+"→"+param.Type)
					}
				}
			}
			consumed[pin] = true
			items = append(items, pin)
			newPRM = append(newPRM, pin)
		}
		for _, pin := range oldPins {
			if !consumed[pin] {
				description := pin.str("Arg")
				if binding := pin.str("Var"); binding != "" {
					description += " (was bound to " + binding + ")"
				}
				dropped = append(dropped, description)
			}
		}
		list.items = items
	}
	build(inList, true)
	build(outList, false)
	for index, pin := range newPRM {
		setPinIx(pin, low+index)
	}
	delta := len(newPRM) - len(oldPRM)
	isNew := map[*ladderJSON]bool{}
	for _, pin := range newPRM {
		isNew[pin] = true
	}
	var shift func(int) int
	if delta != 0 {
		shift = func(value int) int {
			if value > high {
				return value + delta
			}
			return value
		}
		rung.walk(func(object *ladderJSON) {
			if isNew[object] {
				return
			}
			for _, key := range []string{"Ix", "LRI", "RRI"} {
				if value, ok := object.num(key); ok && value > high {
					object.set(key, ladderNumber(value+delta))
				}
			}
		})
	}
	var parts []string
	if len(renamed) > 0 {
		parts = append(parts, "pins renamed "+strings.Join(renamed, ", "))
	}
	if len(added) > 0 {
		parts = append(parts, "new pins "+strings.Join(added, ", ")+" (unconnected)")
	}
	if len(retyped) > 0 {
		parts = append(parts, "pin types "+strings.Join(retyped, ", "))
	}
	if len(dropped) > 0 {
		parts = append(parts, "removed pins "+strings.Join(dropped, ", "))
	}
	if delta != 0 {
		parts = append(parts, fmt.Sprintf("Ix > %d shifted by %+d", high, delta))
	}
	description := ""
	if len(parts) > 0 {
		description = "; " + strings.Join(parts, "; ")
	}
	return description, shift, nil
}

var elementURIPattern = regexp.MustCompile(`/Rung%5B(\d+)%5D/Element%5BN(\d+)%5D`)

func shiftLinkInfo(data []byte, shifts map[int][]func(int) int) []byte {
	return elementURIPattern.ReplaceAllFunc(data, func(match []byte) []byte {
		parts := elementURIPattern.FindSubmatch(match)
		rung, _ := strconv.Atoi(string(parts[1]))
		element, _ := strconv.Atoi(string(parts[2]))
		for _, shift := range shifts[rung] {
			element = shift(element)
		}
		return []byte(fmt.Sprintf("/Rung%%5B%d%%5D/Element%%5BN%d%%5D", rung, element))
	})
}

// ---- variables ----

func (m *cloudMigration) migrateVariables() error {
	for _, pou := range m.pous {
		if pou.vars == nil {
			continue
		}
		vars := pou.vars
		for _, instance := range pou.instances {
			if err := vars.doc.DeleteVariable(instance.OldName); err != nil {
				return err
			}
			m.change(vars.label, "deleted %s (%s)", instance.OldName, instance.Mapping.OldFB)
		}
		for _, variable := range vars.doc.Variables() {
			if variable.Group == "VAR" && isCloudScalarType(variable.Type, "sTagGroupSetting") && variable.InitialValue == "" {
				used := false
				for _, body := range pou.bodies {
					used = used || body.usesIdentifier(variable.Name)
				}
				if !used {
					if err := vars.doc.DeleteVariable(variable.Name); err != nil {
						return err
					}
					m.change(vars.label, "deleted unused %s (sTagGroupSetting)", variable.Name)
				}
			}
		}
		for _, instance := range pou.instances {
			if vars.doc.HasVariable(instance.NewName) {
				return fmt.Errorf("%s already declares %s", vars.label, instance.NewName)
			}
			if err := vars.doc.AddVariable(SLWDVariable{Name: instance.NewName, Type: cloudNamespace + `\` + instance.Mapping.NewFB, Group: "VAR"}); err != nil {
				return err
			}
			m.change(vars.label, "added %s : %s", instance.NewName, instance.Mapping.NewFB)
		}
		if len(pou.instances) > 0 {
			vars.changed = true
		}
	}
	for _, vars := range m.allVars() {
		if err := m.migrateVariableTypes(vars); err != nil {
			return err
		}
	}
	return nil
}

func (m *cloudMigration) migrateVariableTypes(vars *cloudVars) error {
	for _, variable := range vars.doc.Variables() {
		typ := variable.Type
		oldType := typ
		for _, rename := range cloudTypeRenames {
			typ, _ = renameCloudType(typ, rename.Old, rename.New)
		}
		if m.bufferVars[strings.ToLower(variable.Name)] {
			typ, _ = renameCloudType(typ, cloudSampleOld, cloudSampleNew)
		}
		patch := SLWDVariablePatch{Name: variable.Name}
		if typ != oldType {
			patch.Type = &typ
		}
		note := ""
		if variable.Group != "VAR_EXTERNAL" {
			value, description, err := m.convertInitialValue(variable, oldType, typ)
			if err != nil {
				return fmt.Errorf("%s %s: %w", vars.label, variable.Name, err)
			}
			if value != variable.InitialValue {
				patch.InitialValue, note = &value, description
			}
		}
		if patch.Type == nil && patch.InitialValue == nil {
			continue
		}
		if err := vars.doc.PatchVariable(patch); err != nil {
			return err
		}
		vars.changed = true
		if patch.Type != nil {
			m.change(vars.label+"/"+variable.Name, "type %s → %s", oldType, typ)
		}
		if patch.InitialValue != nil {
			m.change(vars.label+"/"+variable.Name, "initial value %s", note)
		}
	}
	return nil
}

// convertInitialValue migrates the initial value of part counter settings
// and part counter data. Other variables keep their value.
func (m *cloudMigration) convertInitialValue(variable SLWDVariable, oldType, newType string) (string, string, error) {
	switch {
	case isCloudScalarType(oldType, "sTagGroupSetting"):
		var old *stLiteral
		if variable.InitialValue != "" {
			parsed, err := parseSTLiteral(variable.InitialValue)
			if err != nil {
				return "", "", fmt.Errorf("parse initial value: %w", err)
			}
			old = parsed
		}
		literal, err := m.convertLiteral(old, newType, variable.Name)
		if err != nil {
			return "", "", err
		}
		var notes []string
		set := func(member string, value *stLiteral, note string) {
			for index, field := range literal.fields {
				if strings.EqualFold(field.name, member) {
					if field.value.format() != value.format() {
						literal.fields[index].value = value
						notes = append(notes, note)
					}
				}
			}
		}
		if old.field("Publish_methode") == nil {
			set("Publish_methode", stScalar("ChangeOf_State"), "Publish_methode := ChangeOf_State")
		}
		if format := old.field("PartCounter_format"); format == nil || !strings.EqualFold(format.scalar, "Sample") {
			set("PartCounter_format", stScalar("Sample"), "PartCounter_format := Sample")
		}
		if stIsZero(old.field("NoOf_Retry_before_cancel_message")) {
			set("NoOf_Retry_before_cancel_message", stScalar("10"), "NoOf_Retry_before_cancel_message := 10")
		}
		if stIsZero(old.field("WaitTime_before_retry")) {
			set("WaitTime_before_retry", stScalar("T#10S"), "WaitTime_before_retry := T#10S")
		}
		m.checkTagTypes(variable.Name, literal)
		// OptiCloud maps counters by position and name in the published tag
		// list: it must survive 1:1, empty slots included.
		if variable.InitialValue != "" {
			oldTags, oldSettings := tagGroupSummary(variable.InitialValue)
			newTags, newSettings := tagGroupSummary(literal.format())
			if strings.Join(oldTags, "|") != strings.Join(newTags, "|") || oldSettings["noof_tag"] != newSettings["noof_tag"] {
				m.unresolved("%s: the part counter tag list changed during conversion (before [%s], after [%s]); OptiCloud maps counters by position and name", variable.Name, strings.Join(oldTags, "; "), strings.Join(newTags, "; "))
			}
		}
		return literal.format(), "converted to sPCTagGroupSetting (" + strings.Join(notes, ", ") + ")", nil
	case isCloudType(oldType, "sTagData") && !isCloudScalarType(oldType, "sTagData"):
		var old *stLiteral
		if variable.InitialValue != "" {
			parsed, err := parseSTLiteral(variable.InitialValue)
			if err != nil {
				return "", "", fmt.Errorf("parse initial value: %w", err)
			}
			old = parsed
		}
		literal, err := m.convertLiteral(old, newType, variable.Name)
		if err != nil {
			return "", "", err
		}
		return literal.format(), "set to the sPCTagData default", nil
	case variable.InitialValue != "" && len(m.memberRenames(oldType)) > 0:
		old, err := parseSTLiteral(variable.InitialValue)
		if err != nil {
			return "", "", fmt.Errorf("parse initial value: %w", err)
		}
		var notes []string
		for index, field := range old.fields {
			for _, rename := range m.memberRenames(oldType) {
				if strings.EqualFold(field.name, rename.Old) {
					old.fields[index].name = rename.New
					notes = append(notes, rename.Old+" → "+rename.New)
				}
			}
		}
		literal, err := m.convertLiteral(old, newType, variable.Name)
		if err != nil {
			return "", "", err
		}
		return literal.format(), "members renamed (" + strings.Join(notes, ", ") + ")", nil
	}
	if definition := m.lookupType(newType); definition != nil && isCloudScalarType(newType, definition.Name) && strings.EqualFold(definition.Base, "ENUM") && variable.InitialValue != "" {
		found := false
		for _, member := range definition.Members {
			found = found || strings.EqualFold(member.Name, strings.TrimSpace(variable.InitialValue))
		}
		if !found {
			m.unresolved("%s: initial value %s is not a member of %s in %s", variable.Name, variable.InitialValue, definition.Name, m.result.Library)
		}
	}
	return variable.InitialValue, "", nil
}

func (m *cloudMigration) memberRenames(typ string) []struct{ Type, Old, New string } {
	var result []struct{ Type, Old, New string }
	for _, rename := range cloudMemberRenames {
		if isCloudScalarType(typ, rename.Type) {
			result = append(result, rename)
		}
	}
	return result
}

func (m *cloudMigration) checkTagTypes(name string, setting *stLiteral) {
	for index, element := range setting.field("TagSetting").expand() {
		if stString(element.field("Name")) == "" {
			continue
		}
		if value := element.field("TagType"); value != nil && strings.HasPrefix(strings.ToLower(value.scalar), "machinedata") {
			m.unresolved("%s.TagSetting[%d]: TagType %s is machine data; the V2 production data service only counts parts (use fbOpti_Telemetry_Service)", name, index, value.scalar)
		}
	}
}

// ---- leftovers ----

var oldCloudNames = []string{"fbCloudConnect", "fbCloud_TagGroup", "fbCloud_EventGroup", "fbCloud_Telemetry", "fbCloud_Batch", "sTagGroupSetting", "sTagData", "ssTagSetting"}

// missingMembers reports "var.member" references to scalar variables of a
// library struct type that has no such member.
func (m *cloudMigration) missingMembers(pou *cloudPOU, text string) []string {
	var problems []string
	scanIdentifiers(text, func(start, end int, prevDot bool) bool {
		if prevDot || end >= len(text) || text[end] != '.' {
			return true
		}
		typ := m.varType(pou, text[start:end])
		array, _, short, ok := splitTypeName(typ)
		if !ok || array != "" || !isCloudType(typ, short) {
			return true
		}
		definition := m.lookupType(typ)
		if definition == nil || !strings.EqualFold(definition.Base, "STRUCT") {
			return true
		}
		memberEnd := end + 1
		for memberEnd < len(text) && isSTIdentChar(text[memberEnd]) {
			memberEnd++
		}
		member := text[end+1 : memberEnd]
		for _, candidate := range definition.Members {
			if strings.EqualFold(candidate.Name, member) {
				return true
			}
		}
		problems = append(problems, fmt.Sprintf("%s is not a member of %s in %s", text[start:memberEnd], definition.Name, m.result.Library))
		return true
	})
	return problems
}

func (m *cloudMigration) scanLeftovers() {
	m.scopes = nil // variable types were migrated
	for _, pou := range m.pous {
		for _, body := range pou.bodies {
			if body.deleted {
				continue
			}
			if body.ladder == nil {
				for _, name := range oldCloudNames {
					if identifierUsed(body.text, name) {
						m.unresolved("%s (structured text) references %s; ST bodies are not migrated automatically", body.label, name)
					}
				}
				for _, instance := range pou.instances {
					if identifierUsed(body.text, instance.OldName) {
						m.unresolved("%s (structured text) references %s; rename it to %s", body.label, instance.OldName, instance.NewName)
					}
				}
				continue
			}
			for index, line := range body.ladder.rungs() {
				for _, value := range ladderVars(line.node) {
					if eventCodeRef.MatchString(value) {
						m.unresolved("%s rung %d still uses %s", body.label, index, value)
					}
					for _, problem := range m.missingMembers(pou, value) {
						m.unresolved("%s rung %d: %s", body.label, index, problem)
					}
					for _, instance := range pou.instances {
						prefix := strings.ToLower(instance.NewName) + "."
						if !strings.HasPrefix(strings.ToLower(value), prefix) {
							continue
						}
						member := value[len(prefix):]
						if end := strings.IndexAny(member, ".[ "); end >= 0 {
							member = member[:end]
						}
						found := false
						for _, param := range m.sigs[strings.ToLower(instance.Mapping.NewFB)] {
							found = found || strings.EqualFold(param.Name, member)
						}
						if !found {
							m.unresolved("%s rung %d: %s has no member %s", body.label, index, instance.Mapping.NewFB, member)
						}
					}
				}
			}
		}
	}
	for _, entity := range m.doc.order {
		if inLibrary(m.doc, entity) || m.doc.entityType(entity) != "DataType" {
			continue
		}
		data, err := os.ReadFile(m.payloadPath(entity))
		if err != nil {
			continue
		}
		for _, name := range []string{"sTagGroupSetting", "sTagData", "ssEvent"} {
			if bytes.Contains(bytes.ToLower(data), []byte(strings.ToLower(`\`+name+`"`))) {
				m.unresolved("user data types reference %s; adapt them manually", name)
			}
		}
	}
}

// ---- write ----

func (m *cloudMigration) write() error {
	files := map[string][]byte{}
	touched := map[string]bool{}
	removed := map[string]bool{}
	for _, vars := range m.allVars() {
		if !vars.changed {
			continue
		}
		data, err := MarshalSLWDDocument(vars.doc)
		if err != nil {
			return err
		}
		files[vars.entity.id+".xml"] = data
		touched[vars.entity.id] = true
	}
	for _, pou := range m.pous {
		for _, body := range pou.bodies {
			switch {
			case body.deleted:
				for _, entity := range subtreeEntities(body.entity) {
					removed[entity.id] = true
				}
			case body.changed:
				files[body.entity.id+".xml"] = body.ladder.marshal()
				touched[body.entity.id] = true
				if body.pbsh != nil && len(body.shifts) > 0 {
					path := m.payloadPath(body.pbsh)
					if data, err := os.ReadFile(path); err == nil {
						if shifted := shiftLinkInfo(data, body.shifts); !bytes.Equal(shifted, data) {
							files[body.pbsh.id+".xml"] = shifted
							touched[body.pbsh.id] = true
						}
					}
				}
			}
		}
	}
	if len(files) == 0 && len(removed) == 0 {
		return nil
	}
	// Index: delete removed sections and stamp modified entities.
	data := m.doc.data
	for id := range removed {
		doc, err := parseOEMText(data)
		if err != nil {
			return err
		}
		entity := doc.entities[id]
		if entity == nil || removed[entityParentID(entity)] {
			continue
		}
		if data, err = doc.removeEntity(entity); err != nil {
			return err
		}
	}
	touched[m.doc.root.id] = true
	stamp := oemTimestamp(m.options.Now)
	for id := range touched {
		doc, err := parseOEMText(data)
		if err != nil {
			return err
		}
		entity := doc.entities[id]
		if entity == nil {
			continue
		}
		tag := string(data[entity.span.start:entity.span.startTagEnd])
		data = splice(data, entity.span.start, entity.span.startTagEnd, []byte(setStartTagAttr(tag, "dateLastModified", stamp)))
	}
	if _, err := parseOEMText(data); err != nil {
		return fmt.Errorf("updated index is invalid: %w", err)
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		base := entry.Name()
		if index := strings.Index(base, "."); !entry.IsDir() && index > 0 && removed[base[:index]] {
			if err := os.Remove(filepath.Join(m.dir, base)); err != nil {
				return err
			}
			m.result.RemovedFiles = append(m.result.RemovedFiles, base)
		}
	}
	files[filepath.Base(m.oemPath)] = data
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(m.dir, name), content, 0600); err != nil {
			return err
		}
		m.result.WrittenFiles = append(m.result.WrittenFiles, name)
	}
	sort.Strings(m.result.WrittenFiles)
	sort.Strings(m.result.RemovedFiles)
	return nil
}

func entityParentID(entity *oemTextEntity) string {
	if entity.parent == nil {
		return ""
	}
	return entity.parent.id
}

// tagGroupSummary lists the published tags of a part counter or telemetry
// group setting ("[i] 'name' machine m type") and its other settings.
func tagGroupSummary(value string) ([]string, map[string]string) {
	literal, err := parseSTLiteral(value)
	if err != nil || value == "" {
		return nil, nil
	}
	settings := map[string]string{}
	for _, field := range literal.fields {
		if !strings.EqualFold(field.name, "TagSetting") {
			settings[strings.ToLower(field.name)] = field.value.format()
		}
	}
	// OptiCloud maps counters by position and name in the published tag
	// list, so every slot up to NoOf_Tag (or the last named tag) counts,
	// empty ones included.
	elements := literal.field("TagSetting").expand()
	count := 0
	if field := literal.field("NoOf_Tag"); field != nil {
		count, _ = strconv.Atoi(strings.TrimSpace(field.scalar))
	}
	for index, element := range elements {
		if stString(element.field("Name")) != "" && index+1 > count {
			count = index + 1
		}
	}
	var tags []string
	for index, element := range elements {
		if index >= count {
			break
		}
		name := stString(element.field("Name"))
		machine, kind := "", ""
		if value := element.field("MachineId"); value != nil {
			machine = value.scalar
		}
		if value := element.field("TagType"); value != nil {
			kind = value.scalar
		}
		tags = append(tags, fmt.Sprintf("[%d] %s machine %s %s", index, stQuote(name), machine, kind))
	}
	return tags, settings
}
