package sysmac

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A ladder FB with one rising-edge contact on PWM and one inline ST section
// (source "inl-src") in rung 1, as stored in an .slr (element prefix aaaa...).
const testLadderBody = `<LadderDiagram z:Id="1" xmlns:i="http://www.w3.org/2001/XMLSchema-instance" xmlns:z="http://schemas.microsoft.com/2003/10/Serialization/"><Elements z:Size="2">` +
	`<DiagramElement z:Id="2" i:type="Rung"><Id>aaaaaaaa-aaaa-aaaa-aaaa-aaaa00000001</Id><Elements z:Size="1">` +
	`<DiagramElement z:Id="3" i:type="Contact"><Id>aaaaaaaa-aaaa-aaaa-aaaa-aaaa00000002</Id><ConnectionPoints z:Size="0"><ConnectionPoint><Id>00000000-0000-0000-0000-000000000000</Id></ConnectionPoint></ConnectionPoints>` +
	`<NegativeTransitionSensing>false</NegativeTransitionSensing><NormallyClosed>false</NormallyClosed><PositiveTransitionSensing>true</PositiveTransitionSensing><Variable z:Id="4">PWM</Variable></DiagramElement>` +
	`<DiagramElement z:Id="5" i:type="Contact"><Id>aaaaaaaa-aaaa-aaaa-aaaa-aaaa00000003</Id><NegativeTransitionSensing>false</NegativeTransitionSensing><PositiveTransitionSensing>false</PositiveTransitionSensing><Variable z:Id="6">Enable</Variable></DiagramElement>` +
	`</Elements></DiagramElement>` +
	`<DiagramElement z:Id="7" i:type="Rung"><Id>aaaaaaaa-aaaa-aaaa-aaaa-aaaa00000004</Id><Elements z:Size="1">` +
	`<DiagramElement z:Id="8" i:type="InlineStructuredTextModel"><Id>aaaaaaaa-aaaa-aaaa-aaaa-aaaa00000005</Id><SourceEntityId>inl-src</SourceEntityId></DiagramElement>` +
	`</Elements></DiagramElement></Elements></LadderDiagram>`

const testInlineST = "\ufeff<?xml version=\"1.0\" encoding=\"utf-8\"?><data><StructuredText><Text>IF a &lt;&gt; b THEN\r\n\tc := 1;\r\nEND_IF;</Text></StructuredText></data>"

// Compile information as Sysmac Studio stores it for the same ladder after an
// import that numbered the elements with prefix bbbb... (old POU trackingId t-oldsched).
const testCompiledCxil = "\ufeff<?xml version=\"1.0\" encoding=\"utf-8\"?><data><SectionUsingMCOrMcr>false</SectionUsingMCOrMcr><CxilVariableGroup POUTrackingId=\"t-oldsched\" />" +
	`<CxilVariable CxilVariableType="Differential" DataType="BOOL" Name="_Diff_bbbbbbbbbbbbbbbbbbbbbbbb00000002" InitialValue="TRUE"><Identifier Type="DifferetialIndentifier" LadderElementId="bbbbbbbb-bbbb-bbbb-bbbb-bbbb00000002" DifferentialCondition="PositiveTransitionSensing" OperandOrFunctionName="PWM" /></CxilVariable>` +
	`<CxilLinkInformationListTable><CxilLinkInformationList Offset="14" FileNameWithoutExtension="FB_toldsched"><ElementUriList ElementUriList="/Rung%5B0%5D/Element%5BN0%5D" />` +
	`<ElementUriList ElementUriList="/Rung%5B1%5D/Element%5BN0%5D/Line%5B1%5D" /><ElementUriList ElementUriList="/Rung%5B1%5D/Element%5BN0%5D/Line%5B2%5D" /><ElementUriList ElementUriList="/Rung%5B1%5D/Element%5BN0%5D/Line%5B3%5D" />` +
	`</CxilLinkInformationList></CxilLinkInformationListTable><CxilDivisionInformation /></data>`

const testEmptyCxil = "\ufeff<?xml version=\"1.0\" encoding=\"utf-8\"?><data><SectionUsingMCOrMcr>false</SectionUsingMCOrMcr><CxilVariableGroup POUTrackingId=\"00000000-0000-0000-0000-000000000000\" /><CxilLinkInformationListTable /><CxilDivisionInformation /></data>"

const testEncryptedBody = "\ufeff<?xml version=\"1.0\" encoding=\"utf-16\"?>\r\n<EncryptedFile>\r\n  <EncryptionLock>LOCK</EncryptionLock>\r\n  <Body>c2VjcmV0</Body>\r\n</EncryptedFile>"

