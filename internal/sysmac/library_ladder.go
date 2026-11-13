package sysmac

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
)

// Ladder POUs of a referenced library are stored differently from the .slr.
// When Sysmac Studio imports a library it compiles every ladder POU body:
//
//   - the LadderDiagram payload of the PouBody is replaced by an EncryptedFile
//     that also holds the inline ST sections of the rungs, so the Inline
//     entities below the body get no payload;
//   - the PouBodySourceHolder receives the CXIL compile information
//     (CxilVariableGroup with the POU trackingId, edge/temporary variables
//     and the link table from CXIL lines to ladder elements).
//
// The .slr only carries the plain LadderDiagram, the inline ST payloads and
// an empty PouBodySourceHolder (POUTrackingId 00000000-...). A project with
// that form passes "Rebuild Controller", but the simulator refuses it with
// "Build failed: <POU> {<namespace>}". The encryption cannot be reproduced,
// so ReplaceLibrary
//
//   - reuses the compiled body and compile information of the same POU in the
//     library being replaced when they provably describe the same ladder
//     (same variables, same edge-detecting elements, same inline ST
//     sections per rung, line for line); the POU trackingId in the compile
//     information is rewritten to the new library's POU;
//   - otherwise keeps the plain ladder together with its inline ST payloads
//     (dropping them would empty the inline ST code of the POU) and warns
//     that Sysmac Studio has to compile the library (Replace Library in the
//     GUI) before the simulator accepts the project.

const emptyCxilTrackingID = "00000000-0000-0000-0000-000000000000"

var (
	ladderRungPattern       = regexp.MustCompile(`<DiagramElement z:Id="[^"]*" i:type="Rung">`)
	ladderElementPattern    = regexp.MustCompile(`<DiagramElement z:Id="[^"]*" i:type="([^"]*)">`)
	ladderIDPattern         = regexp.MustCompile(`<Id>([0-9a-fA-F-]{36})</Id>`)
	ladderVariablePattern   = regexp.MustCompile(`<Variable(?: [^>]*)?>([^<]*)</Variable>`)
	ladderSourcePattern     = regexp.MustCompile(`<SourceEntityId>([^<]*)</SourceEntityId>`)
	inlineTextPattern       = regexp.MustCompile(`(?s)<Text>(.*?)</Text>`)
	cxilTrackingPattern     = regexp.MustCompile(`<CxilVariableGroup POUTrackingId="([^"]*)"`)
	cxilDifferentialPattern = regexp.MustCompile(`LadderElementId="([0-9a-fA-F-]{36})" DifferentialCondition="([^"]*)" OperandOrFunctionName="([^"]*)"`)
	cxilLinePattern         = regexp.MustCompile(`ElementUriList="/Rung%5B(\d+)%5D/Element%5B(N\d+)%5D/Line%5B(\d+)%5D"`)
)

func isEncryptedPayload(data []byte) bool {
	return bytes.Contains(data[:min(len(data), 512)], []byte("EncryptedFile"))
}

// hasCxilCompileInfo reports whether a PouBodySourceHolder payload carries
// compile information for a POU.
func hasCxilCompileInfo(data []byte) bool {
	match := cxilTrackingPattern.FindSubmatch(data)
	return match != nil && string(match[1]) != emptyCxilTrackingID && string(match[1]) != ""
}

// ladderFingerprint describes the parts of a ladder body that its CXIL
// compile information depends on.
type ladderFingerprint struct {
	differentials []string // "<id suffix> <condition> <operand>"
	inlineLines   []string // "<rung> <lines>"
}

func (f ladderFingerprint) equal(other ladderFingerprint) bool {
	return strings.Join(f.differentials, "|") == strings.Join(other.differentials, "|") &&
		strings.Join(f.inlineLines, "|") == strings.Join(other.inlineLines, "|")
}

func (f ladderFingerprint) String() string {
	return fmt.Sprintf("edges [%s], inline ST lines per rung [%s]", strings.Join(f.differentials, "; "), strings.Join(f.inlineLines, "; "))
}

