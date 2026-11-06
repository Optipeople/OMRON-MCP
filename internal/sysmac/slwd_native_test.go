package sysmac

import (
	"bytes"
	"strings"
	"testing"
)

// nativeSLWD mirrors the layout Sysmac Studio writes: TAB separated fields,
// CRLF line endings, no trailing blank line, values containing spaces,
// parentheses, quotes and backslashes.
const nativeSLWD = "[SLWD version=1.0]\r\n" +
	"_EN=Variables\r\n" +
	"+GN=VAR\tGVT=DefaultGroup\r\n" +
	"++D=ARRAY[0..24] OF Toolbox\\Cloud\\sTagData\tN=Tags\tG=VAR\r\n" +
	"++D=Toolbox\\Cloud\\sWillCfg\tN=Will\tIV=(WillTopic := '', WillMsg := 'a b', WillRetain := False)\tG=VAR\r\n" +
	"++D=STRING[256]\tN=Text\tIV=''\tG=VAR\tCom=Comment with  two spaces and \"quotes\"\r\n" +
	"+GN=VAR_INPUT\tGA=InputOutput\tGVT=EdgeInputOutputGroup\r\n" +
	"++D=BOOL\tN=Enable\tIV=False\tOrd=0\tG=VAR_INPUT\r\n" +
	"++D=BOOL\tN=Clear\tE=UpDirection\tOrd=1\tG=VAR_INPUT\tCom=clear the buffer\r\n" +
	"++D=Toolbox\\Cloud\\sErrors\tN=ErrorLog\tOrd=2\tG=VAR_IN_OUT\tCom=List of errors\tEC=<ECs><EC EK=\"NoOf_Msg\" C=\"No of errors\" /></ECs>\r\n" +
	"++D=BOOL\tN=Done\tOrd=3\tG=VAR_OUTPUT\r\n" +
	"+GN=VAR_EXTERNAL\tGA=External\tGVT=ExternalGroup"

func TestSLWDNativeParseKeepsFullTypesAndInitialValues(t *testing.T) {
	doc, err := ParseSLWDDocument([]byte(nativeSLWD))
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]SLWDVariable{}
	for _, variable := range doc.Variables() {
		vars[variable.Name] = variable
	}
	if got := vars["Tags"].Type; got != `ARRAY[0..24] OF Toolbox\Cloud\sTagData` {
		t.Fatalf("Tags type = %q", got)
	}
	if got := vars["Will"].InitialValue; got != "(WillTopic := '', WillMsg := 'a b', WillRetain := False)" {
		t.Fatalf("Will initial value = %q", got)
	}
	if got := vars["Text"].Comment; got != `Comment with  two spaces and "quotes"` {
		t.Fatalf("Text comment = %q", got)
	}
	if got := vars["Clear"].Attributes; got["E"] != "UpDirection" || got["Ord"] != "1" {
		t.Fatalf("Clear attributes = %#v", got)
	}
	if got := vars["ErrorLog"].Attributes["EC"]; got != `<ECs><EC EK="NoOf_Msg" C="No of errors" /></ECs>` {
		t.Fatalf("ErrorLog EC = %q", got)
	}
	if len(vars) != 7 {
		t.Fatalf("variables = %d", len(vars))
	}
}

