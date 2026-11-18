package sysmac

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Synthetic project whose library FB Demo\SvcFB gets a new signature: the
// unconnected input Gone is removed, NewIn (UINT) and Mode (BOOL, rising
// edge) are added. No real project data is used.

const signatureRungUnbound = `{"CLs":[{"__type":"LD","Var":"x"},{"__type":"FB","In":[{"__type":"PF","Arg":"Enable"},{"__type":"PRM","Arg":"Gone","Ix":1,"Type":"INT","Var":""},{"__type":"PRM","Arg":"Speed","Ix":2,"Type":"INT","Var":"spd"}],"Ix":4,"Name":"\\\\Demo\\SvcFB","Out":[{"__type":"PF","Arg":"Done"},{"__type":"PRM","Arg":"Value","Ix":3,"Type":"INT","Var":"val"}],"UD":true,"X":1,"Var":"Svc"}],"LRI":5,"RRI":6,"VLs":[]}`

// Sysmac's Update keeps Speed/Value, drops the unconnected Gone and adds
// NewIn and Mode unconnected; the Ix values above the pins move by +1.
const signatureRungUpdated = `{"CLs":[{"__type":"LD","Var":"x"},{"__type":"FB","In":[{"__type":"PF","Arg":"Enable"},{"__type":"PRM","Arg":"Speed","Ix":1,"Type":"INT","Var":"spd"},{"__type":"PRM","Arg":"NewIn","Ix":2,"Type":"UINT","Var":""},{"__type":"PRM","Arg":"Mode","Ix":3,"Type":"BOOL","Up":true,"Var":""}],"Ix":5,"Name":"\\\\Demo\\SvcFB","Out":[{"__type":"PF","Arg":"Done"},{"__type":"PRM","Arg":"Value","Ix":4,"Type":"INT","Var":"val"}],"UD":true,"X":1,"Var":"Svc"}],"LRI":6,"RRI":7,"VLs":[]}`

// The removed input Gone is connected: the call must not be changed.
const signatureRungBound = `{"CLs":[{"__type":"LD","Var":"x"},{"__type":"FB","In":[{"__type":"PF","Arg":"Enable"},{"__type":"PRM","Arg":"Gone","Ix":1,"Type":"INT","Var":"g"},{"__type":"PRM","Arg":"Speed","Ix":2,"Type":"INT","Var":"spd"}],"Ix":4,"Name":"\\\\Demo\\SvcFB","Out":[{"__type":"PF","Arg":"Done"},{"__type":"PRM","Arg":"Value","Ix":3,"Type":"INT","Var":"val"}],"UD":true,"X":1,"Var":"Svc2"}],"LRI":5,"RRI":6,"VLs":[]}`

const signatureST = "Svc3(Enable := TRUE, Gone := 5, Speed := 1);\r\nv := Svc3.Gone;\r\n(* Svc3(Gone := 1); *)\r\nSvc3(Enable := x, Speed := spd);\r\n"