// elementSuffix is the part of a ladder element id that Sysmac Studio keeps
// when it re-numbers the elements of an imported library POU.
func elementSuffix(id string) string {
	id = strings.ToLower(strings.ReplaceAll(id, "-", ""))
	if len(id) < 8 {
		return id
	}
	return id[len(id)-8:]
}

func inlineLineCount(payload []byte) (int, bool) {
	match := inlineTextPattern.FindSubmatch(bytes.TrimPrefix(payload, []byte(utf8BOM)))
	if match == nil {
		return 0, false
	}
	text := strings.ReplaceAll(html.UnescapeString(string(match[1])), "\r\n", "\n")
	return strings.Count(text, "\n") + 1, true
}

// fingerprintLadder derives the fingerprint of a plain LadderDiagram.
// inline maps the SourceEntityId of inline ST elements to their payload.
func fingerprintLadder(body []byte, inline map[string][]byte) (ladderFingerprint, error) {
	var result ladderFingerprint
	rungs := ladderRungPattern.FindAllIndex(body, -1)
	for index, rung := range rungs {
		end := len(body)
		if index+1 < len(rungs) {
			end = rungs[index+1][0]
		}
		segment := body[rung[1]:end]
		elements := ladderElementPattern.FindAllSubmatchIndex(segment, -1)
		for position, element := range elements {
			elementEnd := len(segment)
			if position+1 < len(elements) {
				elementEnd = elements[position+1][0]
			}
			kind := string(segment[element[2]:element[3]])
			content := segment[element[1]:elementEnd]
			if kind == "InlineStructuredTextModel" {
				source := ladderSourcePattern.FindSubmatch(content)
				if source == nil {
					return result, fmt.Errorf("rung %d: inline ST element without SourceEntityId", index)
				}
				payload, ok := inline[string(source[1])]
				if !ok {
					return result, fmt.Errorf("rung %d: inline ST %s has no payload", index, source[1])
				}
				lines, ok := inlineLineCount(payload)
				if !ok {
					return result, fmt.Errorf("rung %d: inline ST %s has no text", index, source[1])
				}
				result.inlineLines = append(result.inlineLines, fmt.Sprintf("%d %d", index, lines))
				continue
			}
			for _, condition := range []string{"PositiveTransitionSensing", "NegativeTransitionSensing"} {
				if !bytes.Contains(content, []byte("<"+condition+">true</"+condition+">")) {
					continue
				}
				id := ladderIDPattern.FindSubmatch(content)
				variable := ladderVariablePattern.FindSubmatch(content)
				if id == nil || variable == nil {
					return result, fmt.Errorf("rung %d: edge-detecting %s without id or variable", index, kind)
				}
				result.differentials = append(result.differentials, elementSuffix(string(id[1]))+" "+condition+" "+html.UnescapeString(string(variable[1])))
			}
		}
	}
	sort.Strings(result.differentials)
	sort.Strings(result.inlineLines)
	return result, nil
}

// fingerprintCxil derives the fingerprint of compile information.
func fingerprintCxil(cxil []byte) ladderFingerprint {
	var result ladderFingerprint
	for _, match := range cxilDifferentialPattern.FindAllSubmatch(cxil, -1) {
		result.differentials = append(result.differentials, elementSuffix(string(match[1]))+" "+string(match[2])+" "+html.UnescapeString(string(match[3])))
	}
	maxLine := map[string]int{}
	var keys []string
	for _, match := range cxilLinePattern.FindAllSubmatch(cxil, -1) {
		key := string(match[1]) + "/" + string(match[2])
		var line int
		fmt.Sscanf(string(match[3]), "%d", &line)
		if _, ok := maxLine[key]; !ok {
			keys = append(keys, key)
		}
		maxLine[key] = max(maxLine[key], line)
	}
	for _, key := range keys {
		rung := key[:strings.Index(key, "/")]
		result.inlineLines = append(result.inlineLines, fmt.Sprintf("%s %d", rung, maxLine[key]))
	}
	sort.Strings(result.differentials)
	sort.Strings(result.inlineLines)
	return result
}

