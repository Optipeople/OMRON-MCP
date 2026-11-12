package sysmac

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// CompareProjects builds a function inventory of an old (before) and a new
// (after migration) Sysmac project and classifies every difference as an
// expected migration change (with rule id), an unexplained change or a
// missing function. It covers POUs, sections and rungs (raw and normalised
// hash), I/O mapping, all variables, events and their triggers, part
// counters, telemetry/batch services and the IoT Hub connection (host,
// client id and machine ids; never SAS tokens or other secrets).
//
// Both paths may be extracted Solution folders or .smc2 archives.

// CompareClass classifies an inventory item.
type CompareClass string

const (
	CompareIdentical   CompareClass = "identical"
	CompareExpected    CompareClass = "expected"
	CompareUnexplained CompareClass = "unexplained"
	CompareMissing     CompareClass = "missing"
)

// CompareRules describes the rule ids of expected migration changes.
var CompareRules = map[string]string{
	"LIB-REPLACE":     "Library Cloud_FB 2.0.x erstattet af NX-IIOT_OptiCloud_V2 (sysmac_library_replace)",
	"FB-SERVICE":      "Cloud_FB-kald erstattet af V2-service (fbCloudConnect→fbConnectionManager, fbCloud_TagGroup→fbOpti_ProductionData_service, fbCloud_EventGroup→fbOpti_Event_Service, fbCloud_Telemetry→fbOpti_Telemetry_Service, fbCloud_Batch→fbOpti_Batch_Service)",
	"INSTANCE":        "FB-instans omdøbt til V2-navnet (ConnectionManager, ProductionCounterService, EventService, TelemetryService, BatchService)",
	"FB-PINS":         "FB-pins synkroniseret med V2-signaturen: omdøbte pins, nye pins uden forbindelse, pintyper og Ix-nummerering",
	"FB-PIN-DROPPED":  "Pin findes ikke i V2-blokken; forbindelsen er fjernet (variablen bliver aldrig skrevet og har ingen startværdi, så der blev ikke sendt noget)",
	"LAYOUT":          "Kun Ix-/pintype-nummerering ændret (ingen funktionel ændring)",
	"TYPE-PC":         "Emnetællertyper skiftet til V2 (sTagGroupSetting→sPCTagGroupSetting, sTagData→sPCTagData, sSample→sPCSample)",
	"IV-PC":           "Emnetællerkonfiguration konverteret (Publish_methode := ChangeOf_State, PartCounter_format := Sample, retry-standardværdier)",
	"IV-PCDATA":       "sPCTagData sat til standardværdi",
	"MEMBER":          "Struct-medlem omdøbt i V2 (sBatchData.ExpectedLineSpeed → ExpectedSpeed)",
	"EVENT-LAYOUT":    "Eventgruppe i V2-layout: RUN/Stop_UnRegistered/Stop_Registered pr. maskine, ekstra events for event-tags, koden bruger; NoOf_Event og retry-standardværdier",
	"EVENT-INDEX":     "Event-tag flyttet til nyt indeks; kodereferencer omnummereret",
	"EVENT-STOPCAUSE": "EventTags[n].EventCode → EventTags[n].Stop_Cause (Stop_Registered-event med stopårsag)",
	"EVENT-UNUSED":    "Event-tag, som ingen kode bruger (ingen trigger), er ikke overført",
	"IOALARM":         "IOalarm-sektion slettet: den fodrede kun EventCode/Trig, som V2 erstatter",
	"UNUSED-CFG":      "Ubrugt lokal sTagGroupSetting-variabel slettet",
}

// CompareFinding is one difference between the projects.
type CompareFinding struct {
	Area   string       `json:"area"`
	Item   string       `json:"item"`
	Class  CompareClass `json:"class"`
	Rules  []string     `json:"rules,omitempty"`
	Detail string       `json:"detail,omitempty"`
}

// CompareSummary counts the classified items.
type CompareSummary struct {
	Identical   int `json:"identical"`
	Expected    int `json:"expected"`
	Unexplained int `json:"unexplained"`
	Missing     int `json:"missing"`
}

type CompareRung struct {
	Section string       `json:"section"`
	Index   int          `json:"index"`
	OldHash string       `json:"oldHash,omitempty"`
	NewHash string       `json:"newHash,omitempty"`
	NormOld string       `json:"normalizedOldHash,omitempty"`
	NormNew string       `json:"normalizedNewHash,omitempty"`
	Class   CompareClass `json:"class"`
	Rules   []string     `json:"rules,omitempty"`
	Detail  string       `json:"detail,omitempty"`
}

type CompareSection struct {
	POU      string        `json:"pou"`
	Name     string        `json:"name"`
	Language string        `json:"language"`
	OldRungs int           `json:"oldRungs"`
	NewRungs int           `json:"newRungs"`
	Class    CompareClass  `json:"class"`
	Rules    []string      `json:"rules,omitempty"`
	Rungs    []CompareRung `json:"rungs,omitempty"`
}

type CompareVariable struct {
	Scope  string       `json:"scope"`
	Name   string       `json:"name"`
	Group  string       `json:"group,omitempty"`
	Old    string       `json:"old,omitempty"` // type and initial value
	New    string       `json:"new,omitempty"`
	Class  CompareClass `json:"class"`
	Rules  []string     `json:"rules,omitempty"`
	Detail string       `json:"detail,omitempty"`
}

type CompareIO struct {
	Name    string       `json:"name"`
	Old     string       `json:"old,omitempty"`
	New     string       `json:"new,omitempty"`
	Comment string       `json:"comment,omitempty"`
	Class   CompareClass `json:"class"`
}

type CompareEvent struct {
	Group    string       `json:"group"`
	OldIndex *int         `json:"oldIndex,omitempty"`
	NewIndex *int         `json:"newIndex,omitempty"`
	Machine  int          `json:"machine"`
	Old      string       `json:"old,omitempty"`
	New      string       `json:"new,omitempty"`
	Triggers []string     `json:"triggers,omitempty"`
	Inputs   []string     `json:"inputs,omitempty"`
	Offsets  []string     `json:"offsetWriters,omitempty"`
	Causes   []string     `json:"causeWriters,omitempty"`
	Class    CompareClass `json:"class"`
	Rules    []string     `json:"rules,omitempty"`
	Detail   string       `json:"detail,omitempty"`
}

type CompareTagGroup struct {
	Kind     string   `json:"kind"` // part counter / telemetry
	Variable string   `json:"variable"`
	Old      []string `json:"old"`
	New      []string `json:"new"`
	// TagListIdentical: same number, order, names, empty slots, MachineId
	// and TagType of the published tags (OptiCloud maps by position/name).
	TagListIdentical bool         `json:"tagListIdentical"`
	Class            CompareClass `json:"class"`
	Rules            []string     `json:"rules,omitempty"`
	Detail           string       `json:"detail,omitempty"`
}

type CompareService struct {
	Location   string            `json:"location"`
	OldFB      string            `json:"oldFB,omitempty"`
	NewFB      string            `json:"newFB,omitempty"`
	Instance   string            `json:"instance,omitempty"`
	Bindings   map[string]string `json:"bindings,omitempty"`
	Unbound    []string          `json:"unboundNewPins,omitempty"`
	Class      CompareClass      `json:"class"`
	Rules      []string          `json:"rules,omitempty"`
	Detail     string            `json:"detail,omitempty"`
	OldBinding map[string]string `json:"oldBindings,omitempty"`
}

type CompareConnection struct {
	OldHosts      []string     `json:"oldHosts"`
	NewHosts      []string     `json:"newHosts"`
	OldClientIDs  []string     `json:"oldClientIds"`
	NewClientIDs  []string     `json:"newClientIds"`
	OldMachineIDs string       `json:"oldMachineIdRef,omitempty"`
	NewMachineIDs string       `json:"newMachineIdRef,omitempty"`
	Class         CompareClass `json:"class"`
	Detail        string       `json:"detail,omitempty"`
}

// CompareReport is the result of CompareProjects.
type CompareReport struct {
	OldProject   string            `json:"oldProject"`
	NewProject   string            `json:"newProject"`
	OldLibraries []string          `json:"oldLibraries"`
	NewLibraries []string          `json:"newLibraries"`
	Summary      CompareSummary    `json:"summary"`
	Findings     []CompareFinding  `json:"findings"`
	Connection   CompareConnection `json:"connection"`
	IO           []CompareIO       `json:"io"`
	Events       []CompareEvent    `json:"events"`
	// EventOrder: every event tag kept at its old index.
	EventOrder map[string]bool   `json:"eventOrderIdentical"`
	TagGroups  []CompareTagGroup `json:"tagGroups"`
	Services   []CompareService  `json:"services"`
	Sections   []CompareSection  `json:"sections"`
	Variables  []CompareVariable `json:"variables"`
	Rules      map[string]string `json:"rules"`
}

type compareProject struct {
	m         *cloudMigration
	name      string
	libraries []string
	pous      map[string]*cloudPOU
	order     []*cloudPOU
}

func loadCompareProject(root string) (*compareProject, func(), error) {
	cleanup := func() {}
	if strings.EqualFold(filepath.Ext(root), ".smc2") {
		dir, err := os.MkdirTemp("", "omron-compare-")
		if err != nil {
			return nil, cleanup, err
		}
		cleanup = func() { _ = os.RemoveAll(dir) }
		if err := extractSMC2(root, dir); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		root = dir
	}
	m := &cloudMigration{options: CloudFBMigrationOptions{ProjectRoot: root}, result: &CloudFBMigrationResult{}, bufferVars: map[string]bool{}, readOnly: true}
	if err := m.load(); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	project := &compareProject{m: m, pous: map[string]*cloudPOU{}}
	for _, entity := range m.doc.order {
		if m.doc.entityType(entity) == "Solution" {
			project.name = m.doc.attr(entity, "name")
			break
		}
	}
	for _, library := range m.doc.topLevelLibraries() {
		project.libraries = append(project.libraries, m.doc.attr(library, "name"))
	}
	sort.Strings(project.libraries)
	for _, pou := range m.pous {
		key := strings.ToLower(pou.name)
		if _, exists := project.pous[key]; exists {
			continue
		}
		project.pous[key] = pou
		project.order = append(project.order, pou)
	}
	return project, cleanup, nil
}

func extractSMC2(path, dir string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer archive.Close()
	base, _ := filepath.Abs(dir)
	for _, file := range archive.File {
		target := filepath.Join(base, filepath.FromSlash(file.Name))
		if !strings.HasPrefix(target, base+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archive entry %s", file.Name)
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		reader, err := file.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			return err
		}
	}
	return nil
}

type compareEventMap struct {
	index     map[int]int
	stopCause map[int]bool
}

