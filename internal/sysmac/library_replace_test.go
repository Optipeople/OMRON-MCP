package sysmac

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func crlf(text string) string { return strings.ReplaceAll(text, "\n", "\r\n") }

const testXMLHeader = `<?xml version="1.0" encoding="utf-8"?>` + "\n"

func libraryModel(indent, name, version, id, path string) string {
	lines := []string{
		"<LibraryModel>",
		"  <Name>" + name + "</Name>",
		"  <Version>" + version + "</Version>",
		"  <LibraryId>" + id + "</LibraryId>",
		"  <Comment>line one",
		"",
		"line two</Comment>",
		"  <PublicPartsNames>",
		"    <PublicName>Demo\\" + name + "_FB</PublicName>",
		"  </PublicPartsNames>",
		"  <LibraryFilePath>" + path + "</LibraryFilePath>",
		"  <LibraryFileRelativePath></LibraryFileRelativePath>",
		"</LibraryModel>",
	}
	for index, line := range lines {
		if index == 0 || (line != "" && !strings.HasPrefix(line, "line two")) {
			lines[index] = indent + line
		}
	}
	return strings.Join(lines, "\n")
}

const testProjectOEM = `<sysmac schemaVersion="1">
  <Entity type="Solution" subtype="" id="sol" name="Demo" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-sol">
    <AccessInfos />
    <ChildEntities>
      <Entity type="ReferredLibraries" subtype="Nex" id="refs" name="Library" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-refs">
        <AccessInfos />
        <ChildEntities>
          <Entity type="Library" subtype="Nex" id="oldlib" name="OldLib_1_0" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-oldlib">
            <AccessInfos />
            <ChildEntities>
              <Entity type="FunctionBlock" subtype="StructuredText" id="oldfb" name="OldFB" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-oldfb">
                <AccessInfos />
                <ChildEntities>
                  <Entity type="Variables" subtype="StructuredTextFunctionBlock" id="oldvars" name="Variables" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-oldvars">
                    <AccessInfos />
                    <ChildEntities />
                  </Entity>
                </ChildEntities>
              </Entity>
            </ChildEntities>
          </Entity>
        </ChildEntities>
      </Entity>
      <Entity type="Program" subtype="StructuredText" id="prog" name="Main" version="0" dateCreated="01/01/2026 10:00:00" dateLastModified="01/01/2026 10:00:00" trackingId="t-prog">
        <AccessInfos />
        <ChildEntities />
      </Entity>
    </ChildEntities>
  </Entity>
</sysmac>`

func referredPayload(model string) string {
	return crlf(utf8BOM + testXMLHeader + "<data>\n  <data>\n    <ReferredLibrariesModel>\n      <ReferredLibraries>\n        <ReferredLibrary>\n          <data>\n" +
		model + "\n          </data>\n        </ReferredLibrary>\n      </ReferredLibraries>\n    </ReferredLibrariesModel>\n  </data>\n</data>")
}