// rewriteCxilTracking points compile information at another POU trackingId.
func rewriteCxilTracking(cxil []byte, oldTracking, newTracking string) []byte {
	cxil = bytes.ReplaceAll(cxil, []byte(`POUTrackingId="`+oldTracking+`"`), []byte(`POUTrackingId="`+newTracking+`"`))
	compact := func(id string) string { return strings.ToLower(strings.ReplaceAll(id, "-", "")) }
	return bytes.ReplaceAll(cxil, []byte("_"+compact(oldTracking)+`"`), []byte("_"+compact(newTracking)+`"`))
}

// ladderPOU groups the entities of a ladder POU that matter for compiling.
type ladderPOU struct {
	pou, variables, body, sourceHolder *oemTextEntity
	inline                             []*oemTextEntity
}

func (doc *oemTextDocument) ladderPOUs(root *oemTextEntity) []ladderPOU {
	var result []ladderPOU
	for _, entity := range subtreeEntities(root) {
		kind := doc.entityType(entity)
		if (kind != "FunctionBlock" && kind != "Function") || doc.attr(entity, "subtype") != "Ladder" {
			continue
		}
		pou := ladderPOU{pou: entity}
		for _, child := range entity.children {
			switch doc.entityType(child) {
			case "Variables":
				pou.variables = child
			case "PouBody":
				if pou.body == nil {
					pou.body = child
				}
			}
		}
		if pou.body == nil {
			continue
		}
		for _, child := range pou.body.children {
			switch doc.entityType(child) {
			case "PouBodySourceHolder":
				pou.sourceHolder = child
			case "Inline":
				pou.inline = append(pou.inline, child)
			}
		}
		if pou.sourceHolder != nil {
			result = append(result, pou)
		}
	}
	return result
}

// LibraryCompiledPOU reports how a ladder POU of the new library was stored.
type LibraryCompiledPOU struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	// Mode is "reused_compiled" (compiled body and compile information taken
	// over from the replaced library) or "plain" (plain ladder and inline ST
	// kept; Sysmac Studio must compile the library).
	Mode   string `json:"mode"`
	Reason string `json:"reason,omitempty"`
}