type comparer struct {
	old, new  *compareProject
	report    *CompareReport
	instances map[string]map[string]*cloudInstance // pou → old instance (lower)
	events    map[string]*compareEventMap          // event group variable (lower)
	removed   map[string]bool                      // "pou/section" deleted by rule IOALARM
}

// CompareProjects compares an old and a new project (folders or .smc2).
func CompareProjects(oldPath, newPath string) (CompareReport, error) {
	report := CompareReport{Rules: CompareRules}
	old, cleanupOld, err := loadCompareProject(oldPath)
	if err != nil {
		return report, fmt.Errorf("old project: %w", err)
	}
	defer cleanupOld()
	updated, cleanupNew, err := loadCompareProject(newPath)
	if err != nil {
		return report, fmt.Errorf("new project: %w", err)
	}
	defer cleanupNew()
	c := &comparer{old: old, new: updated, report: &report, instances: map[string]map[string]*cloudInstance{}, events: map[string]*compareEventMap{}, removed: map[string]bool{}}
	report.OldProject, report.NewProject = old.name, updated.name
	report.OldLibraries, report.NewLibraries = old.libraries, updated.libraries
	c.compareLibraries()
	c.pairInstances()
	c.findRemovedSections()
	c.compareEvents()
	c.compareSections()
	c.compareIO()
	c.compareVariables()
	c.compareTagGroups()
	c.compareServices()
	c.compareConnection()
	sort.SliceStable(report.Findings, func(a, b int) bool {
		return classRank(report.Findings[a].Class) < classRank(report.Findings[b].Class)
	})
	return report, nil
}

func classRank(class CompareClass) int {
	switch class {
	case CompareMissing:
		return 0
	case CompareUnexplained:
		return 1
	case CompareExpected:
		return 2
	}
	return 3
}

func (c *comparer) count(class CompareClass) {
	switch class {
	case CompareIdentical:
		c.report.Summary.Identical++
	case CompareExpected:
		c.report.Summary.Expected++
	case CompareUnexplained:
		c.report.Summary.Unexplained++
	case CompareMissing:
		c.report.Summary.Missing++
	}
}

func (c *comparer) record(area, item string, class CompareClass, rules []string, detail string) {
	c.count(class)
	if class == CompareIdentical {
		return
	}
	c.report.Findings = append(c.report.Findings, CompareFinding{Area: area, Item: item, Class: class, Rules: rules, Detail: detail})
}

func sortedRules(rules map[string]bool) []string {
	var result []string
	for rule := range rules {
		result = append(result, rule)
	}
	sort.Strings(result)
	return result
}

// ---- libraries ----

func isCloudFBLibrary(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "cloud_fb")
}

func isV2Library(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "nx-iiot_opticloud_v2")
}

func (c *comparer) compareLibraries() {
	newSet := map[string]bool{}
	for _, name := range c.new.libraries {
		newSet[strings.ToLower(name)] = true
	}
	oldSet := map[string]bool{}
	hasV2 := false
	for _, name := range c.new.libraries {
		hasV2 = hasV2 || isV2Library(name)
	}
	for _, name := range c.old.libraries {
		oldSet[strings.ToLower(name)] = true
		switch {
		case newSet[strings.ToLower(name)]:
			c.record("library", name, CompareIdentical, nil, "")
		case (isCloudFBLibrary(name) || isV2Library(name)) && hasV2:
			c.record("library", name, CompareExpected, []string{"LIB-REPLACE"}, "erstattet af "+strings.Join(c.new.libraries, ", "))
		default:
			c.record("library", name, CompareMissing, nil, "library-reference mangler i det nye projekt")
		}
	}
	for _, name := range c.new.libraries {
		if oldSet[strings.ToLower(name)] {
			continue
		}
		if isV2Library(name) {
			continue // counted with LIB-REPLACE
		}
		c.record("library", name, CompareUnexplained, nil, "ny library-reference")
	}
}

// ---- FB instances ----

func (c *comparer) pairInstances() {
	for _, pou := range c.old.order {
		if pou.vars == nil {
			continue
		}
		pairs := map[string]*cloudInstance{}
		newPOU := c.new.pous[strings.ToLower(pou.name)]
		used := map[string]bool{}
		for _, variable := range pou.vars.doc.Variables() {
			mapping := oldFBMapping(variable.Type)
			if mapping == nil || variable.Group != "VAR" {
				continue
			}
			instance := &cloudInstance{OldName: variable.Name, Mapping: mapping}
			if newPOU != nil && newPOU.vars != nil {
				for _, candidate := range newPOU.vars.doc.Variables() {
					if !used[strings.ToLower(candidate.Name)] && isCloudScalarType(candidate.Type, mapping.NewFB) {
						used[strings.ToLower(candidate.Name)] = true
						instance.NewName = candidate.Name
						break
					}
				}
			}
			pairs[strings.ToLower(variable.Name)] = instance
		}
		c.instances[strings.ToLower(pou.name)] = pairs
	}
}

