package sysmac

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Calls of library POUs whose signature changed with a library replacement.
//
// Sysmac Studio stores the pin list of every ladder FB call in the rung. When
// a library FB gets new, removed or renamed parameters, the build fails with
// "The argument of the function or function block caller is different from
// the argument of the definition" until the call is updated (context menu
// Update). ReplaceLibrary does the same as Update for ladder FB calls: the
// pin list is rebuilt from the new definition, connections are kept per pin
// name, new pins stay unconnected (default value) and the Ix numbering of
// the rung and the PouBodySourceHolder link info are shifted (see
// resyncFBPins). A call is reported instead of changed when that would lose
// or move something: a removed pin that is connected, a power flow pin that
// would move to another parameter, a pin that changes between input and
// in-out, or a function call (EN/ENO layout). Structured text (bodies and
// inline ST boxes) is never changed; calls with named arguments that the
// definition no longer has, new in-out parameters that the call does not
// pass and member access to removed parameters are reported.

// libPOUSig is the parameter signature of a library FB or function.
type libPOUSig struct {
	Kind, Namespace, Name string
	Params                []fbParam
}

func (s *libPOUSig) fullName() string {
	if s.Namespace == "" {
		return s.Name
	}
	return s.Namespace + `\` + s.Name
}

func (s *libPOUSig) param(name string) *fbParam {
	for index := range s.Params {
		if strings.EqualFold(s.Params[index].Name, name) {
			return &s.Params[index]
		}
	}
	return nil
}

// libSigSet holds the signatures of the referenced libraries.
type libSigSet struct {
	byKey   map[string]*libPOUSig   // lower-case namespace\name
	byShort map[string][]*libPOUSig // lower-case name
	// local are POU names defined by the project itself; a call without
	// namespace to such a name is not resolved to a library POU.
	local map[string]bool
}

func newLibSigSet() *libSigSet {
	return &libSigSet{byKey: map[string]*libPOUSig{}, byShort: map[string][]*libPOUSig{}, local: map[string]bool{}}
}

func (set *libSigSet) add(sig *libPOUSig) {
	key := strings.ToLower(sig.fullName())
	if _, exists := set.byKey[key]; exists {
		return
	}
	set.byKey[key] = sig
	short := strings.ToLower(sig.Name)
	set.byShort[short] = append(set.byShort[short], sig)
}

// resolve returns the library POU a call or type name refers to: by
// namespace when the name has one (ladder: "\\Ns\Sub\Name"), otherwise by
// the short name when exactly one library POU has it.
func (set *libSigSet) resolve(name string) *libPOUSig {
	if set == nil {
		return nil
	}
	name = strings.TrimLeft(strings.TrimSpace(name), `\`)
	if name == "" {
		return nil
	}
	if strings.Contains(name, `\`) {
		return set.byKey[strings.ToLower(name)]
	}
	if set.local[strings.ToLower(name)] {
		return nil
	}
	if candidates := set.byShort[strings.ToLower(name)]; len(candidates) == 1 {
		return candidates[0]
	}
	return nil
}

// loadLibrarySignatures reads the signatures of every FunctionBlock and
// Function below the given library entities.
func loadLibrarySignatures(doc *oemTextDocument, roots []*oemTextEntity, read func(id string) ([]byte, bool)) *libSigSet {
	set := newLibSigSet()
	for _, root := range roots {
		for _, entity := range subtreeEntities(root) {
			kind := doc.entityType(entity)
			if kind != "FunctionBlock" && kind != "Function" {
				continue
			}
			for _, child := range entity.children {
				if doc.entityType(child) != "Variables" {
					continue
				}
				data, ok := read(child.id)
				if !ok {
					continue
				}
				params, err := parseSignature(data)
				if err != nil {
					continue
				}
				set.add(&libPOUSig{Kind: kind, Namespace: doc.attr(entity, "namespace"), Name: doc.attr(entity, "name"), Params: params})
			}
		}
	}
	for _, entity := range doc.order {
		switch doc.entityType(entity) {
		case "FunctionBlock", "Function":
			if !inLibrary(doc, entity) {
				set.local[strings.ToLower(doc.attr(entity, "name"))] = true
			}
		}
	}
	return set
}

// LibrarySignatureChange describes a library POU whose parameters differ
// between the replaced and the new library.
type LibrarySignatureChange struct {
	POU     string   `json:"pou"`
	Kind    string   `json:"kind"`
	Added   []string `json:"addedParameters,omitempty"`
	Removed []string `json:"removedParameters,omitempty"`
	Changed []string `json:"changedParameters,omitempty"`
}

func paramText(param fbParam) string {
	return param.Name + " (" + param.Group + " " + param.Type + ")"
}

// diffSignatures compares two parameter lists; changed is false when they
// are equal (names, groups, types, edges and order).
func diffSignatures(old, updated []fbParam) (LibrarySignatureChange, bool) {
	var change LibrarySignatureChange
	changed := len(old) != len(updated)
	for index := range old {
		if index < len(updated) && (old[index].Name != updated[index].Name || old[index].Group != updated[index].Group || old[index].Type != updated[index].Type || old[index].Edge != updated[index].Edge) {
			changed = true
		}
	}
	if !changed {
		return change, false
	}
	find := func(list []fbParam, name string) *fbParam {
		for index := range list {
			if strings.EqualFold(list[index].Name, name) {
				return &list[index]
			}
		}
		return nil
	}
	for _, param := range updated {
		previous := find(old, param.Name)
		switch {
		case previous == nil:
			change.Added = append(change.Added, paramText(param))
		case previous.Group != param.Group || previous.Type != param.Type || previous.Edge != param.Edge:
			change.Changed = append(change.Changed, paramText(*previous)+" → "+paramText(param))
		}
	}
	for _, param := range old {
		if find(updated, param.Name) == nil {
			change.Removed = append(change.Removed, paramText(param))
		}
	}
	if len(change.Added) == 0 && len(change.Removed) == 0 && len(change.Changed) == 0 {
		change.Changed = []string{"parameter order"}
	}
	return change, true
}

// LibraryCallUpdate is a call of a library POU whose signature changed.
type LibraryCallUpdate struct {
	// Section is "<POU>/<body>", Location "rung <n>" (0-based, as Sysmac's
	// "Row") or "line <n>" for structured text.
	Section  string   `json:"section"`
	Location string   `json:"location"`
	POU      string   `json:"pou"`
	Instance string   `json:"instance,omitempty"`
	Added    []string `json:"addedPins,omitempty"`
	Removed  []string `json:"removedPins,omitempty"`
	Retyped  []string `json:"retypedPins,omitempty"`
	// Status is "updated" (pin list rebuilt like Sysmac's Update) or
	// "manual" (not changed; Reason says what to do).
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// callPins returns the pins of a ladder FB/function box by lower-case name
// (in-out pins appear on both sides; the connected one wins).
func callPins(fb *ladderJSON) map[string]*ladderJSON {
	pins := map[string]*ladderJSON{}
	for _, side := range []string{"In", "Out"} {
		list := fb.get(side)
		if list == nil {
			continue
		}
		for _, pin := range list.items {
			kind := pin.str("__type")
			if kind != "PRM" && kind != "PF" {
				continue
			}
			key := strings.ToLower(pin.str("Arg"))
			if existing := pins[key]; existing == nil || strings.TrimSpace(existing.str("Var")) == "" {
				pins[key] = pin
			}
		}
	}
	return pins
}

// powerFlowArg returns the Arg of the first PF pin of an FB box side.
func powerFlowArg(fb *ladderJSON, side string) string {
	if list := fb.get(side); list != nil {
		for _, pin := range list.items {
			if pin.str("__type") == "PF" {
				return pin.str("Arg")
			}
		}
	}
	return ""
}

// expectedPowerFlow returns the parameter resyncFBPins puts on the power
// flow of a side: the first BOOL input (In) or output (Out).
func expectedPowerFlow(params []fbParam, side string) string {
	group := "VAR_INPUT"
	if side == "Out" {
		group = "VAR_OUTPUT"
	}
	for _, param := range params {
		if param.Group == group && strings.EqualFold(param.Type, "BOOL") {
			return param.Name
		}
	}
	return ""
}

// callDiff compares a ladder call with a signature.
func callDiff(fb *ladderJSON, sig *libPOUSig) (added, removed, removedBound, retyped []string) {
	pins := callPins(fb)
	for _, param := range sig.Params {
		pin := pins[strings.ToLower(param.Name)]
		if pin == nil {
			added = append(added, param.Name)
			continue
		}
		if pin.str("__type") == "PRM" && pin.str("Type") != param.Type {
			retyped = append(retyped, param.Name+": "+pin.str("Type")+" → "+param.Type)
		}
	}
	var names []string
	for name := range pins {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		pin := pins[name]
		if sig.param(pin.str("Arg")) != nil {
			continue
		}
		removed = append(removed, pin.str("Arg"))
		if binding := strings.TrimSpace(pin.str("Var")); pin.str("__type") == "PRM" && binding != "" {
			removedBound = append(removedBound, pin.str("Arg")+" (connected to "+binding+")")
		}
	}
	return added, removed, removedBound, retyped
}

// callInSync reports whether a ladder FB call has exactly the pin list
// resyncFBPins builds from the signature. ok is false when it cannot tell.
func callInSync(fb *ladderJSON, sig *libPOUSig) (inSync, ok bool) {
	if fb.str("__type") == "F" {
		pins := callPins(fb)
		for _, param := range sig.Params {
			if param.Group != "VAR_OUTPUT" && pins[strings.ToLower(param.Name)] == nil {
				return false, true
			}
		}
		if list := fb.get("In"); list != nil {
			for _, pin := range list.items {
				if pin.str("__type") == "PRM" && sig.param(pin.str("Arg")) == nil {
					return false, true
				}
			}
		}
		return true, true
	}
	clone, err := parseLadderJSON(fb.render())
	if err != nil {
		return false, false
	}
	holder := &ladderJSON{kind: 'a', items: []*ladderJSON{clone}}
	if _, _, err := resyncFBPins(holder, clone, sig.Params, nil); err != nil {
		return false, false
	}
	return clone.render() == fb.render(), true
}

// ladderCalls returns the FB and function boxes of a rung in walk order.
func ladderCalls(node *ladderJSON) []*ladderJSON {
	var result []*ladderJSON
	node.walk(func(object *ladderJSON) {
		switch object.str("__type") {
		case "FB", "F":
			result = append(result, object)
		}
	})
	return result
}

// ---- structured text ----

// maskSTComments blanks out comments and string literals (offsets kept).
func maskSTComments(text string) string {
	masked := []byte(text)
	for index := 0; index < len(masked); {
		switch {
		case strings.HasPrefix(text[index:], "(*"):
			end := strings.Index(text[index+2:], "*)")
			stop := len(text)
			if end >= 0 {
				stop = index + 2 + end + 2
			}
			for position := index; position < stop; position++ {
				if masked[position] != '\n' && masked[position] != '\r' {
					masked[position] = ' '
				}
			}
			index = stop
		case strings.HasPrefix(text[index:], "//"):
			for index < len(masked) && masked[index] != '\n' && masked[index] != '\r' {
				masked[index] = ' '
				index++
			}
		case text[index] == '\'' || text[index] == '"':
			quote := text[index]
			position := index + 1
			for position < len(text) && text[position] != quote {
				if text[position] == '$' {
					position++
				}
				position++
			}
			for fill := index + 1; fill < position && fill < len(masked); fill++ {
				masked[fill] = ' '
			}
			index = position + 1
		default:
			index++
		}
	}
	return string(masked)
}

var stNamedArgPattern = regexp.MustCompile(`(?is)^\s*(?:NOT\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(:=|=>)`)

// stCall is a call with its named arguments.
type stCall struct {
	name   string // instance or function name as written
	offset int
	args   []string
}

// stCalls finds "Name(...)" calls in (masked) structured text.
func stCalls(masked string) []stCall {
	var result []stCall
	scanIdentifiers(masked, func(start, end int, prevDot bool) bool {
		if prevDot {
			return true
		}
		position := end
		for position < len(masked) && strings.ContainsRune(" \t\r\n", rune(masked[position])) {
			position++
		}
		if position >= len(masked) || masked[position] != '(' {
			return true
		}
		depth, argStart := 0, position+1
		call := stCall{name: masked[start:end], offset: start}
		for cursor := position; cursor < len(masked); cursor++ {
			switch masked[cursor] {
			case '(', '[':
				depth++
			case ')', ']':
				depth--
				if depth == 0 {
					call.args = append(call.args, masked[argStart:cursor])
					cursor = len(masked)
				}
			case ',':
				if depth == 1 {
					call.args = append(call.args, masked[argStart:cursor])
					argStart = cursor + 1
				}
			}
		}
		result = append(result, call)
		return true
	})
	return result
}

// stLine returns the 1-based line of offset.
func stLine(text string, offset int) int {
	return strings.Count(text[:offset], "\n") + 1
}

// stSignatureProblems checks calls and member access of library POUs whose
// signature changed in structured text. typeOf resolves a variable name to
// its declared type. It returns one problem per finding with its offset.
func stSignatureProblems(text string, typeOf func(name string) string, changed map[*libPOUSig]*libPOUSig, newSigs *libSigSet) []struct {
	offset  int
	pou     *libPOUSig
	name    string
	problem string
} {
	type finding = struct {
		offset  int
		pou     *libPOUSig
		name    string
		problem string
	}
	var result []finding
	masked := maskSTComments(text)
	resolveInstance := func(name string) *libPOUSig {
		typ := typeOf(name)
		if typ == "" {
			return nil
		}
		if array, _, _, ok := splitTypeName(typ); ok && array != "" {
			return nil
		}
		return newSigs.resolve(typ)
	}
	for _, call := range stCalls(masked) {
		sig := resolveInstance(call.name)
		instance := call.name
		if sig == nil {
			if typeOf(call.name) != "" {
				continue
			}
			if sig = newSigs.resolve(call.name); sig == nil || sig.Kind != "Function" {
				continue
			}
			instance = ""
		}
		old, isChanged := changed[sig]
		if !isChanged {
			continue
		}
		passed := map[string]bool{}
		for _, arg := range call.args {
			match := stNamedArgPattern.FindStringSubmatch(arg)
			if match == nil {
				continue
			}
			passed[strings.ToLower(match[1])] = true
			if sig.param(match[1]) == nil {
				result = append(result, finding{call.offset, sig, instance, fmt.Sprintf("argument %s does not exist in %s any more (removed or renamed); adapt the call by hand", match[1], sig.fullName())})
			}
		}
		if len(passed) == 0 {
			continue // positional or empty call: nothing to check by name
		}
		for _, param := range sig.Params {
			if param.Group == "VAR_IN_OUT" && !passed[strings.ToLower(param.Name)] && (old == nil || old.param(param.Name) == nil) {
				result = append(result, finding{call.offset, sig, instance, fmt.Sprintf("new in-out parameter %s (%s) of %s is not passed; in-out parameters must be connected", param.Name, param.Type, sig.fullName())})
			}
		}
	}
	// Member access to parameters the new definition no longer has.
	scanIdentifiers(masked, func(start, end int, prevDot bool) bool {
		if prevDot || end >= len(masked) || masked[end] != '.' {
			return true
		}
		sig := resolveInstance(masked[start:end])
		if sig == nil {
			return true
		}
		old, isChanged := changed[sig]
		if !isChanged || old == nil {
			return true
		}
		memberEnd := end + 1
		for memberEnd < len(masked) && isSTIdentChar(masked[memberEnd]) {
			memberEnd++
		}
		member := masked[end+1 : memberEnd]
		if member != "" && old.param(member) != nil && sig.param(member) == nil {
			result = append(result, finding{start, sig, masked[start:end], fmt.Sprintf("%s.%s: parameter %s was removed from %s; adapt by hand", masked[start:end], member, member, sig.fullName())})
		}
		return true
	})
	return result
}

// ---- update of the project after a library replacement ----

type libraryCallUpdater struct {
	m       *cloudMigration
	newSigs *libSigSet
	// changed maps a new signature to the old one (nil when the POU has no
	// counterpart in the old library, which is never a changed one).
	changed map[*libPOUSig]*libPOUSig
	updates []LibraryCallUpdate
}

type libraryCallsResult struct {
	changes     []LibrarySignatureChange
	updates     []LibraryCallUpdate
	removedPOUs []string // POUs of the old library missing in the new one
	written     []string
}

// updateLibraryCalls re-synchronises calls in the project (outside the
// libraries) of library POUs whose signature differs between oldSigs and the
// library entity newLibraryID. It writes changed bodies and link info.
func updateLibraryCalls(projectRoot string, oldSigs *libSigSet, newLibraryID string, now time.Time) (libraryCallsResult, error) {
	var result libraryCallsResult
	m := &cloudMigration{options: CloudFBMigrationOptions{ProjectRoot: projectRoot, Now: now}, result: &CloudFBMigrationResult{}, bufferVars: map[string]bool{}, readOnly: true}
	if err := m.load(); err != nil {
		return result, err
	}
	library := m.doc.entities[newLibraryID]
	if library == nil {
		return result, fmt.Errorf("new library entity %s not found", newLibraryID)
	}
	read := func(id string) ([]byte, bool) {
		data, err := os.ReadFile(m.payloadPath(&oemTextEntity{id: id}))
		return data, err == nil
	}
	newSigs := loadLibrarySignatures(m.doc, []*oemTextEntity{library}, read)
	u := &libraryCallUpdater{m: m, newSigs: newSigs, changed: map[*libPOUSig]*libPOUSig{}}
	var keys []string
	for key := range newSigs.byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sig := newSigs.byKey[key]
		old := oldSigs.byKey[key]
		if old == nil {
			continue
		}
		change, differs := diffSignatures(old.Params, sig.Params)
		if !differs {
			continue
		}
		change.POU, change.Kind = sig.fullName(), sig.Kind
		result.changes = append(result.changes, change)
		u.changed[sig] = old
	}
	for key, old := range oldSigs.byKey {
		if newSigs.byKey[key] == nil {
			result.removedPOUs = append(result.removedPOUs, old.fullName())
		}
	}
	sort.Strings(result.removedPOUs)
	if len(u.changed) == 0 {
		return result, nil
	}
	for _, pou := range m.pous {
		for _, body := range pou.bodies {
			switch {
			case body.ladder != nil:
				u.updateLadder(pou, body)
			case body.text != "" && !isEncryptedPayload([]byte(body.text)):
				u.checkST(pou, body.label, "line", body.text, 0)
			}
		}
	}
	result.updates = u.updates
	if err := m.write(); err != nil {
		return result, err
	}
	result.written = m.result.WrittenFiles
	return result, nil
}

func (u *libraryCallUpdater) checkST(pou *cloudPOU, section, unit, text string, rung int) {
	typeOf := func(name string) string { return u.m.varType(pou, name) }
	for _, finding := range stSignatureProblems(text, typeOf, u.changed, u.newSigs) {
		location := fmt.Sprintf("line %d", stLine(text, finding.offset))
		if unit == "rung" {
			location = fmt.Sprintf("rung %d (inline ST line %d)", rung, stLine(text, finding.offset))
		}
		u.updates = append(u.updates, LibraryCallUpdate{Section: section, Location: location, POU: finding.pou.fullName(), Instance: finding.name, Status: "manual", Reason: finding.problem})
	}
}

func (u *libraryCallUpdater) updateLadder(pou *cloudPOU, body *cloudBody) {
	for index, line := range body.ladder.rungs() {
		for _, text := range istTexts(line.node) {
			u.checkST(pou, body.label, "rung", text, index)
		}
		clone, err := parseLadderJSON(line.node.render())
		if err != nil {
			continue
		}
		modified := false
		var shifts []func(int) int
		for _, call := range ladderCalls(clone) {
			sig := u.newSigs.resolve(call.str("Name"))
			old, isChanged := u.changed[sig]
			if sig == nil || !isChanged {
				continue
			}
			if inSync, ok := callInSync(call, sig); ok && inSync {
				continue
			}
			added, removed, removedBound, retyped := callDiff(call, sig)
			update := LibraryCallUpdate{Section: body.label, Location: fmt.Sprintf("rung %d", index), POU: sig.fullName(), Instance: call.str("Var"), Added: added, Removed: removed, Retyped: retyped}
			reason := u.manualReason(call, sig, old, removedBound)
			if reason == "" {
				_, shift, err := resyncFBPins(clone, call, sig.Params, nil)
				if err != nil {
					reason = "the pin list cannot be rebuilt automatically (" + err.Error() + "); select the call in Sysmac Studio and choose Update"
				} else {
					modified = true
					if shift != nil {
						shifts = append(shifts, shift)
					}
					update.Status = "updated"
					var notes []string
					for _, name := range added {
						if param := sig.param(name); param != nil && param.Group == "VAR_IN_OUT" {
							notes = append(notes, "connect the new in-out pin "+name+" ("+param.Type+"); in-out pins cannot stay unconnected")
						}
					}
					if len(retyped) > 0 {
						notes = append(notes, "pin types changed; check that the connected variables still fit")
					}
					update.Reason = strings.Join(notes, "; ")
				}
			}
			if reason != "" {
				update.Status, update.Reason = "manual", reason
			}
			u.updates = append(u.updates, update)
		}
		if modified {
			line.node = clone
			body.changed = true
			if len(shifts) > 0 {
				body.shifts[index] = append(body.shifts[index], shifts...)
			}
		}
	}
}

// manualReason returns why a ladder call is not updated automatically.
func (u *libraryCallUpdater) manualReason(call *ladderJSON, sig, old *libPOUSig, removedBound []string) string {
	if call.str("__type") == "F" {
		return "function call: select the call in Sysmac Studio and choose Update"
	}
	if len(removedBound) > 0 {
		return "connected pins no longer exist in " + sig.fullName() + ": " + strings.Join(removedBound, ", ") + "; decide by hand where the connection belongs (not guessed), then choose Update in Sysmac Studio"
	}
	for _, side := range []string{"In", "Out"} {
		current, expected := powerFlowArg(call, side), expectedPowerFlow(sig.Params, side)
		if !strings.EqualFold(current, expected) {
			return fmt.Sprintf("the power flow pin would move from %q to %q; check the rung and choose Update in Sysmac Studio", current, expected)
		}
	}
	pins := callPins(call)
	for _, param := range sig.Params {
		pin := pins[strings.ToLower(param.Name)]
		if pin == nil || pin.str("__type") != "PRM" {
			continue
		}
		wasInOut := pin.get("IO") != nil
		if wasInOut != (param.Group == "VAR_IN_OUT") && strings.TrimSpace(pin.str("Var")) != "" {
			return fmt.Sprintf("pin %s changes between input/output and in-out; choose Update in Sysmac Studio and check the connection", param.Name)
		}
	}
	return ""
}
