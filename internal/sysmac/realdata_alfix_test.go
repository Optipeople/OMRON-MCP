package sysmac

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealCloudFBMigrationWithoutFacit replays the Cloud_FB → V2 migration
// on a customer project without a technician's facit (Alfix OM01186:
// telemetry, batch and automatic stop causes) and checks that nothing the
// code uses is lost.
//
// OMRON_MCP_CLOUDFB_REAL = "<pre .smc2 or folder>;<new .slr>;<old library name>"
// OMRON_MCP_CLOUDFB_REAL_OUT optionally receives the migrated solution.
// Only names (variables, pins, sections) are logged, never inline ST text.
func TestRealCloudFBMigrationWithoutFacit(t *testing.T) {
	parts := strings.Split(os.Getenv("OMRON_MCP_CLOUDFB_REAL"), ";")
	if len(parts) != 3 {
		t.Skip("OMRON_MCP_CLOUDFB_REAL not set")
	}
	pre, slr, oldName := solutionDir(t, parts[0]), parts[1], parts[2]
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
	// Every Cloud_FB service is migrated and no used event tag is lost.
	for _, item := range result.Unresolved {
		for _, fragment := range []string{"fbCloud_", "EventCode", "no event in the new layout", "is not a member of"} {
			if strings.Contains(item, fragment) {
				t.Errorf("unresolved: %s", item)
			}
		}
	}
	report, err := CompareProjects(pre, work)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("compare summary: %+v", report.Summary)
	for _, finding := range report.Findings {
		if finding.Class == CompareMissing {
			t.Errorf("missing function: %s: %s", finding.Area, finding.Detail)
		} else if finding.Class == CompareUnexplained {
			t.Logf("unexplained: %s %s: %s", finding.Area, finding.Item, finding.Detail)
		}
	}
	if out := os.Getenv("OMRON_MCP_CLOUDFB_REAL_OUT"); out != "" {
		if err := os.RemoveAll(out); err != nil {
			t.Fatal(err)
		}
		if err := CopySolutionDirectory(work, out); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRealCompareProjects writes a compare report for two real projects.
// OMRON_MCP_COMPARE = "<old>;<new>;<report .md>" (.smc2 or folders).
func TestRealCompareProjects(t *testing.T) {
	parts := strings.Split(os.Getenv("OMRON_MCP_COMPARE"), ";")
	if len(parts) != 3 {
		t.Skip("OMRON_MCP_COMPARE not set")
	}
	report, err := CompareProjects(parts[0], parts[1])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("summary: %+v", report.Summary)
	for _, finding := range report.Findings {
		if finding.Class == CompareMissing || finding.Class == CompareUnexplained {
			t.Logf("%s %s %s: %s", finding.Class, finding.Area, finding.Item, finding.Detail)
		}
	}
	if err := os.WriteFile(parts[2], []byte(RenderCompareMarkdown(report, os.Getenv("OMRON_MCP_COMPARE_TITLE"))), 0600); err != nil {
		t.Fatal(err)
	}
}
