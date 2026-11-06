package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rjboer/omron-mcp/internal/sysmac"
)

const libToolProject = "<sysmac schemaVersion=\"1\">\r\n" +
	"  <Entity type=\"Solution\" subtype=\"\" id=\"sol\" name=\"Demo\" trackingId=\"t\">\r\n" +
	"    <AccessInfos />\r\n" +
	"    <ChildEntities>\r\n" +
	"      <Entity type=\"ReferredLibraries\" subtype=\"Nex\" id=\"refs\" name=\"Library\" trackingId=\"t1\">\r\n" +
	"        <AccessInfos />\r\n" +
	"        <ChildEntities>\r\n" +
	"          <Entity type=\"Library\" subtype=\"Nex\" id=\"old\" name=\"Old_1_0\" trackingId=\"t2\">\r\n" +
	"            <AccessInfos />\r\n" +
	"            <ChildEntities />\r\n" +
	"          </Entity>\r\n" +
	"        </ChildEntities>\r\n" +
	"      </Entity>\r\n" +
	"    </ChildEntities>\r\n" +
	"  </Entity>\r\n" +
	"</sysmac>"

const libToolLibrary = "<sysmac schemaVersion=\"3\">\r\n" +
	"  <Entity type=\"Library\" subtype=\"Nex\" id=\"newroot\" name=\"New\" entityVersion=\"0\" entityRevision=\"0\" trackingId=\"t3\">\r\n" +
	"    <AccessInfos />\r\n" +
	"    <ChildEntities />\r\n" +
	"  </Entity>\r\n" +
	"</sysmac>"

func libToolModel(name, id string) string {
	return "<LibraryModel>\r\n      <Name>" + name + "</Name>\r\n      <LibraryId>" + id + "</LibraryId>\r\n      <LibraryFilePath></LibraryFilePath>\r\n    </LibraryModel>"
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

func TestLibraryReplaceToolStagesThenCommits(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "sol")
	writeFiles(t, project, map[string]string{
		"sol.oem":  libToolProject,
		"old.xml":  "<data>\r\n  <data>\r\n    " + libToolModel("Old", "old-id") + "\r\n  </data>\r\n</data>",
		"refs.xml": "<data>\r\n  <data>\r\n    " + libToolModel("Old", "old-id") + "\r\n  </data>\r\n</data>",
	})
	library := filepath.Join(base, "New_2_0")
	writeFiles(t, library, map[string]string{
		"newroot.oem": libToolLibrary,
		"newroot.xml": "<data>\r\n  <data>\r\n    " + libToolModel("New", "new-id") + "\r\n  </data>\r\n</data>",
	})
	staged, err := callArgs(t, "sysmac_library_replace", map[string]any{"project_path": project, "library_path": library, "old_library": "Old_1_0", "new_name": "New_2_0"})
	if err != nil {
		t.Fatal(err)
	}
	content := staged["structuredContent"].(map[string]any)
	if content["status"] != "staged" {
		t.Fatalf("result = %#v", content)
	}
	if data, _ := os.ReadFile(filepath.Join(project, "sol.oem")); !strings.Contains(string(data), "Old_1_0") {
		t.Fatal("staged replace touched the project")
	}
	if _, err := callArgs(t, "sysmac_transaction_commit", map[string]any{"transaction_id": content["transaction_id"], "confirm_commit": true}); err != nil {
		t.Fatal(err)
	}
	inspection, err := sysmac.Inspect(project)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entity := range inspection.Entities {
		if entity.Name == "Old_1_0" {
			t.Fatal("old library still present")
		}
		found = found || entity.Name == "New_2_0"
	}
	if !found {
		t.Fatalf("new library missing: %#v", inspection.Entities)
	}
	refs, _ := os.ReadFile(filepath.Join(project, "refs.xml"))
	if !strings.Contains(string(refs), "<LibraryId>new-id</LibraryId>") {
		t.Fatalf("refs = %s", refs)
	}
	if _, err := os.Stat(filepath.Join(project, "old.xml")); !os.IsNotExist(err) {
		t.Fatal("old library payload not removed")
	}
}