const testLadderVars = "[SLWD version=1.0]\r\n_EN=Variables\r\n+GN=VAR\tGVT=DefaultGroup\r\n++D=BOOL\tN=PWM\tG=VAR\r\n"

func ladderEntity(indent, prefix, tracking string) string {
	lines := []string{
		`<Entity type="FunctionBlock" subtype="Ladder" id="` + prefix + `sched" name="fb_sched" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" namespace="Demo" trackingId="` + tracking + `">`,
		`  <AccessInfos />`,
		`  <ChildEntities>`,
		`    <Entity type="Variables" subtype="LadderFunctionBlock" id="` + prefix + `vars" name="Variables" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-svars">`,
		`      <AccessInfos />`,
		`      <ChildEntities />`,
		`    </Entity>`,
		`    <Entity type="PouBody" subtype="Ladder" id="` + prefix + `body" name="LadderBody" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-sbody">`,
		`      <AccessInfos />`,
		`      <ChildEntities>`,
		`        <Entity type="PouBodySourceHolder" subtype="" id="` + prefix + `cxil" name="PouBodySourceHolder" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-scxil">`,
		`          <AccessInfos />`,
		`          <ChildEntities />`,
		`        </Entity>`,
		`        <Entity type="Inline" subtype="StructuredText" id="` + prefix + `inl" name="inl-src" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-sinl">`,
		`          <AccessInfos />`,
		`          <ChildEntities />`,
		`        </Entity>`,
		`      </ChildEntities>`,
		`    </Entity>`,
		`  </ChildEntities>`,
		`</Entity>`,
	}
	return indent + strings.Join(lines, "\n"+indent)
}

func writeLadderFixture(t *testing.T, inline string) (root, library string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "sol")
	library = filepath.Join(t.TempDir(), "NewLib_2_0")
	for _, dir := range []string{root, library} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	project := `<sysmac schemaVersion="1">
  <Entity type="Solution" subtype="" id="sol" name="Demo" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-sol">
    <AccessInfos />
    <ChildEntities>
      <Entity type="ReferredLibraries" subtype="Nex" id="refs" name="Library" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-refs">
        <AccessInfos />
        <ChildEntities>
          <Entity type="Library" subtype="Nex" id="oldlib" name="OldLib_1_0" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-oldlib">
            <AccessInfos />
            <ChildEntities>
` + ladderEntity("              ", "old", "t-oldsched") + `
            </ChildEntities>
          </Entity>
        </ChildEntities>
      </Entity>
    </ChildEntities>
  </Entity>
</sysmac>`
	model := libraryModel("            ", "OldLib", "1.0.0", "old-library-id", `C:\old\OldLib_1_0.slr`)
	projectFiles := map[string]string{
		"sol.oem":     crlf(utf8BOM + testXMLHeader + project),
		"oldlib.xml":  crlf(utf8BOM + testXMLHeader + "<data>\n  <data>\n" + model + "\n  </data>\n</data>"),
		"refs.xml":    referredPayload(model),
		"oldvars.xml": testLadderVars,
		"oldbody.xml": testEncryptedBody,
		"oldcxil.xml": testCompiledCxil,
	}
	libraryOEM := `<sysmac schemaVersion="3">
  <Entity type="Library" subtype="Nex" id="lib-root" name="NewLib[ACME]" version="0" dateCreated="02/02/2025 09:00:00" dateLastModified="02/02/2025 09:00:00" trackingId="t-lib">
    <AccessInfos />
    <ChildEntities>
` + ladderEntity("      ", "new", "t-newsched") + `
    </ChildEntities>
  </Entity>
</sysmac>`
	libraryFiles := map[string]string{
		"lib-root.oem": crlf(testXMLHeader + libraryOEM),
		"lib-root.xml": crlf(testXMLHeader + "<data>\n  <data>\n" + libraryModel("    ", "NewLib", "2.0.0", "new-library-id", "") + "\n  </data>\n</data>"),
		"newvars.xml":  testXMLHeader + `<data><VariableGroup Name="VAR" Attribute="" ViewType="DefaultGroup"><Variable DataTypeName="BOOL" Name="PWM" Address="" Retained="false" InitialValue="" Comment="" Edge="NoEdge" Order="" IsReadOnly="false" IsConstant="false" Group="VAR" /></VariableGroup></data>`,
		"newbody.xml":  testLadderBody,
		"newcxil.xml":  testEmptyCxil,
		"newinl.xml":   inline,
	}
	for dir, files := range map[string]map[string]string{root: projectFiles, library: libraryFiles} {
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root, library
}