// writeTestProject creates a project referencing library OldLib_1_0.
func writeTestProject(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "sol")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"sol.oem":             crlf(utf8BOM + testXMLHeader + testProjectOEM),
		"oldlib.xml":          crlf(utf8BOM + testXMLHeader + "<data>\n  <data>\n" + libraryModel("    ", "OldLib", "1.0.0", "old-library-id", `C:\old\OldLib_1_0.slr`) + "\n  </data>\n</data>"),
		"oldvars.xml":         crlf("[SLWD version=1.0]\n_EN=Variables\n+GN=VAR\tGVT=DefaultGroup\n++D=BOOL\tN=x\tG=VAR\n"),
		"oldfb.BookmarkGroup": "bookmarks",
		"refs.xml":            referredPayload(libraryModel("            ", "OldLib", "1.0.0", "old-library-id", `C:\old\OldLib_1_0.slr`)),
		"prog.xml":            "program",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const testLibraryOEM = `<sysmac schemaVersion="3">
  <Entity type="Library" subtype="Nex" id="lib-root" name="NewLib[ACME]" version="0" dateCreated="02/02/2025 09:00:00" dateLastModified="02/02/2025 09:00:00" entityVersion="0" entityRevision="0" trackingId="t-lib">
    <AccessInfos />
    <ChildEntities>
      <Entity type="TransferInformation" subtype="" id="ti-1" name="" version="0" dateLastModified="02/02/2025 09:00:00" entityVersion="1" entityRevision="0" trackingId="t-ti1">
        <AccessInfos />
        <ChildEntities />
      </Entity>
      <Entity type="FunctionBlock" subtype="StructuredText" id="lib-fb" name="NewFB" version="0" dateCreated="02/02/2025 09:00:00" dateLastModified="02/02/2025 09:00:00" namespace="Demo" usingNamespaces="Demo&#xD;&#xA;" entityVersion="0" entityRevision="0" trackingId="t-fb">
        <AccessInfos />
        <ChildEntities>
          <Entity type="TransferInformation" subtype="" id="ti-2" name="" version="0" dateLastModified="02/02/2025 09:00:00" entityVersion="1" entityRevision="0" trackingId="t-ti2">
            <AccessInfos />
            <ChildEntities />
          </Entity>
          <Entity type="Variables" subtype="StructuredTextFunctionBlock" id="lib-vars" name="Variables" version="0" dateCreated="02/02/2025 09:00:00" dateLastModified="02/02/2025 09:00:00" entityVersion="0" entityRevision="0" trackingId="t-vars">
            <AccessInfos />
            <ChildEntities>
              <Entity type="TransferInformation" subtype="" id="ti-3" name="" version="0" dateLastModified="02/02/2025 09:00:00" entityVersion="1" entityRevision="0" trackingId="t-ti3">
                <AccessInfos />
                <ChildEntities />
              </Entity>
            </ChildEntities>
          </Entity>
          <Entity type="PouBody" subtype="StructuredText" id="lib-body" name="Program Body" version="0.2" dateCreated="02/02/2025 09:00:00" dateLastModified="02/02/2025 09:00:00" entityVersion="0" entityRevision="0" trackingId="t-body">
            <AccessInfos />
            <ChildEntities />
          </Entity>
          <Entity type="Inline" subtype="StructuredText" id="lib-inline" name="x" version="0" dateCreated="02/02/2025 09:00:00" dateLastModified="02/02/2025 09:00:00" entityVersion="0" entityRevision="0" trackingId="t-inline">
            <AccessInfos />
            <ChildEntities />
          </Entity>
        </ChildEntities>
      </Entity>
      <Entity type="DataType" subtype="" id="lib-dt" name="Data Types" version="0" dateCreated="02/02/2025 09:00:00" dateLastModified="02/02/2025 09:00:00" entityVersion="0" entityRevision="0" trackingId="t-dt">
        <AccessInfos />
        <ChildEntities />
      </Entity>
      <Entity type="ReferredLibraries" subtype="Nex" id="lib-refs" name="Library" version="0" dateCreated="02/02/2025 09:00:00" dateLastModified="02/02/2025 09:00:00" entityVersion="0" entityRevision="0" trackingId="t-lrefs">
        <AccessInfos />
        <ChildEntities>
          <Entity type="Library" subtype="Nex" id="nested" name="NestedLib_V1_0" version="0" dateCreated="02/02/2025 09:00:00" dateLastModified="02/02/2025 09:00:00" entityVersion="0" entityRevision="0" trackingId="t-nested">
            <AccessInfos />
            <ChildEntities />
          </Entity>
        </ChildEntities>
      </Entity>
    </ChildEntities>
  </Entity>
</sysmac>`

const testLibraryVariables = `<data>
  <VariableGroup Name="VAR" Attribute="" ViewType="DefaultGroup">
    <Variable DataTypeName="ARRAY[0..7] OF BYTE" Name="buf" Address="" Length="" Retained="false" InitialValue="" Comment="a &lt;buffer&gt;" NetworkPublication="NoPublication" IsReadOnlyNetwork="false" Edge="NoEdge" Id="v1" Order="" IsControllerVariable="false" IsReadOnly="false" IsConstant="true" Group="VAR" IsAddedInOnlineEdit="false" InitialValueArrayLength="" />
  </VariableGroup>
  <VariableGroup Name="VAR_INPUT" Attribute="InputOutput" ViewType="EdgeInputOutputGroup">
    <Variable DataTypeName="BOOL" Name="Execute" Address="" Length="" Retained="false" InitialValue="False" Comment="" NetworkPublication="NoPublication" IsReadOnlyNetwork="false" Edge="UpDirection" Id="v2" Order="0" IsControllerVariable="false" IsReadOnly="false" IsConstant="false" Group="VAR_INPUT" IsAddedInOnlineEdit="false" InitialValueArrayLength="">
      <ElementComments>
        <ElementComment ElementKey="k" Comment="c &quot;q&quot;" />
      </ElementComments>
    </Variable>
  </VariableGroup>
</data>`

