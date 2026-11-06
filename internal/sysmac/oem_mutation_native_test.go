package sysmac

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nativeOEM mimics Sysmac Studio's layout: BOM, CRLF, 2-space indentation,
// encoded newlines inside attributes and no trailing newline.
var nativeOEM = strings.ReplaceAll(utf8BOM+`<?xml version="1.0" encoding="utf-8"?>
<sysmac schemaVersion="1">
  <Entity type="Solution" subtype="" id="sol" name="Demo" version="0" dateCreated="06/11/2026 12:50:53" dateLastModified="06/11/2026 12:52:55" trackingId="t-sol">
    <AccessInfos />
    <ChildEntities>
      <Entity type="Group" subtype="IecPous" id="pous" name="POUs" version="0" dateCreated="06/11/2026 12:50:53" dateLastModified="06/11/2026 12:50:53" trackingId="t-pous">
        <AccessInfos />
        <ChildEntities>
          <Entity type="Program" subtype="StructuredText" id="prog" name="Main" version="0" dateCreated="06/11/2026 12:50:53" dateLastModified="06/11/2026 12:50:53" usingNamespaces="A\B&#xD;&#xA;" trackingId="t-prog">
            <AccessInfos />
            <ChildEntities>
              <Entity type="PouBody" subtype="StructuredText" id="body" name="Program Body" version="0" dateCreated="06/11/2026 12:50:53" dateLastModified="06/11/2026 12:50:53" trackingId="t-body">
                <AccessInfos />
                <ChildEntities />
              </Entity>
            </ChildEntities>
          </Entity>
          <Entity type="Program" subtype="StructuredText" id="aux" name="Aux" version="0" dateCreated="06/11/2026 12:50:53" dateLastModified="06/11/2026 12:50:53" trackingId="t-aux">
            <AccessInfos />
            <ChildEntities />
          </Entity>
        </ChildEntities>
      </Entity>
    </ChildEntities>
  </Entity>
</sysmac>`, "\n", "\r\n")

func writeNativeOEM(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "sol.oem")
	if err := os.WriteFile(path, []byte(nativeOEM), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestOEMRenameKeepsNativeFormat(t *testing.T) {
	path := writeNativeOEM(t)
	if _, err := ApplyOEMMutation(path, OEMMutation{Operation: "rename", EntityID: "aux", Name: "Aux & <Co>"}, ""); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(nativeOEM, `id="aux" name="Aux"`, `id="aux" name="Aux &amp; &lt;Co&gt;"`, 1)
	if got := readString(t, path); got != want {
		t.Fatalf("rename changed more than the name:\n%s", got)
	}
	project, err := ParseOEM(path)
	if err != nil {
		t.Fatal(err)
	}
	if project.EntitiesByID["aux"].Name != "Aux & <Co>" {
		t.Fatalf("parsed name = %q", project.EntitiesByID["aux"].Name)
	}
	if _, err := Inspect(filepath.Dir(path)); err != nil {
		t.Fatalf("inspect after rename: %v", err)
	}
}

func TestOEMDeleteRemovesOnlyEntityLines(t *testing.T) {
	path := writeNativeOEM(t)
	if _, err := ApplyOEMMutation(path, OEMMutation{Operation: "delete", EntityID: "aux"}, ""); err != nil {
		t.Fatal(err)
	}
	start := strings.Index(nativeOEM, "          <Entity type=\"Program\" subtype=\"StructuredText\" id=\"aux\"")
	end := strings.Index(nativeOEM[start:], "</Entity>\r\n") + start + len("</Entity>\r\n")
	want := nativeOEM[:start] + nativeOEM[end:]
	if got := readString(t, path); got != want {
		t.Fatalf("delete result differs:\n%s", got)
	}
	// A second mutation on the result (previously failed) must work.
	if _, err := ApplyOEMMutation(path, OEMMutation{Operation: "delete", EntityID: "body"}, ""); err != nil {
		t.Fatal(err)
	}
	got := readString(t, path)
	if strings.Contains(got, `id="body"`) || !strings.Contains(got, "            <AccessInfos />\r\n            <ChildEntities />\r\n          </Entity>") {
		t.Fatalf("emptied ChildEntities not collapsed:\n%s", got)
	}
	if _, err := Inspect(filepath.Dir(path)); err != nil {
		t.Fatalf("inspect after delete: %v", err)
	}
}

func TestOEMCreateMoveCloneKeepNativeLayout(t *testing.T) {
	path := writeNativeOEM(t)
	created, err := ApplyOEMMutation(path, OEMMutation{Operation: "create", EntityID: "new", ParentID: "aux", Name: "Body", Type: "PouBody", Subtype: "StructuredText"}, "")
	if err != nil || created != "new" {
		t.Fatalf("create = %q, %v", created, err)
	}
	got := readString(t, path)
	if !strings.Contains(got, "\r\n            <ChildEntities>\r\n              <Entity type=\"PouBody\" subtype=\"StructuredText\" id=\"new\" name=\"Body\"") ||
		!strings.Contains(got, "\r\n                <AccessInfos />\r\n                <ChildEntities />\r\n              </Entity>\r\n            </ChildEntities>\r\n") {
		t.Fatalf("create layout:\n%s", got)
	}
	if _, err := ApplyOEMMutation(path, OEMMutation{Operation: "move", EntityID: "body", ParentID: "pous"}, ""); err != nil {
		t.Fatal(err)
	}
	cloneID, err := ApplyOEMMutation(path, OEMMutation{Operation: "clone", EntityID: "prog", ParentID: "pous", Name: "MainCopy"}, "")
	if err != nil {
		t.Fatal(err)
	}
	project, err := ParseOEM(path)
	if err != nil {
		t.Fatal(err)
	}
	if project.EntitiesByID["body"].ParentID != "pous" {
		t.Fatalf("move parent = %q", project.EntitiesByID["body"].ParentID)
	}
	clone := project.EntitiesByID[cloneID]
	if clone == nil || clone.Name != "MainCopy" || clone.TrackingID == "t-prog" {
		t.Fatalf("clone = %#v", clone)
	}
	final := readString(t, path)
	if !strings.HasPrefix(final, utf8BOM+"<?xml") || strings.Contains(strings.ReplaceAll(final, "\r\n", ""), "\n") || strings.Contains(final, "oemNode") {
		t.Fatal("native encoding/line endings lost")
	}
	if !strings.Contains(final, `usingNamespaces="A\B&#xD;&#xA;"`) {
		t.Fatal("encoded attribute newline was rewritten")
	}
	for _, line := range strings.Split(final, "\r\n") {
		trimmed := strings.TrimLeft(line, " ")
		if (len(line)-len(trimmed))%2 != 0 {
			t.Fatalf("odd indentation: %q", line)
		}
	}
}

func TestOEMRejectsUnknownEntityAndRoot(t *testing.T) {
	path := writeNativeOEM(t)
	if _, err := ApplyOEMMutation(path, OEMMutation{Operation: "delete", EntityID: "missing"}, ""); err == nil {
		t.Fatal("expected missing entity error")
	}
	if _, err := ApplyOEMMutation(path, OEMMutation{Operation: "delete", EntityID: "sol"}, ""); err == nil {
		t.Fatal("expected root delete error")
	}
	if got := readString(t, path); got != nativeOEM {
		t.Fatal("failed mutation modified file")
	}
}
