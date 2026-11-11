package sysmac

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestRealCloudFBMigrationMatchesTechnician replays the Cloud_FB 2.0.13 →
// NX-IIOT_OptiCloud_V2 migration a technician did by hand in Sysmac Studio:
// library replace + program migration on the "pre" project, compared
// structurally with the "post" project.
//
// OMRON_MCP_CLOUDFB_FACIT = "<pre>;<new .slr>;<post>;<old library name>"
// where <pre>/<post> are .smc2 files or extracted <GUID> solution folders.
// OMRON_MCP_CLOUDFB_FACIT_OUT optionally receives the migrated solution.
// Real projects are never checked in; nothing of their content (SAS tokens)
// is logged beyond variable and pin names.
func TestRealCloudFBMigrationMatchesTechnician(t *testing.T) {
	parts := strings.Split(os.Getenv("OMRON_MCP_CLOUDFB_FACIT"), ";")
	if len(parts) != 4 {
		t.Skip("OMRON_MCP_CLOUDFB_FACIT not set")
	}
	pre, slr, post, oldName := solutionDir(t, parts[0]), parts[1], solutionDir(t, parts[2]), parts[3]
	work := filepath.Join(t.TempDir(), filepath.Base(pre))
	if err := CopySolutionDirectory(pre, work); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaceLibrary(LibraryReplaceOptions{ProjectRoot: work, LibraryPath: slr, OldLibrary: oldName, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	result, err := MigrateCloudFBProgram(CloudFBMigrationOptions{ProjectRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range result.Changes {
		t.Logf("change  %s: %s", change.Target, change.Change)
	}
	for _, item := range result.Unresolved {
		t.Logf("UNRESOLVED %s", item)
	}
	for _, item := range result.Warnings {
		t.Logf("warning %s", item)
	}
	if _, err := Inspect(work); err != nil {
		t.Fatalf("inspect after migration: %v", err)
	}
	compareMigrationWithFacit(t, work, post)
	if out := os.Getenv("OMRON_MCP_CLOUDFB_FACIT_OUT"); out != "" {
		if err := os.RemoveAll(out); err != nil {
			t.Fatal(err)
		}
		if err := CopySolutionDirectory(work, out); err != nil {
			t.Fatal(err)
		}
	}
}

// solutionDir returns an extracted solution folder; .smc2 archives are
// extracted into a temporary directory.
func solutionDir(t *testing.T, path string) string {
	t.Helper()
	if !strings.EqualFold(filepath.Ext(path), ".smc2") {
		return path
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	dir := t.TempDir()
	root := ""
	for _, file := range archive.File {
		target := filepath.Join(dir, filepath.FromSlash(file.Name))
		if !strings.HasPrefix(target, dir) {
			t.Fatalf("unsafe archive entry %s", file.Name)
		}
		if root == "" {
			root = strings.SplitN(file.Name, "/", 2)[0]
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, root)
}

type facitEntity struct {
	typ, subtype, name, parent string
}

func projectEntities(t *testing.T, dir string) (*oemTextDocument, map[string]facitEntity) {
	t.Helper()
	path, err := findOEM(dir)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := loadOEMText(path)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]facitEntity{}
	for _, entity := range doc.order {
		if inLibrary(doc, entity) {
			continue
		}
		result[entity.id] = facitEntity{doc.entityType(entity), doc.attr(entity, "subtype"), doc.attr(entity, "name"), entityParentID(entity)}
	}
	return doc, result
}

// rungSummary describes a rung without inline ST text: element types, FB
// names, instances, pins (Arg=Var@Ix) and Ix/LRI/RRI values.
func rungSummary(node *ladderJSON) string {
	var parts []string
	node.walk(func(object *ladderJSON) {
		switch kind := object.str("__type"); kind {
		case "IST":
			parts = append(parts, "IST(text not shown)")
		case "":
			for _, key := range []string{"LRI", "RRI"} {
				if value, ok := object.num(key); ok {
					parts = append(parts, fmt.Sprintf("%s=%d", key, value))
				}
			}
		case "PRM", "PF":
			parts = append(parts, fmt.Sprintf("%s %s=%s@%d type=%s", kind, object.str("Arg"), object.str("Var"), pinIx(object), object.str("Type")))
		default:
			parts = append(parts, fmt.Sprintf("%s %s %s@%d", kind, object.str("Name"), object.str("Var"), pinIx(object)))
		}
	})
	return strings.Join(parts, " | ")
}

func compareMigrationWithFacit(t *testing.T, got, want string) {
	gotDoc, gotEntities := projectEntities(t, got)
	_, wantEntities := projectEntities(t, want)
	gotDir, wantDir := filepath.Dir(mustOEM(t, got)), filepath.Dir(mustOEM(t, want))
	stats := map[string]int{}
	// Entity structure outside the library.
	var ids []string
	for id := range wantEntities {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		wantEntity := wantEntities[id]
		gotEntity, ok := gotEntities[id]
		switch {
		case !ok && wantEntity.typ == "GridWatch":
			t.Logf("expected difference: %s %q exists only in the technician's project (created by the GUI)", wantEntity.typ, wantEntity.name)
		case !ok:
			t.Errorf("entity %s %q (%s) missing", wantEntity.typ, wantEntity.name, id)
		case gotEntity.typ != wantEntity.typ || gotEntity.name != wantEntity.name || gotEntity.subtype != wantEntity.subtype:
			t.Errorf("entity %s differs: got %+v want %+v", id, gotEntity, wantEntity)
		}
	}
	for id, entity := range gotEntities {
		if _, ok := wantEntities[id]; !ok && !inLibraryID(gotDoc, id) {
			t.Errorf("entity %s %q (%s) should have been deleted", entity.typ, entity.name, id)
		}
	}
	for _, id := range ids {
		entity := wantEntities[id]
		if _, ok := gotEntities[id]; !ok {
			continue
		}
		gotData, gotErr := os.ReadFile(filepath.Join(gotDir, id+".xml"))
		wantData, wantErr := os.ReadFile(filepath.Join(wantDir, id+".xml"))
		if (gotErr == nil) != (wantErr == nil) {
			t.Errorf("%s %q: payload presence differs", entity.typ, entity.name)
			continue
		}
		if gotErr != nil {
			continue
		}
		label := entity.typ + " " + entity.name
		if parent, ok := wantEntities[entity.parent]; ok {
			label = parent.name + "/" + label
		}
		switch {
		case bytes.Equal(gotData, wantData):
			stats["identical payload"]++
		case entity.typ == "Variables":
			compareVariables(t, label, gotData, wantData)
			stats["variables compared"]++
		case entity.typ == "PouBody" && entity.subtype == "Ladder":
			compareLadder(t, label, gotData, wantData)
			stats["ladder compared"]++
		case entity.typ == "Solution":
			t.Logf("expected difference: %s payload (project properties, DateModified)", label)
		default:
			t.Errorf("%s: payload differs", label)
		}
	}
	t.Logf("comparison: %v", stats)
	t.Log("not compared (regenerated by Sysmac Studio): build artefacts (.NexBuildResults, .NexTransferInformation, controller .zip, .ValidationResults), dateLastModified and DN attributes, StorageData")
}

func mustOEM(t *testing.T, dir string) string {
	path, err := findOEM(dir)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func inLibraryID(doc *oemTextDocument, id string) bool {
	entity := doc.entities[id]
	return entity != nil && inLibrary(doc, entity)
}

func compareVariables(t *testing.T, label string, got, want []byte) {
	gotDoc, err := ParseSLWDDocument(got)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	wantDoc, err := ParseSLWDDocument(want)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	render := func(doc SLWDDocument) []string {
		var lines []string
		for _, variable := range doc.Variables() {
			var attributes []string
			for key, value := range variable.Attributes {
				attributes = append(attributes, key+"="+value)
			}
			sort.Strings(attributes)
			lines = append(lines, fmt.Sprintf("%s %s : %s IV=%q %v", variable.Group, variable.Name, variable.Type, variable.InitialValue, attributes))
		}
		return lines
	}
	gotLines, wantLines := render(gotDoc), render(wantDoc)
	if strings.Join(gotLines, "\n") == strings.Join(wantLines, "\n") {
		if !bytes.Equal(got, want) {
			t.Errorf("%s: variables equal but bytes differ", label)
		}
		return
	}
	for index := 0; index < len(gotLines) || index < len(wantLines); index++ {
		var a, b string
		if index < len(gotLines) {
			a = gotLines[index]
		}
		if index < len(wantLines) {
			b = wantLines[index]
		}
		if a != b && !strings.Contains(a+b, " SAS ") {
			t.Errorf("%s variable %d:\n got %s\nwant %s", label, index, a, b)
		} else if a != b {
			t.Errorf("%s variable %d differs (SAS variable, not shown)", label, index)
		}
	}
}

func compareLadder(t *testing.T, label string, got, want []byte) {
	gotBody, err := parseLadderBody(got)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	wantBody, err := parseLadderBody(want)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	gotRungs, wantRungs := gotBody.rungs(), wantBody.rungs()
	if len(gotRungs) != len(wantRungs) {
		t.Errorf("%s: %d rungs, want %d", label, len(gotRungs), len(wantRungs))
		return
	}
	for index := range gotRungs {
		if gotRungs[index].node.render() == wantRungs[index].node.render() {
			continue
		}
		t.Errorf("%s rung %d differs\n got %s\nwant %s", label, index, rungSummary(gotRungs[index].node), rungSummary(wantRungs[index].node))
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s: rungs equal but bytes differ (BOM/line endings)", label)
	}
}