func TestSLWDNativeRoundTripIsByteIdentical(t *testing.T) {
	for _, input := range []string{nativeSLWD, nativeSLWD + "\r\n", "ï»¿" + nativeSLWD} {
		doc, err := ParseSLWDDocument([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		out, err := MarshalSLWDDocument(doc)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out, []byte(input)) {
			t.Fatalf("round trip changed bytes:\n%q\n%q", input, out)
		}
	}
}

func TestSLWDNativeUpdateTouchesOnlyChangedField(t *testing.T) {
	doc, err := ParseSLWDDocument([]byte(nativeSLWD))
	if err != nil {
		t.Fatal(err)
	}
	comment := "new comment"
	if err := doc.PatchVariable(SLWDVariablePatch{Name: "Clear", Comment: &comment}); err != nil {
		t.Fatal(err)
	}
	out, _ := MarshalSLWDDocument(doc)
	want := strings.Replace(nativeSLWD, "Com=clear the buffer", "Com=new comment", 1)
	if string(out) != want {
		t.Fatalf("patched document differs:\n%q\n%q", want, out)
	}
}

func TestSLWDNativeUpdateInsertsAndRemovesFieldsInPlace(t *testing.T) {
	doc, err := ParseSLWDDocument([]byte(nativeSLWD))
	if err != nil {
		t.Fatal(err)
	}
	iv := "16#FF"
	empty := ""
	if err := doc.PatchVariable(SLWDVariablePatch{Name: "Done", InitialValue: &iv}); err != nil {
		t.Fatal(err)
	}
	if err := doc.PatchVariable(SLWDVariablePatch{Name: "Text", Comment: &empty}); err != nil {
		t.Fatal(err)
	}
	// UpdateVariable with only a type keeps every other field.
	if err := doc.UpdateVariable(SLWDVariable{Name: "Tags", Type: `ARRAY[0..49] OF Toolbox\Cloud\sTagData`}); err != nil {
		t.Fatal(err)
	}
	out, _ := MarshalSLWDDocument(doc)
	want := nativeSLWD
	want = strings.Replace(want, "++D=BOOL\tN=Done\tOrd=3", "++D=BOOL\tN=Done\tIV=16#FF\tOrd=3", 1)
	want = strings.Replace(want, "\tG=VAR\tCom=Comment with  two spaces and \"quotes\"", "\tG=VAR", 1)
	want = strings.Replace(want, "ARRAY[0..24]", "ARRAY[0..49]", 1)
	if string(out) != want {
		t.Fatalf("patched document differs:\n%q\n%q", want, out)
	}
}

func TestSLWDNativeAddRenameDelete(t *testing.T) {
	doc, err := ParseSLWDDocument([]byte(nativeSLWD))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.AddVariable(SLWDVariable{Name: "Counter", Type: "UDINT", InitialValue: "0", Comment: "parts made"}); err != nil {
		t.Fatal(err)
	}
	if err := doc.AddVariable(SLWDVariable{Name: "Reset", Type: "BOOL", Group: "VAR_INPUT"}); err != nil {
		t.Fatal(err)
	}
	if err := doc.AddVariable(SLWDVariable{Name: "Ext", Type: "BOOL", Group: "VAR_EXTERNAL"}); err != nil {
		t.Fatal(err)
	}
	if err := doc.RenameVariable("Enable", "Execute"); err != nil {
		t.Fatal(err)
	}
	if err := doc.DeleteVariable("Will"); err != nil {
		t.Fatal(err)
	}
	out, _ := MarshalSLWDDocument(doc)
	want := nativeSLWD
	want = strings.Replace(want, "++D=Toolbox\\Cloud\\sWillCfg\tN=Will\tIV=(WillTopic := '', WillMsg := 'a b', WillRetain := False)\tG=VAR\r\n", "", 1)
	want = strings.Replace(want, "\tCom=Comment with  two spaces and \"quotes\"\r\n", "\tCom=Comment with  two spaces and \"quotes\"\r\n++D=UDINT\tN=Counter\tIV=0\tG=VAR\tCom=parts made\r\n", 1)
	want = strings.Replace(want, "N=Enable", "N=Execute", 1)
	want = strings.Replace(want, "++D=BOOL\tN=Done\tOrd=3\tG=VAR_OUTPUT\r\n", "++D=BOOL\tN=Done\tOrd=3\tG=VAR_OUTPUT\r\n++D=BOOL\tN=Reset\tOrd=4\tG=VAR_INPUT\r\n", 1)
	want += "\r\n++D=BOOL\tN=Ext\tG=VAR_EXTERNAL"
	if string(out) != want {
		t.Fatalf("document differs:\n%q\n%q", want, out)
	}
	if _, err := ParseSLWDDocument(out); err != nil {
		t.Fatal(err)
	}
}

func TestSLWDNativeRejectsTabsInValues(t *testing.T) {
	doc, err := ParseSLWDDocument([]byte(nativeSLWD))
	if err != nil {
		t.Fatal(err)
	}
	bad := "a\tb"
	if err := doc.PatchVariable(SLWDVariablePatch{Name: "Done", Comment: &bad}); err == nil {
		t.Fatal("expected TAB in value to be rejected")
	}
}
