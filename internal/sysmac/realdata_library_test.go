package sysmac

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealLibraryReplaceMatchesSysmacStudio replays a library replacement
// that was done manually in Sysmac Studio and compares the result with it.
// OMRON_MCP_LIBRARY_FACIT = "<pre solution dir>;<new .slr>;<post solution dir>;<old library name>"
// (the solution dirs are the extracted <GUID> folders). Real projects are
// never checked in.
func TestRealLibraryReplaceMatchesSysmacStudio(t *testing.T) {
	parts := strings.Split(os.Getenv("OMRON_MCP_LIBRARY_FACIT"), ";")
	if len(parts) != 4 {
		t.Skip("OMRON_MCP_LIBRARY_FACIT not set")
	}
	pre, slr, post, oldName := parts[0], parts[1], parts[2], parts[3]
	work := filepath.Join(t.TempDir(), "sol")
	if err := CopySolutionDirectory(pre, work); err != nil {
		t.Fatal(err)
	}
	result, err := ReplaceLibrary(LibraryReplaceOptions{ProjectRoot: work, LibraryPath: slr, OldLibrary: oldName, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("old=%+v new=%+v removed=%d written=%d skipped=%d", result.OldLibrary, result.NewLibrary, len(result.RemovedFiles), len(result.WrittenFiles), len(result.SkippedFiles))
	if _, err := Inspect(work); err != nil {
		t.Fatalf("inspect after replace: %v", err)
	}
	gotOEM, _ := findOEM(work)
	wantOEM, _ := findOEM(post)
	got, err := loadOEMText(gotOEM)
	if err != nil {
		t.Fatal(err)
	}
	want, err := loadOEMText(wantOEM)
	if err != nil {
		t.Fatal(err)
	}
	find := func(doc *oemTextDocument) *oemTextEntity {
		for _, entity := range doc.topLevelLibraries() {
			if doc.attr(entity, "name") == result.NewLibrary.Name {
				return entity
			}
		}
		t.Fatalf("library %q not found", result.NewLibrary.Name)
		return nil
	}
	gotLib, wantLib := find(got), find(want)
	ignored := map[string]bool{"id": true, "dateLastModified": true, "DN": true}
	attrs := func(doc *oemTextDocument, entity *oemTextEntity) string {
		var parts []string
		for _, attribute := range entity.attrs {
			if !ignored[attribute.Name.Local] {
				parts = append(parts, attribute.Name.Local+"="+attribute.Value)
			}
		}
		return strings.Join(parts, " ")
	}
	// Inline payloads are kept only below ladder bodies that stay plain.
	plainLadder := map[string]bool{}
	for _, pou := range got.ladderPOUs(gotLib) {
		if data, err := os.ReadFile(filepath.Join(filepath.Dir(gotOEM), pou.body.id+".xml")); err == nil && !isEncryptedPayload(data) {
			for _, child := range pou.inline {
				plainLadder[child.id] = true
			}
		}
	}
	t.Logf("ladder POUs: %+v", result.LadderPOUs)
	expectedDiff := map[string]bool{"PouBody": true, "PouBodySourceHolder": true}
	stats := map[string]int{}
	var walk func(a, b *oemTextEntity, path string)
	walk = func(a, b *oemTextEntity, path string) {
		if attrs(got, a) != attrs(want, b) {
			t.Errorf("%s: attributes differ\n got %s\nwant %s", path, attrs(got, a), attrs(want, b))
		}
		stats["entities"]++
		gotFile, gotErr := os.ReadFile(filepath.Join(filepath.Dir(gotOEM), a.id+".xml"))
		wantFile, wantErr := os.ReadFile(filepath.Join(filepath.Dir(wantOEM), b.id+".xml"))
		switch {
		case (gotErr == nil) != (wantErr == nil) && got.entityType(a) == "Inline" && gotErr == nil && !plainLadder[a.id]:
			t.Errorf("%s (%s): inline payload stored although its body is compiled", path, got.entityType(a))
		case (gotErr == nil) != (wantErr == nil) && got.entityType(a) == "Inline" && gotErr == nil:
			stats["inline kept with plain ladder"]++
		case (gotErr == nil) != (wantErr == nil):
			t.Errorf("%s (%s): payload presence differs (got %v, want %v)", path, got.entityType(a), gotErr == nil, wantErr == nil)
		case gotErr != nil:
			stats["no payload"]++
		case bytes.Equal(gotFile, wantFile):
			stats["identical payload"]++
		case expectedDiff[got.entityType(a)]:
			stats["differs (re-encrypted / link info): "+got.entityType(a)]++
		default:
			t.Errorf("%s (%s): payload differs", path, got.entityType(a))
		}
		if len(a.children) != len(b.children) {
			t.Fatalf("%s: %d children, want %d", path, len(a.children), len(b.children))
		}
		for index := range a.children {
			walk(a.children[index], b.children[index], path+"/"+got.attr(a.children[index], "name"))
		}
	}
	walk(gotLib, wantLib, result.NewLibrary.Name)
	t.Logf("comparison: %v", stats)
	gotReferred, _ := os.ReadFile(filepath.Join(filepath.Dir(gotOEM), gotLib.parent.id+".xml"))
	wantReferred, _ := os.ReadFile(filepath.Join(filepath.Dir(wantOEM), wantLib.parent.id+".xml"))
	if !bytes.Equal(gotReferred, wantReferred) {
		t.Errorf("ReferredLibraries payload differs from Sysmac Studio")
	}
	for _, name := range result.RemovedFiles {
		if _, err := os.Stat(filepath.Join(work, name)); err == nil {
			t.Errorf("old file %s still present", name)
		}
	}
	if out := os.Getenv("OMRON_MCP_LIBRARY_FACIT_OUT"); out != "" {
		if err := os.RemoveAll(out); err != nil {
			t.Fatal(err)
		}
		if err := CopySolutionDirectory(work, out); err != nil {
			t.Fatal(err)
		}
	}
}