// rewriteOld applies the migration rules to a variable reference of the
// old project and records which rules fired.
func (c *comparer) rewriteOld(pou *cloudPOU, text string, rules map[string]bool) string {
	type edit struct {
		start, end int
		value      string
	}
	var edits []edit
	instances := c.instances[strings.ToLower(pou.name)]
	scanIdentifiers(text, func(start, end int, prevDot bool) bool {
		if prevDot {
			return true
		}
		word := text[start:end]
		memberEnd := end
		if end < len(text) && text[end] == '.' {
			memberEnd = end + 1
			for memberEnd < len(text) && isSTIdentChar(text[memberEnd]) {
				memberEnd++
			}
		}
		if instance := instances[strings.ToLower(word)]; instance != nil && instance.NewName != "" {
			edits = append(edits, edit{start, end, instance.NewName})
			rules["INSTANCE"] = true
			if memberEnd > end+1 {
				if renamed, ok := instance.Mapping.Pins[strings.ToLower(text[end+1:memberEnd])]; ok {
					edits = append(edits, edit{end + 1, memberEnd, renamed})
					rules["FB-PINS"] = true
				}
			}
			return true
		}
		if typ := c.old.m.varType(pou, word); typ != "" && memberEnd > end+1 {
			for _, rename := range cloudMemberRenames {
				if isCloudScalarType(typ, rename.Type) && strings.EqualFold(text[end+1:memberEnd], rename.Old) {
					edits = append(edits, edit{end + 1, memberEnd, rename.New})
					rules["MEMBER"] = true
				}
			}
		}
		if events := c.events[strings.ToLower(word)]; events != nil && strings.HasPrefix(strings.ToLower(text[end:]), ".eventtags") {
			offset := end + len(".eventtags")
			if match := eventIndexPattern.FindStringSubmatchIndex(text[offset:]); match != nil {
				index, _ := strconv.Atoi(text[offset+match[2] : offset+match[3]])
				if target, ok := events.index[index]; ok && target != index {
					edits = append(edits, edit{offset + match[2], offset + match[3], strconv.Itoa(target)})
					rules["EVENT-INDEX"] = true
				}
				if events.stopCause[index] {
					rest := offset + match[1]
					if member := eventCodeMemberPattern.FindStringSubmatchIndex(text[rest:]); member != nil {
						edits = append(edits, edit{rest + member[2], rest + member[3], "Stop_Cause"})
						rules["EVENT-STOPCAUSE"] = true
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

// ---- rungs ----

func shortHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:12]
}

// normalizeRung renders a rung without Ix/LRI/RRI numbering and pin types;
// pins of library cloud FBs are sorted, unconnected pins dropped and power
// flow pin names ignored. On the old side the migration rules are applied.
func (c *comparer) normalizeRung(node *ladderJSON, pou *cloudPOU, oldSide bool, rules map[string]bool, dropped *[]string) string {
	var value any
	if err := json.Unmarshal([]byte(node.render()), &value); err != nil {
		return node.render()
	}
	value = c.normalizeValue(value, pou, oldSide, rules, dropped)
	data, _ := json.Marshal(value)
	return string(data)
}

func (c *comparer) normalizeValue(value any, pou *cloudPOU, oldSide bool, rules map[string]bool, dropped *[]string) any {
	switch typed := value.(type) {
	case []any:
		for index, item := range typed {
			typed[index] = c.normalizeValue(item, pou, oldSide, rules, dropped)
		}
		return typed
	case map[string]any:
		delete(typed, "Ix")
		delete(typed, "LRI")
		delete(typed, "RRI")
		kind, _ := typed["__type"].(string)
		if kind == "PRM" || kind == "PF" {
			delete(typed, "Type")
		}
		if kind == "FB" {
			c.normalizeFB(typed, pou, oldSide, rules, dropped)
		}
		if kind != "IST" {
			if text, ok := typed["Var"].(string); ok && oldSide {
				typed["Var"] = c.rewriteOld(pou, text, rules)
			}
		}
		for key, item := range typed {
			if key == "In" || key == "Out" || key == "CLs" || key == "VLs" {
				typed[key] = c.normalizeValue(item, pou, oldSide, rules, dropped)
			} else if nested, ok := item.(map[string]any); ok {
				typed[key] = c.normalizeValue(nested, pou, oldSide, rules, dropped)
			}
		}
		return typed
	}
	return value
}

func (c *comparer) normalizeFB(fb map[string]any, pou *cloudPOU, oldSide bool, rules map[string]bool, dropped *[]string) {
	name, _ := fb["Name"].(string)
	if !strings.Contains(strings.ToLower(name), strings.ToLower(cloudNamespace)) {
		return
	}
	short := fbShortName(name)
	var mapping *cloudFBMapping
	if oldSide {
		mapping = oldFBTypes[strings.ToLower(short)]
	}
	target := short
	if mapping != nil {
		target = mapping.NewFB
		rules["FB-SERVICE"] = true
	}
	fb["Name"] = target
	signature, known := c.new.m.sigs[strings.ToLower(target)]
	for _, side := range []string{"In", "Out"} {
		pins, _ := fb[side].([]any)
		var kept []any
		for _, item := range pins {
			pin, ok := item.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := pin["__type"].(string)
			if kind == "PF" {
				delete(pin, "Arg")
				kept = append(kept, pin)
				continue
			}
			arg, _ := pin["Arg"].(string)
			if mapping != nil {
				if renamed, ok := mapping.Pins[strings.ToLower(arg)]; ok {
					arg = renamed
					pin["Arg"] = arg
					rules["FB-PINS"] = true
				}
			}
			binding, _ := pin["Var"].(string)
			if strings.TrimSpace(binding) == "" {
				continue
			}
			if oldSide && known {
				found := false
				for _, param := range signature {
					found = found || strings.EqualFold(param.Name, arg)
				}
				if !found {
					switch {
					case side == "In":
						*dropped = append(*dropped, target+"."+arg+"="+binding)
					case pin["IO"] != true:
						*dropped = append(*dropped, target+"."+arg+"="+binding+"@out")
					}
					rules["FB-PIN-DROPPED"] = true
					continue
				}
			}
			kept = append(kept, pin)
		}
		sort.SliceStable(kept, func(a, b int) bool {
			first, _ := kept[a].(map[string]any)
			second, _ := kept[b].(map[string]any)
			argA, _ := first["Arg"].(string)
			argB, _ := second["Arg"].(string)
			return strings.ToLower(argA) < strings.ToLower(argB)
		})
		if kept == nil {
			kept = []any{}
		}
		fb[side] = kept
	}
}

func rungVars(normalized string) map[string]int {
	result := map[string]int{}
	var value any
	if json.Unmarshal([]byte(normalized), &value) != nil {
		return result
	}
	var walk func(any)
	walk = func(item any) {
		switch typed := item.(type) {
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case map[string]any:
			kind, _ := typed["__type"].(string)
			if text, ok := typed["Var"].(string); ok && text != "" {
				result[text]++
			}
			if kind == "F" || kind == "FB" {
				if name, ok := typed["Name"].(string); ok {
					result[kind+" "+fbShortName(name)]++
				}
			}
			if kind == "IST" {
				if text, ok := typed["TXT"].(string); ok {
					result["IST#"+shortHash(text)]++
				}
			}
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return result
}

func describeRungDiff(oldNorm, newNorm string) string {
	oldVars, newVars := rungVars(oldNorm), rungVars(newNorm)
	var onlyOld, onlyNew []string
	for key, count := range oldVars {
		if newVars[key] < count {
			onlyOld = append(onlyOld, key)
		}
	}
	for key, count := range newVars {
		if oldVars[key] < count {
			onlyNew = append(onlyNew, key)
		}
	}
	sort.Strings(onlyOld)
	sort.Strings(onlyNew)
	label := func(items []string) string {
		for index, item := range items {
			if strings.HasPrefix(item, "IST#") {
				items[index] = "inline ST (tekst ændret)"
			}
		}
		return strings.Join(items, ", ")
	}
	var parts []string
	if len(onlyOld) > 0 {
		parts = append(parts, "forventet efter migreringsreglerne, men mangler: "+label(onlyOld))
	}
	if len(onlyNew) > 0 {
		parts = append(parts, "findes i det nye projekt, men var ikke forventet: "+label(onlyNew))
	}
	if len(parts) == 0 {
		return "samme variabler og blokke, men anden struktur (kontakter, forbindelser, negering, flanker eller kommentar)"
	}
	return strings.Join(parts, "; ")
}

func bodyLanguage(c *cloudMigration, body *cloudBody) string {
	if body.ladder != nil {
		return "Ladder"
	}
	return c.doc.attr(body.entity, "subtype")
}

func (c *comparer) compareSections() {
	for _, pou := range c.old.order {
		newPOU := c.new.pous[strings.ToLower(pou.name)]
		if newPOU == nil {
			c.record("pou", pou.name, CompareMissing, nil, "POU findes ikke i det nye projekt")
			continue
		}
		newBodies := map[string]*cloudBody{}
		for _, body := range newPOU.bodies {
			newBodies[strings.ToLower(c.new.m.doc.attr(body.entity, "name"))] = body
		}
		seen := map[string]bool{}
		for _, body := range pou.bodies {
			name := c.old.m.doc.attr(body.entity, "name")
			seen[strings.ToLower(name)] = true
			section := CompareSection{POU: pou.name, Name: name, Language: bodyLanguage(c.old.m, body)}
			if body.ladder != nil {
				section.OldRungs = len(body.ladder.rungs())
			}
			newBody := newBodies[strings.ToLower(name)]
			if newBody == nil {
				if strings.EqualFold(name, "IOalarm") && body.ladder != nil && c.old.m.ioAlarmBlocker(body) == "" && body.usesEventCode() {
					section.Class, section.Rules = CompareExpected, []string{"IOALARM"}
					c.removed[strings.ToLower(pou.name+"/"+name)] = true
					c.record("section", pou.name+"/"+name, CompareExpected, section.Rules, fmt.Sprintf("%d rungs slettet; de skrev kun EventCode/Trig", section.OldRungs))
				} else {
					section.Class = CompareMissing
					c.record("section", pou.name+"/"+name, CompareMissing, nil, "sektionen findes ikke i det nye projekt")
				}
				c.report.Sections = append(c.report.Sections, section)
				continue
			}
			c.compareBody(pou, newPOU, body, newBody, &section)
			c.report.Sections = append(c.report.Sections, section)
		}
		for _, body := range newPOU.bodies {
			name := c.new.m.doc.attr(body.entity, "name")
			if !seen[strings.ToLower(name)] {
				section := CompareSection{POU: newPOU.name, Name: name, Language: bodyLanguage(c.new.m, body), Class: CompareUnexplained}
				if body.ladder != nil {
					section.NewRungs = len(body.ladder.rungs())
				}
				c.report.Sections = append(c.report.Sections, section)
				c.record("section", newPOU.name+"/"+name, CompareUnexplained, nil, "ny sektion")
			}
		}
	}
	for _, pou := range c.new.order {
		if c.old.pous[strings.ToLower(pou.name)] == nil {
			c.record("pou", pou.name, CompareUnexplained, nil, "ny POU")
		}
	}
}

func (body *cloudBody) usesEventCode() bool {
	for _, line := range body.ladder.rungs() {
		for _, value := range ladderVars(line.node) {
			if eventCodeRef.MatchString(value) {
				return true
			}
		}
	}
	return false
}

func (c *comparer) compareBody(pou, newPOU *cloudPOU, body, newBody *cloudBody, section *CompareSection) {
	label := pou.name + "/" + section.Name
	if body.ladder == nil || newBody.ladder == nil {
		if body.ladder != nil || newBody.ladder != nil {
			section.Class = CompareUnexplained
			c.record("section", label, CompareUnexplained, nil, "sproget er ændret")
			return
		}
		if body.text == newBody.text {
			section.Class = CompareIdentical
		} else {
			section.Class = CompareUnexplained
		}
		c.record("section", label, section.Class, nil, "structured text ændret (tekst vises ikke)")
		return
	}
	oldRungs, newRungs := body.ladder.rungs(), newBody.ladder.rungs()
	section.NewRungs = len(newRungs)
	sectionRules := map[string]bool{}
	worst := CompareIdentical
	for index := 0; index < len(oldRungs) || index < len(newRungs); index++ {
		rung := CompareRung{Section: label, Index: index}
		switch {
		case index >= len(newRungs):
			rung.OldHash = shortHash(oldRungs[index].node.render())
			rung.Class, rung.Detail = CompareMissing, "rung findes ikke i det nye projekt"
		case index >= len(oldRungs):
			rung.NewHash = shortHash(newRungs[index].node.render())
			rung.Class, rung.Detail = CompareUnexplained, "ny rung"
		default:
			oldRaw, newRaw := oldRungs[index].node.render(), newRungs[index].node.render()
			rung.OldHash, rung.NewHash = shortHash(oldRaw), shortHash(newRaw)
			if oldRaw == newRaw {
				rung.Class = CompareIdentical
				break
			}
			rules := map[string]bool{}
			var dropped []string
			oldNorm := c.normalizeRung(oldRungs[index].node, pou, true, rules, &dropped)
			newNorm := c.normalizeRung(newRungs[index].node, newPOU, false, map[string]bool{}, &[]string{})
			rung.NormOld, rung.NormNew = shortHash(oldNorm), shortHash(newNorm)
			if oldNorm != newNorm {
				rung.Class, rung.Detail = CompareUnexplained, describeRungDiff(oldNorm, newNorm)
				break
			}
			if len(rules) == 0 {
				rules["LAYOUT"] = true
			}
			rung.Class, rung.Rules = CompareExpected, sortedRules(rules)
			var notes []string
			for _, binding := range dropped {
				output := strings.HasSuffix(binding, "@out")
				binding = strings.TrimSuffix(binding, "@out")
				switch {
				case c.droppedBindingHarmless(binding, output) && output:
					notes = append(notes, "fjernet udgang "+binding+" (variablen læses ingen andre steder)")
				case c.droppedBindingHarmless(binding, output):
					notes = append(notes, "fjernet forbindelse "+binding+" (variablen skrives aldrig og har ingen startværdi)")
				case output:
					rung.Class = CompareMissing
					notes = append(notes, "fjernet udgang "+binding+": variablen læses andre steder, men V2-blokken har ikke udgangen")
				default:
					rung.Class = CompareMissing
					notes = append(notes, "fjernet forbindelse "+binding+": variablen skrives eller har startværdi, funktionen findes ikke i V2")
				}
			}
			rung.Detail = strings.Join(notes, "; ")
			for _, rule := range rung.Rules {
				sectionRules[rule] = true
			}
		}
		// A rung that is unchanged can still be broken by the library
		// change: check member paths against the new library types.
		if index < len(newRungs) {
			if problems := c.invalidReferences(newPOU, newRungs[index].node); len(problems) > 0 {
				rung.Class = CompareMissing
				detail := "refererer til noget, der ikke findes i " + c.new.m.result.Library + ": " + strings.Join(problems, ", ")
				if rung.Detail != "" {
					detail = rung.Detail + "; " + detail
				}
				rung.Detail = detail
			}
		}
		if classRank(rung.Class) < classRank(worst) {
			worst = rung.Class
		}
		c.record("rung", fmt.Sprintf("%s rung %d", label, index), rung.Class, rung.Rules, rung.Detail)
		section.Rungs = append(section.Rungs, rung)
	}
	section.Class, section.Rules = worst, sortedRules(sectionRules)
}

// droppedBindingHarmless reports whether removing the binding of a pin
// ("FB.Pin=Var") loses nothing: for an input the variable is never written
// by the old program and has no initial value (the old FB never received
// data through it); for an output the variable is read nowhere else.
func (c *comparer) droppedBindingHarmless(binding string, output bool) bool {
	parts := strings.SplitN(binding, "=", 2)
	if len(parts) != 2 {
		return false
	}
	name := strings.TrimSpace(parts[1])
	if !isIdentifier(name) {
		return false
	}
	if output {
		return c.old.references(name) <= 1
	}
	for _, vars := range c.old.m.allVars() {
		for _, variable := range vars.doc.Variables() {
			if strings.EqualFold(variable.Name, name) && variable.InitialValue != "" {
				return false
			}
		}
	}
	return !c.old.written(name)
}

// written reports whether code writes name (or one of its members).
func (p *compareProject) written(name string) bool {
	target := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(name) + `\b`)
	assign := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_.])` + regexp.QuoteMeta(name) + `\b[^;=]*:=`)
	for _, pou := range p.order {
		for _, body := range pou.bodies {
			if body.ladder == nil {
				if assign.MatchString(body.text) {
					return true
				}
				continue
			}
			found := false
			for _, line := range body.ladder.rungs() {
				line.node.walk(func(object *ladderJSON) {
					switch object.str("__type") {
					case "ST":
						found = found || target.MatchString(strings.TrimSpace(object.str("Var")))
					case "IST":
						found = found || assign.MatchString(object.str("TXT"))
					case "F", "FB":
						if out := object.get("Out"); out != nil {
							for _, pin := range out.items {
								found = found || target.MatchString(strings.TrimSpace(pin.str("Var")))
							}
						}
					}
				})
			}
			if found {
				return true
			}
		}
	}
	return false
}

// ---- I/O ----

func (c *comparer) compareIO() {
	collect := func(project *compareProject) (map[string]SLWDVariable, []string) {
		result := map[string]SLWDVariable{}
		var order []string
		for _, vars := range project.m.globals {
			for _, variable := range vars.doc.Variables() {
				if variable.Attributes["AT"] == "" {
					continue
				}
				key := strings.ToLower(variable.Name)
				if _, ok := result[key]; !ok {
					order = append(order, key)
				}
				result[key] = variable
			}
		}
		return result, order
	}
	oldIO, order := collect(c.old)
	newIO, newOrder := collect(c.new)
	describe := func(variable SLWDVariable) string {
		return variable.Attributes["AT"] + " : " + variable.Type
	}
	for _, key := range order {
		variable := oldIO[key]
		item := CompareIO{Name: variable.Name, Old: describe(variable), Comment: variable.Comment}
		if updated, ok := newIO[key]; ok {
			item.New = describe(updated)
			if item.New == item.Old && updated.Comment == variable.Comment {
				item.Class = CompareIdentical
			} else {
				item.Class = CompareUnexplained
			}
		} else {
			item.Class = CompareMissing
		}
		c.report.IO = append(c.report.IO, item)
		c.record("io", variable.Name, item.Class, nil, ioDetail(item))
	}
	for _, key := range newOrder {
		if _, ok := oldIO[key]; !ok {
			variable := newIO[key]
			item := CompareIO{Name: variable.Name, New: describe(variable), Comment: variable.Comment, Class: CompareUnexplained}
			c.report.IO = append(c.report.IO, item)
			c.record("io", variable.Name, item.Class, nil, "ny I/O-mapping "+item.New)
		}
	}
}

func ioDetail(item CompareIO) string {
	switch item.Class {
	case CompareMissing:
		return "I/O-mapping mangler: " + item.Old
	case CompareUnexplained:
		return item.Old + " → " + item.New
	}
	return ""
}

// ---- variables ----

var secretNamePattern = regexp.MustCompile(`(?i)(sas|token|passw|secret|key)`)

func displayValue(variable SLWDVariable) string {
	text := variable.Type
	if variable.InitialValue != "" {
		value := variable.InitialValue
		if secretNamePattern.MatchString(variable.Name) || strings.Contains(value, "SharedAccessSignature") || strings.Contains(value, "sig=") {
			value = "<skjult, sha " + shortHash(value) + ">"
		}
		text += " := " + value
	}
	var attributes []string
	for _, key := range []string{"AT", "R", "Const", "RO"} {
		if value := variable.Attributes[key]; value != "" {
			attributes = append(attributes, key+"="+value)
		}
	}
	if len(attributes) > 0 {
		text += " [" + strings.Join(attributes, " ") + "]"
	}
	return text
}

func (c *comparer) compareVariables() {
	type scope struct {
		name     string
		old, new *cloudVars
		pou      *cloudPOU
	}
	var scopes []scope
	for index, vars := range c.old.m.globals {
		var updated *cloudVars
		if index < len(c.new.m.globals) {
			updated = c.new.m.globals[index]
		}
		scopes = append(scopes, scope{name: vars.label, old: vars, new: updated})
	}
	for _, pou := range c.old.order {
		var updated *cloudVars
		if newPOU := c.new.pous[strings.ToLower(pou.name)]; newPOU != nil {
			updated = newPOU.vars
		}
		if pou.vars != nil {
			scopes = append(scopes, scope{name: pou.name, old: pou.vars, new: updated, pou: pou})
		}
	}
	for _, item := range scopes {
		newVars := map[string]SLWDVariable{}
		if item.new != nil {
			for _, variable := range item.new.doc.Variables() {
				newVars[strings.ToLower(variable.Group+"/"+variable.Name)] = variable
			}
		}
		seen := map[string]bool{}
		for _, variable := range item.old.doc.Variables() {
			key := strings.ToLower(variable.Group + "/" + variable.Name)
			seen[key] = true
			entry := CompareVariable{Scope: item.name, Name: variable.Name, Group: variable.Group, Old: displayValue(variable)}
			updated, ok := newVars[key]
			if ok {
				entry.New = displayValue(updated)
				entry.Class, entry.Rules, entry.Detail = c.classifyVariable(variable, updated)
				if problem := c.new.checkPath(updated.Name, updated.Type, ""); problem != "" {
					entry.Class, entry.Detail = CompareMissing, "datatypen findes ikke i "+c.new.m.result.Library+": "+problem
				}
			} else {
				entry.Class, entry.Rules, entry.Detail = c.classifyRemovedVariable(item.pou, variable)
			}
			c.report.Variables = append(c.report.Variables, entry)
			c.record("variable", item.name+"/"+variable.Name, entry.Class, entry.Rules, entry.Detail)
		}
		if item.new == nil {
			continue
		}
		for _, variable := range item.new.doc.Variables() {
			key := strings.ToLower(variable.Group + "/" + variable.Name)
			if seen[key] {
				continue
			}
			entry := CompareVariable{Scope: item.name, Name: variable.Name, Group: variable.Group, New: displayValue(variable), Class: CompareUnexplained, Detail: "ny variabel"}
			for _, mapping := range cloudFBMappings {
				if isCloudScalarType(variable.Type, mapping.NewFB) {
					entry.Class, entry.Rules, entry.Detail = CompareExpected, []string{"INSTANCE"}, "ny V2-instans"
				}
			}
			c.report.Variables = append(c.report.Variables, entry)
			c.record("variable", item.name+"/"+variable.Name, entry.Class, entry.Rules, entry.Detail)
		}
	}
}

func (c *comparer) classifyVariable(old, updated SLWDVariable) (CompareClass, []string, string) {
	if displayValue(old) == displayValue(updated) && old.Comment == updated.Comment && old.InitialValue == updated.InitialValue {
		return CompareIdentical, nil, ""
	}
	rules := map[string]bool{}
	typeOK := old.Type == updated.Type
	if !typeOK {
		renamed := old.Type
		for _, rename := range cloudTypeRenames {
			renamed, _ = renameCloudType(renamed, rename.Old, rename.New)
		}
		renamed, _ = renameCloudType(renamed, cloudSampleOld, cloudSampleNew)
		if strings.EqualFold(renamed, updated.Type) {
			typeOK = true
			rules["TYPE-PC"] = true
		}
	}
	ivOK := old.InitialValue == updated.InitialValue
	if !ivOK {
		switch {
		case isCloudScalarType(old.Type, "sTagGroupSetting"):
			ivOK, rules["IV-PC"] = true, true
		case isCloudType(old.Type, "sTagData"):
			ivOK, rules["IV-PCDATA"] = true, true
		case isCloudScalarType(old.Type, "sEventGroup"):
			ivOK, rules["EVENT-LAYOUT"] = true, true // checked per event in compareEvents
		case len(c.old.m.memberRenames(old.Type)) > 0:
			ivOK, rules["MEMBER"] = true, true
		}
	}
	attributesOK := old.Attributes["AT"] == updated.Attributes["AT"] && old.Attributes["R"] == updated.Attributes["R"] && old.Attributes["Const"] == updated.Attributes["Const"] && old.Comment == updated.Comment
	if typeOK && ivOK && attributesOK {
		if len(rules) == 0 {
			return CompareIdentical, nil, ""
		}
		return CompareExpected, sortedRules(rules), ""
	}
	return CompareUnexplained, nil, "ændret: " + displayValue(old) + " → " + displayValue(updated)
}

func (c *comparer) classifyRemovedVariable(pou *cloudPOU, variable SLWDVariable) (CompareClass, []string, string) {
	if pou != nil {
		if instance := c.instances[strings.ToLower(pou.name)][strings.ToLower(variable.Name)]; instance != nil {
			if instance.NewName != "" {
				return CompareExpected, []string{"INSTANCE"}, "erstattet af " + instance.NewName + " : " + instance.Mapping.NewFB
			}
			return CompareMissing, nil, variable.Type + " er slettet uden en " + instance.Mapping.NewFB + "-instans i det nye projekt"
		}
		if variable.Group == "VAR" && isCloudScalarType(variable.Type, "sTagGroupSetting") && variable.InitialValue == "" {
			used := false
			for _, body := range pou.bodies {
				used = used || body.usesIdentifier(variable.Name)
			}
			if !used {
				return CompareExpected, []string{"UNUSED-CFG"}, ""
			}
		}
	}
	for _, other := range c.old.order {
		for _, body := range other.bodies {
			if body.usesIdentifier(variable.Name) {
				return CompareMissing, nil, "slettet, men bruges i " + other.name + "/" + c.old.m.doc.attr(body.entity, "name")
			}
		}
	}
	return CompareUnexplained, nil, "slettet (bruges ikke i koden)"
}

// ---- events ----

type eventTag struct {
	index           int
	name, kind      string // kind: RUN, Stop_UnRegistered, Stop_Registered
	code, cause     string
	machine         int
	offset          string
	noName, isEmpty bool
}

type eventUse struct {
	triggers, inputs, offsets, causes map[string]bool
	refs                              map[string]bool // section/rung of any reference
}

func newEventUse() *eventUse {
	return &eventUse{triggers: map[string]bool{}, inputs: map[string]bool{}, offsets: map[string]bool{}, causes: map[string]bool{}, refs: map[string]bool{}}
}

func eventGroupTags(value string, v2 bool) ([]eventTag, string) {
	literal, err := parseSTLiteral(value)
	if err != nil || value == "" {
		return nil, ""
	}
	var tags []eventTag
	for index, element := range literal.field("EventTags").expand() {
		tag := eventTag{index: index, name: stString(element.field("Name")), offset: "T#0S"}
		if offset := element.field("SampleTime_Offset"); offset != nil {
			tag.offset = offset.scalar
		}
		if machine := element.field("MachineId"); machine != nil {
			tag.machine, _ = strconv.Atoi(strings.TrimSpace(machine.scalar))
		}
		if v2 {
			if kind := element.field("Event_Type"); kind != nil {
				tag.kind = kind.scalar
			}
			tag.cause = stString(element.field("Stop_Cause"))
		} else {
			tag.code = stString(element.field("EventCode"))
			tag.kind = eventTypeForCode(tag.code)
			if tag.kind == "Stop_Registered" {
				tag.cause = tag.code
			}
		}
		tag.isEmpty = tag.name == ""
		tags = append(tags, tag)
	}
	noOf := ""
	if field := literal.field("NoOf_Event"); field != nil {
		noOf = field.scalar
	}
	return tags, noOf
}

// eventUsage scans the ladder code of a project for EventTags references
// of variable: triggers (Trig written), inputs (contacts of those rungs),
// SampleTime_Offset writers and stop cause writers (EventCode/Stop_Cause).
func (p *compareProject) eventUsage(variable string) map[int]*eventUse {
	pattern := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_.])` + regexp.QuoteMeta(variable) + `\s*\.\s*EventTags\s*\[\s*(\d+)\s*\]\s*(?:\.\s*([A-Za-z_]+))?`)
	result := map[int]*eventUse{}
	get := func(index int) *eventUse {
		if result[index] == nil {
			result[index] = newEventUse()
		}
		return result[index]
	}
	for _, pou := range p.order {
		for _, body := range pou.bodies {
			if body.ladder == nil {
				continue
			}
			section := pou.name + "/" + p.m.doc.attr(body.entity, "name")
			for rung, line := range body.ladder.rungs() {
				where := fmt.Sprintf("%s rung %d", section, rung)
				var inputs []string
				touched := map[int]bool{}
				line.node.walk(func(object *ladderJSON) {
					kind := object.str("__type")
					if kind == "LD" {
						text := object.str("Var")
						if !pattern.MatchString(text) {
							prefix := ""
							if object.get("Not") != nil {
								prefix = "NOT "
							}
							inputs = append(inputs, prefix+text)
						}
					}
					if kind == "IST" {
						for _, match := range pattern.FindAllStringSubmatch(object.str("TXT"), -1) {
							index, _ := strconv.Atoi(match[1])
							use := get(index)
							use.refs[where] = true
							touched[index] = true
							if strings.EqualFold(match[2], "Trig") {
								use.triggers[where+" (inline ST)"] = true
							}
						}
						return
					}
					literal := ""
					if kind == "F" && strings.EqualFold(object.str("Name"), "MOVE") {
						if in := object.get("In"); in != nil {
							for _, pin := range in.items {
								if strings.EqualFold(pin.str("Arg"), "In") {
									literal = pin.str("Var")
								}
							}
						}
					}
					check := func(text string, write bool) {
						for _, match := range pattern.FindAllStringSubmatch(text, -1) {
							index, _ := strconv.Atoi(match[1])
							use := get(index)
							use.refs[where] = true
							touched[index] = true
							if !write {
								continue
							}
							switch strings.ToLower(match[2]) {
							case "trig":
								use.triggers[where] = true
							case "sampletime_offset":
								use.offsets[where+" ← "+literal] = true
							case "eventcode", "stop_cause":
								use.causes[where+" "+match[2]+" ← "+literal] = true
							}
						}
					}
					switch kind {
					case "ST":
						check(object.str("Var"), true)
					case "F", "FB":
						if out := object.get("Out"); out != nil {
							for _, pin := range out.items {
								check(pin.str("Var"), true)
							}
						}
						if in := object.get("In"); in != nil {
							for _, pin := range in.items {
								check(pin.str("Var"), false)
							}
						}
					case "LD":
						check(object.str("Var"), false)
					}
				})
				for index := range touched {
					if len(result[index].triggers) == 0 {
						continue
					}
					for _, input := range inputs {
						result[index].inputs[input] = true
					}
				}
			}
		}
	}
	return result
}

func setList(set map[string]bool) []string {
	var result []string
	for key := range set {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func (c *comparer) compareEvents() {
	for _, vars := range c.old.m.allVars() {
		for _, variable := range vars.doc.Variables() {
			if !isCloudScalarType(variable.Type, "sEventGroup") || variable.Group == "VAR_EXTERNAL" || variable.InitialValue == "" {
				continue
			}
			c.compareEventGroup(variable)
		}
	}
}

func (c *comparer) findNewVariable(name string) (SLWDVariable, bool) {
	for _, vars := range c.new.m.allVars() {
		for _, variable := range vars.doc.Variables() {
			if strings.EqualFold(variable.Name, name) && variable.Group != "VAR_EXTERNAL" {
				return variable, true
			}
		}
	}
	return SLWDVariable{}, false
}

func (c *comparer) compareEventGroup(variable SLWDVariable) {
	oldV2 := strings.Contains(variable.InitialValue, "Event_Type")
	oldTags, oldNoOf := eventGroupTags(variable.InitialValue, oldV2)
	updated, ok := c.findNewVariable(variable.Name)
	if !ok {
		c.record("event", variable.Name, CompareMissing, nil, "eventgruppen findes ikke i det nye projekt")
		return
	}
	v2 := strings.Contains(updated.InitialValue, "Event_Type")
	newTags, newNoOf := eventGroupTags(updated.InitialValue, v2)
	if !v2 {
		// Same Cloud_FB layout on both sides (no migration).
		newTags, newNoOf = eventGroupTags(updated.InitialValue, false)
	}
	alreadyV2 := oldV2 && v2
	oldUse, newUse := c.old.eventUsage(variable.Name), c.new.eventUsage(variable.Name)
	mapping := &compareEventMap{index: map[int]int{}, stopCause: map[int]bool{}}
	matched := map[int]bool{}
	// Per machine: the k-th old event of a kind matches the k-th new event
	// of that kind.
	for _, tag := range oldTags {
		if tag.isEmpty {
			continue
		}
		for _, candidate := range newTags {
			if candidate.isEmpty || matched[candidate.index] || candidate.machine != tag.machine || candidate.kind != tag.kind {
				continue
			}
			mapping.index[tag.index] = candidate.index
			matched[candidate.index] = true
			if v2 && !oldV2 && tag.kind == "Stop_Registered" {
				mapping.stopCause[tag.index] = true
			}
			break
		}
	}
	c.events[strings.ToLower(variable.Name)] = mapping
	orderSame := true
	for from, to := range mapping.index {
		orderSame = orderSame && from == to
	}
	if c.report.EventOrder == nil {
		c.report.EventOrder = map[string]bool{}
	}
	c.report.EventOrder[variable.Name] = orderSame
	usedOld := func(index int) (bool, bool) { // used in kept code, used only in removed sections
		use := oldUse[index]
		if use == nil {
			return false, false
		}
		kept := false
		for location := range use.refs {
			section := location[:strings.LastIndex(location, " rung ")]
			if !c.isRemoved(section) {
				kept = true
			}
		}
		return kept, len(use.refs) > 0 && !kept
	}
	describeOld := func(tag eventTag) string {
		if oldV2 {
			return fmt.Sprintf("%s, Stop_Cause %s, machine %d, offset %s", tag.kind, stQuote(tag.cause), tag.machine, tag.offset)
		}
		return fmt.Sprintf("EventCode %s, machine %d, offset %s", stQuote(tag.code), tag.machine, tag.offset)
	}
	describeNew := func(tag eventTag) string {
		if !v2 {
			return fmt.Sprintf("EventCode %s, machine %d, offset %s", stQuote(tag.code), tag.machine, tag.offset)
		}
		return fmt.Sprintf("%s, Stop_Cause %s, machine %d, offset %s", tag.kind, stQuote(tag.cause), tag.machine, tag.offset)
	}
	groupRules := []string{"EVENT-LAYOUT"}
	if oldNoOf != newNoOf {
		c.record("event", variable.Name+".NoOf_Event", CompareExpected, groupRules, oldNoOf+" → "+newNoOf)
	} else {
		c.record("event", variable.Name+".NoOf_Event", CompareIdentical, nil, "")
	}
	for _, tag := range oldTags {
		if tag.isEmpty {
			continue
		}
		oldIndex := tag.index
		entry := CompareEvent{Group: variable.Name, OldIndex: &oldIndex, Machine: tag.machine, Old: describeOld(tag)}
		if use := oldUse[tag.index]; use != nil {
			entry.Triggers, entry.Inputs, entry.Offsets, entry.Causes = setList(use.triggers), setList(use.inputs), setList(use.offsets), setList(use.causes)
		}
		target, found := mapping.index[tag.index]
		kept, onlyRemoved := usedOld(tag.index)
		switch {
		case !found && kept:
			entry.Class, entry.Detail = CompareMissing, "event-tagget bruges i koden, men har ingen plads i det nye projekt"
		case !found && onlyRemoved:
			entry.Class, entry.Rules, entry.Detail = CompareExpected, []string{"IOALARM"}, "brugtes kun i den slettede IOalarm-sektion"
		case !found:
			entry.Class, entry.Rules, entry.Detail = CompareExpected, []string{"EVENT-UNUSED"}, "ingen kode trigger dette event"
		default:
			newIndex := target
			entry.NewIndex = &newIndex
			var newTag eventTag
			for _, candidate := range newTags {
				if candidate.index == target {
					newTag = candidate
				}
			}
			entry.New = describeNew(newTag)
			entry.Class, entry.Rules, entry.Detail = c.compareEventUse(tag, newTag, oldUse[tag.index], newUse[target], v2, alreadyV2)
		}
		c.report.Events = append(c.report.Events, entry)
		c.record("event", fmt.Sprintf("%s.EventTags[%d]", variable.Name, tag.index), entry.Class, entry.Rules, entry.Detail)
	}
	for _, tag := range newTags {
		if tag.isEmpty || matched[tag.index] {
			continue
		}
		newIndex := tag.index
		entry := CompareEvent{Group: variable.Name, NewIndex: &newIndex, Machine: tag.machine, New: describeNew(tag)}
		if use := newUse[tag.index]; use != nil && len(use.triggers) > 0 {
			entry.Triggers = setList(use.triggers)
			entry.Class, entry.Detail = CompareUnexplained, "nyt event, som koden trigger"
		} else {
			entry.Class, entry.Rules, entry.Detail = CompareExpected, []string{"EVENT-LAYOUT"}, "standardplads i V2-layoutet uden gammelt modstykke; ingen kode trigger den"
		}
		c.report.Events = append(c.report.Events, entry)
		c.record("event", fmt.Sprintf("%s.EventTags[%d] (ny)", variable.Name, tag.index), entry.Class, entry.Rules, entry.Detail)
	}
}

func (c *comparer) isRemoved(section string) bool {
	return c.removed[strings.ToLower(section)]
}

func (c *comparer) compareEventUse(old, updated eventTag, oldUse, newUse *eventUse, v2, alreadyV2 bool) (CompareClass, []string, string) {
	if oldUse == nil {
		oldUse = newEventUse()
	}
	if newUse == nil {
		newUse = newEventUse()
	}
	keptOnly := func(set map[string]bool) map[string]bool {
		result := map[string]bool{}
		for key := range set {
			section := key
			if index := strings.LastIndex(key, " rung "); index >= 0 {
				section = key[:index]
			}
			if !c.isRemoved(section) {
				result[key] = true
			}
		}
		return result
	}
	var problems []string
	compareSets := func(label string, before, after map[string]bool) {
		a, b := setList(keptOnly(before)), setList(after)
		if strings.Join(a, "|") != strings.Join(b, "|") {
			problems = append(problems, fmt.Sprintf("%s før [%s] efter [%s]", label, strings.Join(a, ", "), strings.Join(b, ", ")))
		}
	}
	compareSets("triggere", oldUse.triggers, newUse.triggers)
	compareSets("tidsstempel-offset", oldUse.offsets, newUse.offsets)
	oldCauses := map[string]bool{}
	for key := range oldUse.causes {
		if v2 {
			key = regexp.MustCompile(`(?i) EventCode ←`).ReplaceAllString(key, " Stop_Cause ←")
		}
		oldCauses[key] = true
	}
	compareSets("stopårsag", oldCauses, newUse.causes)
	// Inputs are compared after the old references were renamed.
	inputs := func(set map[string]bool, rename bool) map[string]bool {
		result := map[string]bool{}
		for key := range set {
			if rename {
				prefix := ""
				if strings.HasPrefix(key, "NOT ") {
					prefix, key = "NOT ", strings.TrimPrefix(key, "NOT ")
				}
				for _, pou := range c.old.order {
					if renamed := c.rewriteOld(pou, key, map[string]bool{}); renamed != key {
						key = renamed
						break
					}
				}
				key = prefix + key
			}
			result[key] = true
		}
		return result
	}
	if a, b := setList(inputs(oldUse.inputs, true)), setList(inputs(newUse.inputs, false)); strings.Join(a, "|") != strings.Join(b, "|") && len(keptOnly(oldUse.triggers)) == len(oldUse.triggers) {
		problems = append(problems, fmt.Sprintf("inputs før [%s] efter [%s]", strings.Join(a, ", "), strings.Join(b, ", ")))
	}
	if old.machine != updated.machine {
		problems = append(problems, fmt.Sprintf("MachineId %d → %d", old.machine, updated.machine))
	}
	if !strings.EqualFold(old.offset, updated.offset) {
		problems = append(problems, fmt.Sprintf("SampleTime_Offset %s → %s", old.offset, updated.offset))
	}
	if v2 && old.kind == "Stop_Registered" && old.cause != updated.cause {
		problems = append(problems, fmt.Sprintf("stopårsag %s → %s", stQuote(old.cause), stQuote(updated.cause)))
	}
	if old.index != updated.index {
		// The migration moves tags only to build the per machine layout;
		// OptiCloud may rely on the order, so a technician must confirm.
		problems = append(problems, fmt.Sprintf("event-tagget er flyttet fra indeks %d til %d (rækkefølgen er ændret; bekræft OptiCloud-mapningen)", old.index, updated.index))
	}
	if len(problems) > 0 {
		return CompareUnexplained, nil, strings.Join(problems, "; ")
	}
	if (!v2 || alreadyV2) && old.index == updated.index && old.code == updated.code && old.kind == updated.kind {
		return CompareIdentical, nil, ""
	}
	rules := []string{"EVENT-LAYOUT"}
	if old.index != updated.index {
		rules = append(rules, "EVENT-INDEX")
	}
	if len(oldUse.causes) > 0 {
		rules = append(rules, "EVENT-STOPCAUSE")
	}
	return CompareExpected, rules, fmt.Sprintf("%s → %s; samme triggere, inputs, offset og stopårsag", eventTypeForCode(old.code), updated.kind)
}

// ---- part counter / telemetry tag groups ----

func (c *comparer) compareTagGroups() {
	kinds := []struct{ old, kind string }{{"sTagGroupSetting", "emnetæller"}, {"sTelemtryGroupSetting", "telemetri"}}
	for _, vars := range c.old.m.allVars() {
		for _, variable := range vars.doc.Variables() {
			if variable.Group == "VAR_EXTERNAL" || variable.InitialValue == "" {
				continue
			}
			for _, kind := range kinds {
				if !isCloudScalarType(variable.Type, kind.old) {
					continue
				}
				entry := CompareTagGroup{Kind: kind.kind, Variable: variable.Name}
				oldTags, oldSettings := tagGroupSummary(variable.InitialValue)
				entry.Old = append(oldTags, settingsLine(oldSettings))
				updated, ok := c.findNewVariable(variable.Name)
				if !ok {
					entry.Class, entry.Detail = CompareMissing, "konfigurationen findes ikke i det nye projekt"
				} else {
					newTags, newSettings := tagGroupSummary(updated.InitialValue)
					entry.New = append(newTags, settingsLine(newSettings))
					entry.TagListIdentical = strings.Join(oldTags, "|") == strings.Join(newTags, "|") && oldSettings["noof_tag"] == newSettings["noof_tag"]
					switch {
					case !entry.TagListIdentical:
						entry.Class, entry.Detail = CompareMissing, "tag-listen er ikke identisk (OptiCloud mapper efter position/navn): før ["+strings.Join(oldTags, "; ")+"] efter ["+strings.Join(newTags, "; ")+"], NoOf_Tag "+valueOr(oldSettings["noof_tag"])+" → "+valueOr(newSettings["noof_tag"])
					case variable.InitialValue == updated.InitialValue:
						entry.Class = CompareIdentical
					default:
						changes := settingChanges(oldSettings, newSettings)
						if kind.kind == "emnetæller" && changesAllowed(changes, "publish_methode", "partcounter_format", "noof_retry_before_cancel_message", "waittime_before_retry") {
							entry.Class, entry.Rules = CompareExpected, []string{"IV-PC"}
						} else {
							entry.Class = CompareUnexplained
						}
						entry.Detail = "samme tags; " + strings.Join(changes, ", ")
					}
				}
				c.report.TagGroups = append(c.report.TagGroups, entry)
				c.record(kind.kind, variable.Name, entry.Class, entry.Rules, entry.Detail)
			}
		}
	}
}

func settingsLine(settings map[string]string) string {
	var parts []string
	for key, value := range settings {
		parts = append(parts, key+"="+value)
	}
	sort.Strings(parts)
	return "indstillinger: " + strings.Join(parts, ", ")
}

func settingChanges(old, updated map[string]string) []string {
	var result []string
	keys := map[string]bool{}
	for key := range old {
		keys[key] = true
	}
	for key := range updated {
		keys[key] = true
	}
	for _, key := range setList(keys) {
		if old[key] != updated[key] {
			result = append(result, fmt.Sprintf("%s %s → %s", key, valueOr(old[key]), valueOr(updated[key])))
		}
	}
	return result
}

func valueOr(value string) string {
	if value == "" {
		return "(ingen)"
	}
	return value
}

func changesAllowed(changes []string, keys ...string) bool {
	for _, change := range changes {
		ok := false
		for _, key := range keys {
			ok = ok || strings.HasPrefix(change, key+" ")
		}
		if !ok {
			return false
		}
	}
	return true
}

// ---- cloud services (FB calls) ----

type serviceCall struct {
	location, fb, instance string
	bindings               map[string]string
	outputs                map[string]bool // pins bound on the output side only
}

func (p *compareProject) serviceCalls() []serviceCall {
	var result []serviceCall
	for _, pou := range p.order {
		for _, body := range pou.bodies {
			if body.ladder == nil {
				continue
			}
			for rung, line := range body.ladder.rungs() {
				line.node.walk(func(object *ladderJSON) {
					name := object.str("Name")
					if object.str("__type") != "FB" || !strings.Contains(strings.ToLower(name), strings.ToLower(cloudNamespace)) {
						return
					}
					call := serviceCall{location: fmt.Sprintf("%s/%s rung %d", pou.name, p.m.doc.attr(body.entity, "name"), rung), fb: fbShortName(name), instance: object.str("Var"), bindings: map[string]string{}, outputs: map[string]bool{}}
					for side, list := range []*ladderJSON{object.get("In"), object.get("Out")} {
						if list == nil {
							continue
						}
						for _, pin := range list.items {
							if pin.str("__type") == "PRM" && strings.TrimSpace(pin.str("Var")) != "" {
								call.bindings[pin.str("Arg")] = pin.str("Var")
								if side == 1 && pin.get("IO") == nil {
									call.outputs[strings.ToLower(pin.str("Arg"))] = true
								}
							}
						}
					}
					result = append(result, call)
				})
			}
		}
	}
	return result
}

func (c *comparer) compareServices() {
	newCalls := c.new.serviceCalls()
	used := make([]bool, len(newCalls))
	for _, call := range c.old.serviceCalls() {
		target := call.fb
		mapping := oldFBTypes[strings.ToLower(call.fb)]
		if mapping != nil {
			target = mapping.NewFB
		}
		entry := CompareService{Location: call.location, OldFB: call.fb, OldBinding: call.bindings}
		match := -1
		for index, candidate := range newCalls {
			if !used[index] && candidate.location == call.location && strings.EqualFold(candidate.fb, target) {
				match = index
				break
			}
		}
		if match < 0 {
			entry.Class, entry.Detail = CompareMissing, "kaldet af "+call.fb+" findes ikke i det nye projekt"
			c.report.Services = append(c.report.Services, entry)
			c.record("service", call.location+" "+call.fb, entry.Class, nil, entry.Detail)
			continue
		}
		used[match] = true
		updated := newCalls[match]
		entry.NewFB, entry.Instance, entry.Bindings = updated.fb, updated.instance, updated.bindings
		rules := map[string]bool{}
		if mapping != nil {
			rules["FB-SERVICE"] = true
		}
		pou := c.old.pous[strings.ToLower(strings.SplitN(call.location, "/", 2)[0])]
		if pou != nil && c.rewriteOld(pou, call.instance, rules) != updated.instance {
			entry.Class, entry.Detail = CompareUnexplained, "instans "+call.instance+" → "+updated.instance
		}
		var problems, notes []string
		for arg, binding := range call.bindings {
			newArg := arg
			if mapping != nil {
				if renamed, ok := mapping.Pins[strings.ToLower(arg)]; ok {
					newArg = renamed
					rules["FB-PINS"] = true
				}
			}
			renamedBinding := binding
			if pou != nil {
				renamedBinding = c.rewriteOld(pou, binding, rules)
			}
			got, ok := lookupFold(updated.bindings, newArg)
			switch {
			case ok && got == renamedBinding:
			case ok:
				problems = append(problems, fmt.Sprintf("%s: %s → %s", newArg, binding, got))
			case !c.hasParam(updated.fb, newArg):
				rules["FB-PIN-DROPPED"] = true
				output := call.outputs[strings.ToLower(arg)]
				if c.droppedBindingHarmless(updated.fb+"."+newArg+"="+binding, output) {
					reason := "variablen skrives aldrig og har ingen startværdi"
					if output {
						reason = "udgangen læses ingen andre steder"
					}
					notes = append(notes, fmt.Sprintf("%s=%s fjernet (findes ikke i %s; %s)", arg, binding, updated.fb, reason))
				} else {
					entry.Class = CompareMissing
					problems = append(problems, fmt.Sprintf("%s=%s fjernet, men variablen bruges (findes ikke i %s)", arg, binding, updated.fb))
				}
			default:
				entry.Class = CompareMissing
				problems = append(problems, fmt.Sprintf("%s er ikke længere forbundet (var %s)", newArg, binding))
			}
		}
		for _, param := range c.new.m.sigs[strings.ToLower(updated.fb)] {
			if _, ok := lookupFold(updated.bindings, param.Name); !ok && (param.Group == "VAR_INPUT" || param.Group == "VAR_OUTPUT") && !strings.EqualFold(param.Type, "BOOL") || false {
				entry.Unbound = append(entry.Unbound, param.Name)
			}
		}
		sort.Strings(problems)
		sort.Strings(notes)
		switch {
		case entry.Class == CompareMissing:
		case len(problems) > 0:
			entry.Class = CompareUnexplained
		case entry.Class == CompareUnexplained:
		case call.fb == updated.fb && len(rules) == 0:
			entry.Class = CompareIdentical
		default:
			entry.Class = CompareExpected
		}
		entry.Rules = sortedRules(rules)
		entry.Detail = strings.Join(append(problems, notes...), "; ")
		if entry.Detail == "" && entry.Class == CompareExpected {
			entry.Detail = fmt.Sprintf("%s %s → %s %s; alle forbindelser bevaret", call.fb, call.instance, updated.fb, updated.instance)
		}
		c.report.Services = append(c.report.Services, entry)
		c.record("service", call.location+" "+call.fb, entry.Class, entry.Rules, entry.Detail)
	}
	for index, call := range newCalls {
		if used[index] {
			continue
		}
		entry := CompareService{Location: call.location, NewFB: call.fb, Instance: call.instance, Bindings: call.bindings, Class: CompareUnexplained, Detail: "nyt kald"}
		c.report.Services = append(c.report.Services, entry)
		c.record("service", call.location+" "+call.fb, entry.Class, nil, entry.Detail)
	}
}

func lookupFold(values map[string]string, key string) (string, bool) {
	for candidate, value := range values {
		if strings.EqualFold(candidate, key) {
			return value, true
		}
	}
	return "", false
}

func (c *comparer) hasParam(fb, name string) bool {
	params, known := c.new.m.sigs[strings.ToLower(fb)]
	if !known {
		return true
	}
	for _, param := range params {
		if strings.EqualFold(param.Name, name) {
			return true
		}
	}
	return false
}

// ---- connection ----

var hubPattern = regexp.MustCompile(`(?i)([A-Za-z0-9-]+\.azure-devices\.net)(/([A-Za-z0-9_.-]+))?`)

type connectionInfo struct {
	hosts, clients map[string]bool
	machineIDs     string
}

func (p *compareProject) connection() connectionInfo {
	info := connectionInfo{hosts: map[string]bool{}, clients: map[string]bool{}}
	scanText := func(text string) {
		for _, match := range hubPattern.FindAllStringSubmatch(text, -1) {
			info.hosts[strings.ToLower(match[1])] = true
			if match[3] != "" {
				info.clients[match[3]+" (brugernavn)"] = true
			}
		}
	}
	for _, call := range p.serviceCalls() {
		for arg, binding := range call.bindings {
			switch strings.ToLower(arg) {
			case "hostadr":
				info.hosts[strings.ToLower(stString(stScalar(binding)))] = true
			case "clientid":
				if strings.HasPrefix(binding, "'") {
					info.clients[stString(stScalar(binding))+" (ClientID)"] = true
				} else {
					info.clients["variabel "+binding+" (ClientID)"] = true
				}
			}
		}
	}
	for _, pou := range p.order {
		for _, body := range pou.bodies {
			if body.ladder == nil {
				scanText(body.text)
				continue
			}
			for _, line := range body.ladder.rungs() {
				for _, text := range istTexts(line.node) {
					scanText(text)
				}
			}
		}
	}
	for _, vars := range p.m.globals {
		for _, variable := range vars.doc.Variables() {
			if strings.EqualFold(variable.Name, "machineIdRef") {
				info.machineIDs = variable.InitialValue
			}
		}
	}
	return info
}

func (c *comparer) compareConnection() {
	old, updated := c.old.connection(), c.new.connection()
	entry := CompareConnection{OldHosts: setList(old.hosts), NewHosts: setList(updated.hosts), OldClientIDs: setList(old.clients), NewClientIDs: setList(updated.clients), OldMachineIDs: old.machineIDs, NewMachineIDs: updated.machineIDs}
	var problems []string
	if strings.Join(entry.OldHosts, "|") != strings.Join(entry.NewHosts, "|") {
		problems = append(problems, "IoT Hub-host ændret")
	}
	if strings.Join(entry.OldClientIDs, "|") != strings.Join(entry.NewClientIDs, "|") {
		problems = append(problems, "ClientID/device ændret")
	}
	if entry.OldMachineIDs != entry.NewMachineIDs {
		problems = append(problems, "machineIdRef ændret")
	}
	entry.Class = CompareIdentical
	if len(problems) > 0 {
		entry.Class, entry.Detail = CompareMissing, strings.Join(problems, "; ")
	}
	c.report.Connection = entry
	c.record("connection", "IoT Hub / ClientID / machineIdRef", entry.Class, nil, entry.Detail)
}

// ---- markdown ----

func mdCell(text string) string {
	text = strings.ReplaceAll(text, "|", `\|`)
	text = strings.ReplaceAll(text, "\r", "")
	return strings.ReplaceAll(text, "\n", " ")
}

func classLabel(class CompareClass) string {
	switch class {
	case CompareIdentical:
		return "identisk"
	case CompareExpected:
		return "(a) forventet"
	case CompareUnexplained:
		return "(b) uforklaret"
	case CompareMissing:
		return "(c) MANGLER"
	}
	return string(class)
}

// RenderCompareMarkdown renders a report for a technician's approval.
func RenderCompareMarkdown(report CompareReport, title string) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	if title == "" {
		title = "Funktionsinventar: " + report.OldProject + " før og efter migrering"
	}
	w("# %s\n\n", title)
	w("Lavet med `sysmac_program_compare` (omron-mcp). Rapporten indeholder ingen SAS-tokens eller andre hemmeligheder; inline ST vises kun som hash.\n\n")
	w("- Før: `%s`, libraries: %s\n", report.OldProject, strings.Join(report.OldLibraries, ", "))
	w("- Efter: `%s`, libraries: %s\n\n", report.NewProject, strings.Join(report.NewLibraries, ", "))
	w("## Resumé\n\n| Klasse | Antal |\n|---|---|\n")
	w("| identisk | %d |\n| (a) forventet migreringsændring | %d |\n| (b) uforklaret ændring | %d |\n| (c) manglende funktion | %d |\n\n", report.Summary.Identical, report.Summary.Expected, report.Summary.Unexplained, report.Summary.Missing)
	switch {
	case report.Summary.Missing > 0:
		w("**Der mangler funktioner.** Se (c) nedenfor, før projektet godkendes.\n\n")
	case report.Summary.Unexplained > 0:
		w("Ingen manglende funktioner, men %d uforklarede ændringer skal vurderes af en tekniker.\n\n", report.Summary.Unexplained)
	default:
		w("Ingen manglende funktioner og ingen uforklarede ændringer: alle forskelle er forventede migreringsændringer.\n\n")
	}
	for _, class := range []CompareClass{CompareMissing, CompareUnexplained} {
		var items []CompareFinding
		for _, finding := range report.Findings {
			if finding.Class == class {
				items = append(items, finding)
			}
		}
		w("## %s (%d)\n\n", classLabel(class), len(items))
		if len(items) == 0 {
			w("Ingen.\n\n")
			continue
		}
		w("| Område | Element | Detalje |\n|---|---|---|\n")
		for _, item := range items {
			w("| %s | %s | %s |\n", item.Area, mdCell(item.Item), mdCell(item.Detail))
		}
		w("\n")
	}
	w("## (a) Forventede migreringsændringer\n\n| Område | Element | Regler | Detalje |\n|---|---|---|---|\n")
	for _, finding := range report.Findings {
		if finding.Class == CompareExpected {
			w("| %s | %s | %s | %s |\n", finding.Area, mdCell(finding.Item), strings.Join(finding.Rules, ", "), mdCell(finding.Detail))
		}
	}
	w("\n## Forbindelse (IoT Hub)\n\n| | Før | Efter |\n|---|---|---|\n")
	w("| IoT Hub-host | %s | %s |\n", mdCell(strings.Join(report.Connection.OldHosts, ", ")), mdCell(strings.Join(report.Connection.NewHosts, ", ")))
	w("| ClientID / device | %s | %s |\n", mdCell(strings.Join(report.Connection.OldClientIDs, ", ")), mdCell(strings.Join(report.Connection.NewClientIDs, ", ")))
	w("| machineIdRef | %s | %s |\n\n", mdCell(report.Connection.OldMachineIDs), mdCell(report.Connection.NewMachineIDs))
	w("Klasse: %s\n\n", classLabel(report.Connection.Class))
	w("## Events\n\n")
	for _, group := range setList(boolKeys(report.EventOrder)) {
		answer := "ja"
		if !report.EventOrder[group] {
			answer = "**NEJ** (se flyttede event-tags nedenfor)"
		}
		w("Event-rækkefølge for `%s` identisk (hvert overført event-tag på samme indeks): %s\n\n", group, answer)
	}
	w("| Gruppe | Før | Efter | Maskine | Før-konfiguration | Efter-konfiguration | Triggere | Inputs | Offset-skrivninger | Stopårsag-skrivninger | Klasse |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, event := range report.Events {
		index := func(value *int) string {
			if value == nil {
				return "–"
			}
			return strconv.Itoa(*value)
		}
		w("| %s | %s | %s | %d | %s | %s | %s | %s | %s | %s | %s |\n", event.Group, index(event.OldIndex), index(event.NewIndex), event.Machine, mdCell(event.Old), mdCell(event.New), mdCell(strings.Join(event.Triggers, "; ")), mdCell(strings.Join(event.Inputs, ", ")), mdCell(strings.Join(event.Offsets, "; ")), mdCell(strings.Join(event.Causes, "; ")), classLabel(event.Class)+" "+strings.Join(event.Rules, ","))
	}
	w("\n## Emnetællere og telemetri\n\n")
	for _, group := range report.TagGroups {
		identical := "ja"
		if !group.TagListIdentical {
			identical = "**NEJ**"
		}
		w("### %s `%s` – %s\n\nTag-liste identisk (antal, rækkefølge, navne, tomme pladser, MachineId, TagType): %s\n\n", group.Kind, group.Variable, classLabel(group.Class)+" "+strings.Join(group.Rules, ","), identical)
		w("| Før | Efter |\n|---|---|\n")
		for index := 0; index < len(group.Old) || index < len(group.New); index++ {
			var a, z string
			if index < len(group.Old) {
				a = group.Old[index]
			}
			if index < len(group.New) {
				z = group.New[index]
			}
			w("| %s | %s |\n", mdCell(a), mdCell(z))
		}
		if group.Detail != "" {
			w("\n%s\n", mdCell(group.Detail))
		}
		w("\n")
	}
	w("## Cloud-services (FB-kald)\n\n| Sted | Før | Efter | Instans | Forbindelser | Uforbundne nye pins | Klasse | Detalje |\n|---|---|---|---|---|---|---|---|\n")
	for _, service := range report.Services {
		var bindings []string
		for arg, value := range service.Bindings {
			bindings = append(bindings, arg+"="+value)
		}
		sort.Strings(bindings)
		w("| %s | %s | %s | %s | %s | %s | %s | %s |\n", mdCell(service.Location), service.OldFB, service.NewFB, service.Instance, mdCell(strings.Join(bindings, ", ")), mdCell(strings.Join(service.Unbound, ", ")), classLabel(service.Class)+" "+strings.Join(service.Rules, ","), mdCell(service.Detail))
	}
	w("\n## I/O-mapping\n\n| Variabel | Før | Efter | Kommentar | Klasse |\n|---|---|---|---|---|\n")
	for _, item := range report.IO {
		w("| %s | %s | %s | %s | %s |\n", item.Name, mdCell(item.Old), mdCell(item.New), mdCell(item.Comment), classLabel(item.Class))
	}
	w("\n## POU'er, sektioner og rungs\n\nHash = SHA-256 (12 tegn) af rungens rå form; normaliseret hash = uden Ix-nummerering og pintyper, med migreringsreglerne anvendt på før-siden.\n\n")
	w("| Sektion | Sprog | Rungs før/efter | Klasse | Regler |\n|---|---|---|---|---|\n")
	for _, section := range report.Sections {
		w("| %s/%s | %s | %d/%d | %s | %s |\n", section.POU, section.Name, section.Language, section.OldRungs, section.NewRungs, classLabel(section.Class), strings.Join(section.Rules, ", "))
	}
	w("\n<details><summary>Alle rungs</summary>\n\n| Sektion | Rung | Hash før | Hash efter | Normaliseret før/efter | Klasse | Regler | Detalje |\n|---|---|---|---|---|---|---|---|\n")
	for _, section := range report.Sections {
		for _, rung := range section.Rungs {
			norm := ""
			if rung.NormOld != "" {
				norm = rung.NormOld + " / " + rung.NormNew
			}
			w("| %s | %d | %s | %s | %s | %s | %s | %s |\n", mdCell(rung.Section), rung.Index, rung.OldHash, rung.NewHash, norm, classLabel(rung.Class), strings.Join(rung.Rules, ", "), mdCell(rung.Detail))
		}
	}
	w("\n</details>\n\n## Variabler\n\n")
	identical := 0
	w("| Scope | Navn | Gruppe | Før | Efter | Klasse | Regler | Detalje |\n|---|---|---|---|---|---|---|---|\n")
	for _, variable := range report.Variables {
		if variable.Class == CompareIdentical {
			identical++
			continue
		}
		w("| %s | %s | %s | %s | %s | %s | %s | %s |\n", mdCell(variable.Scope), variable.Name, variable.Group, mdCell(truncate(variable.Old, 300)), mdCell(truncate(variable.New, 300)), classLabel(variable.Class), strings.Join(variable.Rules, ", "), mdCell(variable.Detail))
	}
	w("\n<details><summary>%d identiske variabler</summary>\n\n| Scope | Navn | Gruppe | Type / startværdi |\n|---|---|---|---|\n", identical)
	for _, variable := range report.Variables {
		if variable.Class == CompareIdentical {
			w("| %s | %s | %s | %s |\n", mdCell(variable.Scope), variable.Name, variable.Group, mdCell(truncate(variable.Old, 200)))
		}
	}
	w("\n</details>\n\n## Regler\n\n| Regel | Beskrivelse |\n|---|---|\n")
	var ids []string
	for id := range report.Rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		w("| %s | %s |\n", id, mdCell(report.Rules[id]))
	}
	return b.String()
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

func boolKeys(values map[string]bool) map[string]bool {
	result := map[string]bool{}
	for key := range values {
		result[key] = true
	}
	return result
}

// invalidReferences checks the variable references of a rung of the new
// project against the library: variables of a library type that no longer
// exists, members that a library struct or FB does not have.
func (c *comparer) invalidReferences(pou *cloudPOU, node *ladderJSON) []string {
	seen := map[string]bool{}
	var problems []string
	for _, text := range ladderVars(node) {
		for _, problem := range c.new.invalidPaths(pou, text) {
			if !seen[problem] {
				seen[problem] = true
				problems = append(problems, problem)
			}
		}
	}
	sort.Strings(problems)
	return problems
}

var referencePathPattern = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)((?:\s*\[[^\]]*\]|\s*\.\s*[A-Za-z_][A-Za-z0-9_]*)*)`)
var pathStepPattern = regexp.MustCompile(`\s*(\[[^\]]*\]|\.\s*[A-Za-z_][A-Za-z0-9_]*)`)

func (p *compareProject) invalidPaths(pou *cloudPOU, text string) []string {
	var problems []string
	for _, match := range referencePathPattern.FindAllStringSubmatchIndex(text, -1) {
		start := match[0]
		if start > 0 && (text[start-1] == '.' || text[start-1] == '#' || text[start-1] == '\'' || isSTIdentChar(text[start-1])) {
			continue
		}
		if strings.Count(text[:start], "'")%2 == 1 {
			continue // inside a string literal
		}
		name := text[match[2]:match[3]]
		typ := p.m.varType(pou, name)
		if typ == "" {
			continue
		}
		path := text[match[4]:match[5]]
		if problem := p.checkPath(name, typ, path); problem != "" {
			problems = append(problems, problem)
		}
	}
	return problems
}

// checkPath walks "[i]" and ".member" steps through library types; it
// stops silently at types it does not know (user or system types).
func (p *compareProject) checkPath(name, typ, path string) string {
	current := strings.TrimSpace(typ)
	walked := name
	// A variable typed with an old Cloud_FB block or struct that the new
	// library does not define.
	_, namespace, short, ok := splitTypeName(current)
	if ok && strings.EqualFold(strings.TrimSuffix(namespace, `\`), cloudNamespace) && p.m.lookupType(current) == nil {
		if _, isFB := p.m.sigs[strings.ToLower(short)]; !isFB && p.m.result.Library != "" {
			return walked + " (typen " + short + " findes ikke)"
		}
	}
	for _, step := range pathStepPattern.FindAllStringSubmatch(path, -1) {
		token := strings.TrimSpace(step[1])
		if strings.HasPrefix(token, "[") {
			match := arrayBoundsPattern.FindStringSubmatch(current)
			if match == nil {
				return ""
			}
			current = strings.TrimSpace(match[3])
			walked += token
			continue
		}
		member := strings.TrimSpace(strings.TrimPrefix(token, "."))
		walked += "." + member
		_, _, short, ok := splitTypeName(current)
		if !ok {
			return ""
		}
		if params, isFB := p.m.sigs[strings.ToLower(short)]; isFB && isCloudType(current, short) {
			found := false
			for _, param := range params {
				found = found || strings.EqualFold(param.Name, member)
			}
			if !found {
				return walked
			}
			return "" // FB parameters are not walked further
		}
		definition := p.m.lookupType(current)
		if definition == nil || !isCloudType(current, short) || !strings.EqualFold(definition.Base, "STRUCT") {
			return ""
		}
		next := ""
		for _, candidate := range definition.Members {
			if strings.EqualFold(candidate.Name, member) {
				next = candidate.Base
			}
		}
		if next == "" {
			return walked
		}
		current = next
	}
	return ""
}

// findRemovedSections marks IOalarm sections deleted under rule IOALARM
// (needed before events are compared).
func (c *comparer) findRemovedSections() {
	for _, pou := range c.old.order {
		newPOU := c.new.pous[strings.ToLower(pou.name)]
		if newPOU == nil {
			continue
		}
		for _, body := range pou.bodies {
			name := c.old.m.doc.attr(body.entity, "name")
			exists := false
			for _, candidate := range newPOU.bodies {
				exists = exists || strings.EqualFold(c.new.m.doc.attr(candidate.entity, "name"), name)
			}
			if !exists && strings.EqualFold(name, "IOalarm") && body.ladder != nil && c.old.m.ioAlarmBlocker(body) == "" && body.usesEventCode() {
				c.removed[strings.ToLower(pou.name+"/"+name)] = true
			}
		}
	}
}

// references counts the references to name (outside string literals) in
// the code of the project.
func (p *compareProject) references(name string) int {
	count := 0
	countText := func(text string) {
		scanIdentifiers(text, func(start, end int, prevDot bool) bool {
			if !prevDot && strings.EqualFold(text[start:end], name) {
				count++
			}
			return true
		})
	}
	for _, pou := range p.order {
		for _, body := range pou.bodies {
			if body.ladder == nil {
				countText(body.text)
				continue
			}
			for _, line := range body.ladder.rungs() {
				for _, value := range append(ladderVars(line.node), istTexts(line.node)...) {
					countText(value)
				}
			}
		}
	}
	return count
}