// writeTestLibrary creates an .slr archive laid out like Sysmac Studio's.
func writeTestLibrary(t *testing.T, path string) {
	t.Helper()
	null := `<NullObject xmlns="http://schemas.datacontract.org/2004/07/Omron.Cxap.DataLayer.Entities.Core" xmlns:i="http://www.w3.org/2001/XMLSchema-instance"/>`
	files := map[string]string{
		"lib-root/lib-root.oem":   crlf(testXMLHeader + testLibraryOEM),
		"lib-root/lib-root.xml":   crlf(testXMLHeader + "<data>\n  <data>\n" + libraryModel("    ", "NewLib", "2.0.0", "new-library-id", "") + "\n  </data>\n</data>"),
		"lib-root/lib-fb.xml":     null,
		"lib-root/lib-vars.xml":   crlf(testXMLHeader + testLibraryVariables),
		"lib-root/lib-body.xml":   `<StructuredTextModel>&lt;EncryptedFile&gt;secret&lt;/EncryptedFile&gt;</StructuredTextModel>`,
		"lib-root/lib-inline.xml": testXMLHeader + `<data><StructuredText><Text>plain</Text></StructuredText></data>`,
		"lib-root/lib-dt.xml":     crlf(testXMLHeader + "<data>\n  <DataType Name=\"\" BaseType=\"\" Order=\"0\" Id=\"d1\">\n    <DataType Name=\"sX\" BaseType=\"STRUCT\" Id=\"d2\" />\n  </DataType>\n</data>"),
		"lib-root/lib-refs.xml":   crlf(testXMLHeader + "<data />"),
		"lib-root/nested.xml":     crlf(testXMLHeader + "<data />"),
		"lib-root/ti-1.xml":       null,
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(out)
	for name, content := range files {
		writer, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceLibraryGraftsSubtreeLikeSysmacStudio(t *testing.T) {
	root := writeTestProject(t)
	slr := filepath.Join(t.TempDir(), "NewLib_2_0.slr")
	writeTestLibrary(t, slr)
	now := time.Date(2026, 11, 6, 14, 5, 9, 0, time.UTC)
	result, err := ReplaceLibrary(LibraryReplaceOptions{ProjectRoot: root, LibraryPath: slr, LibraryFilePath: `C:\libs\NewLib_2_0.slr`, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if result.OldLibrary.LibraryID != "old-library-id" || result.NewLibrary.Name != "NewLib_2_0" || result.NewLibrary.Version != "2.0.0" || result.NewLibrary.Entities != 8 {
		t.Fatalf("result = %+v", result)
	}
	inspection, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]EntitySummary{}
	for _, entity := range inspection.Entities {
		byName[entity.Name] = entity
		if entity.Type == "TransferInformation" {
			t.Fatal("TransferInformation entity was grafted")
		}
		switch entity.ID {
		case "lib-root", "lib-fb", "oldlib", "oldfb":
			t.Fatalf("entity id %q not regenerated/removed", entity.ID)
		}
	}
	for _, name := range []string{"NewLib_2_0", "NewFB", "Program Body", "Data Types", "NestedLib_V1_0", "Main"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("entity %q missing: %#v", name, inspection.Entities)
		}
	}
	if byName["NewFB"].TrackingID != "t-fb" {
		t.Fatalf("trackingId not preserved: %#v", byName["NewFB"])
	}
	oemData, _ := os.ReadFile(filepath.Join(root, "sol.oem"))
	oem := string(oemData)
	if strings.Contains(oem, "entityVersion") || strings.Contains(oem, "OldLib") {
		t.Fatal("old library or slr-only attributes left in index")
	}
	if !strings.Contains(oem, `dateCreated="02/02/2025 09:00:00" dateLastModified="11/06/2026 14:05:09" namespace="Demo" usingNamespaces="Demo&#xD;&#xA;" trackingId="t-fb"`) {
		t.Fatalf("attributes not preserved in order:\n%s", oem)
	}
	if !strings.Contains(oem, "\r\n          <Entity type=\"Library\" subtype=\"Nex\" id=\""+result.NewLibrary.EntityID+"\" name=\"NewLib_2_0\"") ||
		!strings.Contains(oem, "\r\n                  <Entity type=\"Variables\"") ||
		!strings.Contains(oem, "trackingId=\"t-vars\">\r\n                    <AccessInfos />\r\n                    <ChildEntities />\r\n") {
		t.Fatalf("subtree not re-indented / emptied ChildEntities not collapsed:\n%s", oem)
	}
	read := func(id string) string {
		data, err := os.ReadFile(filepath.Join(root, id+".xml"))
		if err != nil {
			return "<missing>"
		}
		return string(data)
	}
	wantVars := "[SLWD version=1.0]\r\n_EN=Variables\r\n+GN=VAR\tGVT=DefaultGroup\r\n++D=ARRAY[0..7] OF BYTE\tN=buf\tConst=1\tG=VAR\tCom=a <buffer>\r\n" +
		"+GN=VAR_INPUT\tGA=InputOutput\tGVT=EdgeInputOutputGroup\r\n++D=BOOL\tN=Execute\tIV=False\tE=UpDirection\tOrd=0\tG=VAR_INPUT\tEC=<ECs><EC EK=\"k\" C=\"c &quot;q&quot;\" /></ECs>\r\n"
	if got := read(byName["Variables"].ID); got != wantVars {
		t.Fatalf("variables = %q", got)
	}
	if got := read(byName["Data Types"].ID); !strings.HasPrefix(got, utf8BOM) || strings.Contains(got, " Id=") || !strings.Contains(got, `<DataType Name="Data Types" BaseType="" Order="0">`) {
		t.Fatalf("data type = %q", got)
	}
	if got := read(byName["Program Body"].ID); !strings.Contains(got, "EncryptedFile") || strings.HasPrefix(got, utf8BOM) {
		t.Fatalf("body = %q", got)
	}
	if read(byName["NewFB"].ID) != "<missing>" || read(byName["x"].ID) != "<missing>" {
		t.Fatal("NullObject or plain inline payload was copied")
	}
	rootPayload := read(result.NewLibrary.EntityID)
	if !strings.HasPrefix(rootPayload, utf8BOM) || !strings.Contains(rootPayload, `<LibraryFilePath>C:\libs\NewLib_2_0.slr</LibraryFilePath>`) {
		t.Fatalf("root payload = %q", rootPayload)
	}
	for _, name := range []string{"oldlib.xml", "oldvars.xml", "oldfb.BookmarkGroup"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("old file %s not removed", name)
		}
	}
	if read("prog") != "program" {
		t.Fatal("unrelated payload touched")
	}
	want := referredPayload(libraryModel("            ", "NewLib", "2.0.0", "new-library-id", `C:\libs\NewLib_2_0.slr`))
	if got := read("refs"); got != want {
		t.Fatalf("ReferredLibraries payload:\n%q\nwant\n%q", got, want)
	}
}

func TestReplaceLibraryRejectsUnknownLibrary(t *testing.T) {
	root := writeTestProject(t)
	slr := filepath.Join(t.TempDir(), "NewLib_2_0.slr")
	writeTestLibrary(t, slr)
	before, _ := os.ReadFile(filepath.Join(root, "sol.oem"))
	if _, err := ReplaceLibrary(LibraryReplaceOptions{ProjectRoot: root, LibraryPath: slr, OldLibrary: "Nope"}); err == nil || !strings.Contains(err.Error(), "OldLib_1_0") {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "sol.oem"))
	if string(before) != string(after) {
		t.Fatal("failed replace modified project")
	}
}