// prepareLibraryLadderPOUs post-processes the payloads of the new library's
// ladder POUs in newFiles (keyed by <new id><ext>). oldPayload reads a payload
// of the replaced library by entity id.
func prepareLibraryLadderPOUs(libraryDoc *oemTextDocument, idMap map[string]string, newFiles map[string][]byte, project *oemTextDocument, old *oemTextEntity, oldPayload func(id string) ([]byte, bool)) []LibraryCompiledPOU {
	var report []LibraryCompiledPOU
	oldPOUs := project.ladderPOUs(old)
	for _, pou := range libraryDoc.ladderPOUs(libraryDoc.root) {
		bodyName := idMap[pou.body.id] + ".xml"
		body, ok := newFiles[bodyName]
		if !ok || isEncryptedPayload(body) {
			continue // already stored in compiled form
		}
		entry := LibraryCompiledPOU{Name: libraryDoc.attr(pou.pou, "name"), Namespace: libraryDoc.attr(pou.pou, "namespace"), Mode: "plain"}
		inline := map[string][]byte{}
		for _, child := range pou.inline {
			if payload, ok := newFiles[idMap[child.id]+".xml"]; ok {
				inline[libraryDoc.attr(child, "name")] = payload
			}
		}
		reason := ""
		fingerprint, err := fingerprintLadder(body, inline)
		if err != nil {
			reason = "ladder body: " + err.Error()
		}
		var donor *ladderPOU
		if reason == "" {
			reason = "the replaced library has no compiled ladder POU with this name and namespace"
			for index := range oldPOUs {
				candidate := &oldPOUs[index]
				if project.attr(candidate.pou, "name") != entry.Name || project.attr(candidate.pou, "namespace") != entry.Namespace || project.entityType(candidate.pou) != libraryDoc.entityType(pou.pou) {
					continue
				}
				donorBody, okBody := oldPayload(candidate.body.id)
				donorCxil, okCxil := oldPayload(candidate.sourceHolder.id)
				switch {
				case !okBody || !isEncryptedPayload(donorBody) || !okCxil || !hasCxilCompileInfo(donorCxil):
					reason = "the POU in the replaced library is not compiled either"
				case !variablesMatch(pou, candidate, idMap, newFiles, oldPayload):
					reason = "the variables differ from the POU in the replaced library"
				case !fingerprint.equal(fingerprintCxil(donorCxil)):
					reason = fmt.Sprintf("the ladder differs from the POU in the replaced library (new: %s; replaced: %s)", fingerprint, fingerprintCxil(donorCxil))
				default:
					donor = candidate
				}
				break
			}
		}
		if donor != nil {
			donorBody, _ := oldPayload(donor.body.id)
			donorCxil, _ := oldPayload(donor.sourceHolder.id)
			oldTracking := project.attr(donor.pou, "trackingId")
			if match := cxilTrackingPattern.FindSubmatch(donorCxil); match != nil {
				oldTracking = string(match[1])
			}
			newFiles[bodyName] = donorBody
			newFiles[idMap[pou.sourceHolder.id]+".xml"] = rewriteCxilTracking(donorCxil, oldTracking, libraryDoc.attr(pou.pou, "trackingId"))
			for _, child := range pou.inline {
				delete(newFiles, idMap[child.id]+".xml") // part of the encrypted body
			}
			entry.Mode, entry.Reason = "reused_compiled", ""
		} else {
			entry.Reason = reason
		}
		report = append(report, entry)
	}
	return report
}

func variablesMatch(pou ladderPOU, donor *ladderPOU, idMap map[string]string, newFiles map[string][]byte, oldPayload func(string) ([]byte, bool)) bool {
	if pou.variables == nil || donor.variables == nil {
		return pou.variables == nil && donor.variables == nil
	}
	newVars, okNew := newFiles[idMap[pou.variables.id]+".xml"]
	oldVars, okOld := oldPayload(donor.variables.id)
	normalize := func(data []byte) string {
		return strings.ReplaceAll(string(bytes.TrimPrefix(data, []byte(utf8BOM))), "\r\n", "\n")
	}
	return okNew && okOld && normalize(newVars) == normalize(oldVars)
}

// dropInlineOfEncryptedBodies removes plain-text Inline payloads of POUs
// whose body is stored encrypted: Sysmac Studio keeps the inline sections in
// the encrypted body and persists no Inline payload. It returns the removed
// payload names (by new id).
func dropInlineOfEncryptedBodies(libraryDoc *oemTextDocument, idMap map[string]string, newFiles map[string][]byte) []string {
	var dropped []string
	for _, entity := range libraryDoc.order {
		if libraryDoc.entityType(entity) != "Inline" {
			continue
		}
		name := idMap[entity.id] + ".xml"
		payload, ok := newFiles[name]
		if !ok || isEncryptedPayload(payload) {
			continue
		}
		pou := entity.parent
		for pou != nil {
			kind := libraryDoc.entityType(pou)
			if kind == "FunctionBlock" || kind == "Function" || kind == "Program" {
				break
			}
			pou = pou.parent
		}
		if pou == nil {
			continue
		}
		for _, candidate := range subtreeEntities(pou) {
			if libraryDoc.entityType(candidate) != "PouBody" {
				continue
			}
			if body, ok := newFiles[idMap[candidate.id]+".xml"]; ok && isEncryptedPayload(body) {
				delete(newFiles, name)
				dropped = append(dropped, name)
				break
			}
		}
	}
	return dropped
}