func ladderPayloads(t *testing.T, root string) (body, cxil, inline string, inlineErr error) {
	t.Helper()
	inspection, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) (string, error) {
		for _, entity := range inspection.Entities {
			if entity.Name == name {
				data, err := os.ReadFile(filepath.Join(root, entity.ID+".xml"))
				return string(data), err
			}
		}
		t.Fatalf("entity %s missing", name)
		return "", nil
	}
	body, err = read("LadderBody")
	if err != nil {
		t.Fatal(err)
	}
	cxil, err = read("PouBodySourceHolder")
	if err != nil {
		t.Fatal(err)
	}
	inline, inlineErr = read("inl-src")
	return
}

func TestFingerprintLadderMatchesCompileInformation(t *testing.T) {
	ladder, err := fingerprintLadder([]byte(testLadderBody), map[string][]byte{"inl-src": []byte(testInlineST)})
	if err != nil {
		t.Fatal(err)
	}
	compiled := fingerprintCxil([]byte(testCompiledCxil))
	if !ladder.equal(compiled) {
		t.Fatalf("ladder %s\ncompiled %s", ladder, compiled)
	}
	if strings.Join(ladder.differentials, "|") != "00000002 PositiveTransitionSensing PWM" || strings.Join(ladder.inlineLines, "|") != "1 3" {
		t.Fatalf("fingerprint = %s", ladder)
	}
	if _, err := fingerprintLadder([]byte(testLadderBody), nil); err == nil {
		t.Fatal("missing inline payload not reported")
	}
	if hasCxilCompileInfo([]byte(testEmptyCxil)) || !hasCxilCompileInfo([]byte(testCompiledCxil)) {
		t.Fatal("hasCxilCompileInfo")
	}
}

// Sysmac Studio compiles library ladder POUs on import; a plain .slr ladder
// with empty compile information makes the simulator report
// "Build failed: <POU>". The compiled form of the replaced library is reused
// when it describes the same ladder.
func TestReplaceLibraryReusesCompiledLadderOfReplacedLibrary(t *testing.T) {
	root, library := writeLadderFixture(t, testInlineST)
	result, err := ReplaceLibrary(LibraryReplaceOptions{ProjectRoot: root, LibraryPath: library, NewName: "NewLib_2_0", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.LadderPOUs) != 1 || result.LadderPOUs[0].Mode != "reused_compiled" {
		t.Fatalf("ladder POUs = %+v", result.LadderPOUs)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "uncompiled") {
			t.Fatalf("unexpected warning %q", warning)
		}
	}
	body, cxil, _, inlineErr := ladderPayloads(t, root)
	if body != testEncryptedBody {
		t.Fatalf("body = %q", body)
	}
	if !strings.Contains(cxil, `POUTrackingId="t-newsched"`) || strings.Contains(cxil, "t-oldsched") || !strings.Contains(cxil, `FileNameWithoutExtension="FB_tnewsched"`) {
		t.Fatalf("compile information not moved to the new POU: %q", cxil)
	}
	if !strings.Contains(cxil, `LadderElementId="bbbbbbbb-bbbb-bbbb-bbbb-bbbb00000002"`) {
		t.Fatal("compile information must keep the element ids of the encrypted body")
	}
	if !os.IsNotExist(inlineErr) {
		t.Fatalf("inline ST payload of an encrypted body must not be stored (err %v)", inlineErr)
	}
}

func TestReplaceLibraryKeepsInlineSTWhenLadderStaysPlain(t *testing.T) {
	changed := strings.Replace(testInlineST, "END_IF;", "END_IF;\r\nd := 2;", 1)
	root, library := writeLadderFixture(t, changed)
	result, err := ReplaceLibrary(LibraryReplaceOptions{ProjectRoot: root, LibraryPath: library, NewName: "NewLib_2_0", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.LadderPOUs) != 1 || result.LadderPOUs[0].Mode != "plain" || !strings.Contains(result.LadderPOUs[0].Reason, "ladder differs") {
		t.Fatalf("ladder POUs = %+v", result.LadderPOUs)
	}
	warned := false
	for _, warning := range result.Warnings {
		warned = warned || strings.Contains(warning, "Build failed: fb_sched {Demo}")
	}
	if !warned {
		t.Fatalf("no simulator warning: %v", result.Warnings)
	}
	body, cxil, inline, inlineErr := ladderPayloads(t, root)
	if body != testLadderBody || cxil != testEmptyCxil {
		t.Fatalf("plain ladder not kept: body %q cxil %q", body, cxil)
	}
	if inlineErr != nil || inline != changed {
		t.Fatalf("inline ST of a plain ladder must be kept, got %q (%v)", inline, inlineErr)
	}
}