func writeSignatureFixture(t *testing.T) (project, library string) {
	t.Helper()
	body := func(id, subtype, name string) string {
		return cloudEntity("PouBody", subtype, id, name, "", cloudEntity("PouBodySourceHolder", "", id+"-pbsh", "PouBodySourceHolder", "", ""))
	}
	program := cloudEntity("Program", "MultipartLadder", "prog", "Main", "",
		cloudEntity("Variables", "Ladder", "prog-vars", "Variables", "", "")+body("b-lad", "Ladder", "Calls")+body("b-st", "StructuredText", "Text"))
	oldLibrary := cloudEntity("Library", "Nex", "oldlib", "OldLib_1_0", "",
		cloudEntity("FunctionBlock", "StructuredText", "oldfb", "SvcFB", ` namespace="Demo"`, cloudEntity("Variables", "StructuredTextFunctionBlock", "oldfb-vars", "Variables", "", "")))
	oem := `<sysmac schemaVersion="1">` + cloudEntity("Solution", "", "sol", "Demo", "",
		program+cloudEntity("ReferredLibraries", "Nex", "refs", "Library", "", oldLibrary)) + `</sysmac>`
	project = filepath.Join(t.TempDir(), "sol")
	files := map[string]string{
		"sol.oem":    oem,
		"oldlib.xml": crlf(utf8BOM + testXMLHeader + "<data>\n  <data>\n" + libraryModel("    ", "OldLib", "1.0.0", "old-library-id", `C:\old\OldLib_1_0.slr`) + "\n  </data>\n</data>"),
		"refs.xml":   referredPayload(libraryModel("            ", "OldLib", "1.0.0", "old-library-id", `C:\old\OldLib_1_0.slr`)),
		"oldfb-vars.xml": slwd("+GN=VAR_INPUT\tGA=InputOutput\tGVT=EdgeInputOutputGroup",
			"++D=BOOL\tN=Enable\tOrd=0\tG=VAR_INPUT",
			"++D=INT\tN=Gone\tOrd=1\tG=VAR_INPUT",
			"++D=INT\tN=Speed\tOrd=2\tG=VAR_INPUT",
			"++D=BOOL\tN=Done\tOrd=3\tG=VAR_OUTPUT",
			"++D=INT\tN=Value\tOrd=4\tG=VAR_OUTPUT"),
		"prog-vars.xml": slwd("+GN=VAR\tGVT=DefaultGroup",
			"++D=Demo\\SvcFB\tN=Svc\tG=VAR",
			"++D=Demo\\SvcFB\tN=Svc2\tG=VAR",
			"++D=SvcFB\tN=Svc3\tG=VAR",
			"++D=BOOL\tN=x\tG=VAR",
			"++D=INT\tN=spd\tG=VAR",
			"++D=INT\tN=val\tG=VAR",
			"++D=INT\tN=g\tG=VAR",
			"++D=INT\tN=v\tG=VAR"),
		"b-lad.xml":      utf8BOM + signatureRungUnbound + "\r\n" + signatureRungBound + "\r\n",
		"b-lad-pbsh.xml": utf8BOM + `<?xml version="1.0" encoding="utf-8"?><data><ElementUriList ElementUriList="/Rung%5B0%5D/Element%5BN4%5D" /><ElementUriList ElementUriList="/Rung%5B1%5D/Element%5BN4%5D" /></data>`,
		"b-st.xml":       signatureST,
	}
	writeFiles(t, project, files)

	library = filepath.Join(t.TempDir(), "NewLib_2_0")
	libraryOEM := `<sysmac schemaVersion="3">` + cloudEntity("Library", "Nex", "lib-root", "NewLib", "",
		cloudEntity("FunctionBlock", "StructuredText", "lib-fb", "SvcFB", ` namespace="Demo"`, cloudEntity("Variables", "StructuredTextFunctionBlock", "lib-fb-vars", "Variables", "", ""))) + `</sysmac>`
	writeFiles(t, library, map[string]string{
		"lib-root.oem": libraryOEM,
		"lib-root.xml": crlf(testXMLHeader + "<data>\n  <data>\n" + libraryModel("    ", "NewLib", "2.0.0", "new-library-id", "") + "\n  </data>\n</data>"),
		"lib-fb-vars.xml": slwd("+GN=VAR_INPUT\tGA=InputOutput\tGVT=EdgeInputOutputGroup",
			"++D=BOOL\tN=Enable\tOrd=0\tG=VAR_INPUT",
			"++D=INT\tN=Speed\tOrd=1\tG=VAR_INPUT",
			"++D=UINT\tN=NewIn\tOrd=2\tG=VAR_INPUT",
			"++D=BOOL\tN=Mode\tE=UpDirection\tOrd=3\tG=VAR_INPUT",
			"++D=BOOL\tN=Done\tOrd=4\tG=VAR_OUTPUT",
			"++D=INT\tN=Value\tOrd=5\tG=VAR_OUTPUT"),
	})
	return project, library
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReplaceLibraryUpdatesCallsOfChangedSignature(t *testing.T) {
	project, library := writeSignatureFixture(t)
	result, err := ReplaceLibrary(LibraryReplaceOptions{ProjectRoot: project, LibraryPath: library, Now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SignatureChanges) != 1 {
		t.Fatalf("signature changes = %+v", result.SignatureChanges)
	}
	change := result.SignatureChanges[0]
	if change.POU != `Demo\SvcFB` || strings.Join(change.Added, ",") != "NewIn (VAR_INPUT UINT),Mode (VAR_INPUT BOOL)" || strings.Join(change.Removed, ",") != "Gone (VAR_INPUT INT)" {
		t.Fatalf("signature change = %+v", change)
	}
	byLocation := map[string]LibraryCallUpdate{}
	for _, update := range result.CallUpdates {
		t.Logf("%+v", update)
		byLocation[update.Section+" "+update.Location] = update
	}
	updated := byLocation["Main/Calls rung 0"]
	if updated.Status != "updated" || updated.Instance != "Svc" || strings.Join(updated.Added, ",") != "NewIn,Mode" || strings.Join(updated.Removed, ",") != "Gone" {
		t.Fatalf("rung 0 = %+v", updated)
	}
	if manual := byLocation["Main/Calls rung 1"]; manual.Status != "manual" || !strings.Contains(manual.Reason, "Gone (connected to g)") {
		t.Fatalf("rung 1 = %+v", manual)
	}
	if manual := byLocation["Main/Text line 1"]; manual.Status != "manual" || !strings.Contains(manual.Reason, "argument Gone") {
		t.Fatalf("ST line 1 = %+v", manual)
	}
	if manual := byLocation["Main/Text line 2"]; manual.Status != "manual" || !strings.Contains(manual.Reason, "Svc3.Gone") {
		t.Fatalf("ST line 2 = %+v", manual)
	}
	if len(result.CallUpdates) != 4 {
		t.Fatalf("calls in comments or matching calls reported: %+v", result.CallUpdates)
	}
	if want := utf8BOM + signatureRungUpdated + "\r\n" + signatureRungBound + "\r\n"; readFixture(t, project, "b-lad.xml") != want {
		t.Fatalf("ladder body:\n%s\nwant\n%s", readFixture(t, project, "b-lad.xml"), want)
	}
	if got := readFixture(t, project, "b-lad-pbsh.xml"); !strings.Contains(got, "/Rung%5B0%5D/Element%5BN5%5D") || !strings.Contains(got, "/Rung%5B1%5D/Element%5BN4%5D") {
		t.Fatalf("link info not shifted: %s", got)
	}
	if readFixture(t, project, "b-st.xml") != signatureST {
		t.Fatal("structured text was changed")
	}
	written := strings.Join(result.WrittenFiles, ",")
	if !strings.Contains(written, "b-lad.xml") || !strings.Contains(written, "b-lad-pbsh.xml") || strings.Contains(written, "b-st.xml") {
		t.Fatalf("written files = %v", result.WrittenFiles)
	}
	manualWarnings := 0
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "must be adapted by hand") {
			manualWarnings++
		}
	}
	if manualWarnings != 3 {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

func TestCompareClassifiesChangedLibrarySignatures(t *testing.T) {
	before, library := writeSignatureFixture(t)
	after := filepath.Join(t.TempDir(), "sol")
	if err := CopySolutionDirectory(before, after); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaceLibrary(LibraryReplaceOptions{ProjectRoot: after, LibraryPath: library}); err != nil {
		t.Fatal(err)
	}
	report, err := CompareProjects(before, after)
	if err != nil {
		t.Fatal(err)
	}
	byItem := map[string]CompareFinding{}
	for _, finding := range report.Findings {
		t.Logf("%s %s %s %v: %s", finding.Class, finding.Area, finding.Item, finding.Rules, finding.Detail)
		byItem[finding.Item] = finding
	}
	if finding := byItem["Main/Calls rung 0"]; finding.Class != CompareExpected || strings.Join(finding.Rules, ",") != "LIB-SIGNATURE" || !strings.Contains(finding.Detail, "nye pins Mode, NewIn uden forbindelse") || !strings.Contains(finding.Detail, "fjernede pins Gone") {
		t.Fatalf("updated call = %+v", finding)
	}
	// Unchanged bytes, but the call no longer fits the definition: this
	// is the build error Sysmac reports, so it must not be "identical".
	if finding := byItem["Main/Calls rung 1"]; finding.Class != CompareUnexplained || !strings.Contains(finding.Detail, "mangler pins NewIn, Mode") || !strings.Contains(finding.Detail, "Gone") {
		t.Fatalf("stale call = %+v", finding)
	}
	if finding := byItem["Main/Text"]; finding.Class != CompareUnexplained || !strings.Contains(finding.Detail, "linje 1: argument Gone") || !strings.Contains(finding.Detail, "linje 2:") || strings.Contains(finding.Detail, "linje 3") {
		t.Fatalf("structured text = %+v", finding)
	}
}

func TestCallInSyncAndManualReasons(t *testing.T) {
	sig := &libPOUSig{Kind: "FunctionBlock", Namespace: "Demo", Name: "SvcFB", Params: []fbParam{
		{Name: "Enable", Group: "VAR_INPUT", Type: "BOOL"},
		{Name: "Speed", Group: "VAR_INPUT", Type: "INT"},
		{Name: "NewIn", Group: "VAR_INPUT", Type: "UINT"},
		{Name: "Mode", Group: "VAR_INPUT", Type: "BOOL", Edge: "UpDirection"},
		{Name: "Done", Group: "VAR_OUTPUT", Type: "BOOL"},
		{Name: "Value", Group: "VAR_OUTPUT", Type: "INT"},
	}}
	call := func(rung string) *ladderJSON {
		node, err := parseLadderJSON(rung)
		if err != nil {
			t.Fatal(err)
		}
		return ladderCalls(node)[0]
	}
	if inSync, ok := callInSync(call(signatureRungUpdated), sig); !ok || !inSync {
		t.Fatal("updated call reported out of sync")
	}
	if inSync, ok := callInSync(call(signatureRungUnbound), sig); !ok || inSync {
		t.Fatal("old call reported in sync")
	}
	u := &libraryCallUpdater{}
	if reason := u.manualReason(call(signatureRungUnbound), sig, nil, nil); reason != "" {
		t.Fatalf("unexpected manual reason %q", reason)
	}
	// A new BOOL input before Enable would take over the power flow.
	moved := &libPOUSig{Name: "SvcFB", Params: append([]fbParam{{Name: "Start", Group: "VAR_INPUT", Type: "BOOL"}}, sig.Params...)}
	if reason := u.manualReason(call(signatureRungUnbound), moved, nil, nil); !strings.Contains(reason, `power flow pin would move from "Enable" to "Start"`) {
		t.Fatalf("reason = %q", reason)
	}
	if _, changed := diffSignatures(sig.Params, sig.Params); changed {
		t.Fatal("equal signatures reported as changed")
	}
}

func TestSTCallsIgnoreCommentsAndStrings(t *testing.T) {
	masked := maskSTComments("a := 'Svc(x := 1)'; // Svc(y := 2)\r\n(* Svc(z := 3) *) Svc(Enable := TRUE, In := f(1, 2));")
	calls := stCalls(masked)
	var names []string
	for _, call := range calls {
		names = append(names, call.name)
	}
	if strings.Join(names, ",") != "Svc,f" || len(calls[0].args) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
}
