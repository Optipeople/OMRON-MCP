package sysmac

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRealLibraryReplaceUpdatesChangedSignatures replays a library update
// on a project that already uses NX-IIOT_OptiCloud_V2 (OptiPeople OM01347:
// V2 2.5.0 → V2_7 2.7.0, where fbOpti_ProductionData_service got new
// inputs). Sysmac Studio rejected the result before the fix with "The
// argument of the function or function block caller is different from the
// argument of the definition" on Cloud/Tags row 0. The ladder call must get
// the new pins and compare must explain the change with LIB-SIGNATURE.
//
// OMRON_MCP_LIBRARY_SIGNATURE = "<project .smc2 or folder>;<new .slr>;<old library name>;<section>;<rung>"
// e.g. "...\OM01347_Online_2026-11-17.smc2;...\NX-IIOT_OptiCloud_V2_7.slr;NX-IIOT_OptiCloud_V2_5;Cloud/Tags;0".
// OMRON_MCP_LIBRARY_SIGNATURE_OUT optionally receives the updated solution.
// Only pin names and bindings of FB boxes are logged, never inline ST.
func TestRealLibraryReplaceUpdatesChangedSignatures(t *testing.T) {
	parts := strings.Split(os.Getenv("OMRON_MCP_LIBRARY_SIGNATURE"), ";")
	if len(parts) != 5 {
		t.Skip("OMRON_MCP_LIBRARY_SIGNATURE not set")
	}
	source, slr, oldName, section, rung := parts[0], parts[1], parts[2], parts[3], parts[4]
	pre := solutionDir(t, source)
	work := filepath.Join(t.TempDir(), filepath.Base(pre))
	if err := CopySolutionDirectory(pre, work); err != nil {
		t.Fatal(err)
	}
	result, err := ReplaceLibrary(LibraryReplaceOptions{ProjectRoot: work, LibraryPath: slr, OldLibrary: oldName, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("library %s %s → %s %s", result.OldLibrary.Name, result.OldLibrary.Version, result.NewLibrary.Name, result.NewLibrary.Version)
	for _, change := range result.SignatureChanges {
		t.Logf("signature %s: added %v removed %v changed %v", change.POU, change.Added, change.Removed, change.Changed)
	}
	var target *LibraryCallUpdate
	for index, update := range result.CallUpdates {
		t.Logf("call %s %s %s %s: %s added %v removed %v retyped %v %s", update.Section, update.Location, update.POU, update.Instance, update.Status, update.Added, update.Removed, update.Retyped, update.Reason)
		if update.Section == section && update.Location == "rung "+rung {
			target = &result.CallUpdates[index]
		}
	}
	for _, warning := range result.Warnings {
		t.Logf("warning %s", warning)
	}
	if target == nil || target.Status != "updated" || len(target.Added) == 0 {
		t.Fatalf("call in %s rung %s was not updated: %+v", section, rung, target)
	}
	if _, err := Inspect(work); err != nil {
		t.Fatalf("inspect after replace: %v", err)
	}

	// The call now has exactly the pins of the new definition.
	before, after := sectionRung(t, pre, section, rung), sectionRung(t, work, section, rung)
	t.Logf("before %s", rungSummary(before))
	t.Logf("after  %s", rungSummary(after))
	project, cleanup, err := loadCompareProject(work)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, call := range ladderCalls(after) {
		if sig := project.sigs.resolve(call.str("Name")); sig != nil {
			if inSync, ok := callInSync(call, sig); !ok || !inSync {
				t.Errorf("%s %s does not match its definition after the update", sig.Name, call.str("Var"))
			}
		}
	}
	pins := callPins(ladderCalls(after)[0])
	for _, name := range target.Added {
		if pin := pins[strings.ToLower(name)]; pin == nil || pin.str("Var") != "" {
			t.Errorf("new pin %s missing or connected", name)
		}
	}
	for name, pin := range callPins(ladderCalls(before)[0]) {
		if binding := pin.str("Var"); binding != "" && pins[name].str("Var") != binding {
			t.Errorf("pin %s lost its connection %s", name, binding)
		}
	}

	report, err := CompareProjects(source, work)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("compare summary: %+v", report.Summary)
	found := false
	for _, finding := range report.Findings {
		if finding.Class == CompareMissing || finding.Class == CompareUnexplained {
			t.Errorf("%s %s %s: %s", finding.Class, finding.Area, finding.Item, finding.Detail)
		}
		if finding.Item == section+" rung "+rung {
			t.Logf("finding %s %v: %s", finding.Class, finding.Rules, finding.Detail)
			for _, rule := range finding.Rules {
				found = found || rule == "LIB-SIGNATURE"
			}
		}
	}
	if !found {
		t.Errorf("%s rung %s is not classified with LIB-SIGNATURE", section, rung)
	}

	// Without the call update (the section as before the replacement)
	// compare must flag the rung instead of calling it identical.
	stale := filepath.Join(t.TempDir(), filepath.Base(pre))
	if err := CopySolutionDirectory(work, stale); err != nil {
		t.Fatal(err)
	}
	preProject, cleanupPre, err := loadCompareProject(pre)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupPre()
	for _, pou := range preProject.order {
		for _, body := range pou.bodies {
			if body.label != section {
				continue
			}
			for _, entity := range []*oemTextEntity{body.entity, body.pbsh} {
				if entity == nil {
					continue
				}
				if data, err := os.ReadFile(filepath.Join(preProject.m.dir, entity.id+".xml")); err == nil {
					if err := os.WriteFile(filepath.Join(filepath.Dir(mustOEM(t, stale)), entity.id+".xml"), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	staleReport, err := CompareProjects(source, stale)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("compare without call update: %+v", staleReport.Summary)
	flagged := false
	for _, finding := range staleReport.Findings {
		if finding.Item == section+" rung "+rung && finding.Class == CompareUnexplained {
			flagged = true
			t.Logf("without update: %s: %s", finding.Class, finding.Detail)
		}
	}
	if !flagged {
		t.Errorf("compare does not flag the stale call in %s rung %s", section, rung)
	}
	if out := os.Getenv("OMRON_MCP_LIBRARY_SIGNATURE_OUT"); out != "" {
		if err := os.RemoveAll(out); err != nil {
			t.Fatal(err)
		}
		if err := CopySolutionDirectory(work, out); err != nil {
			t.Fatal(err)
		}
	}
}

// sectionRung returns rung index of the ladder section "<POU>/<body>".
func sectionRung(t *testing.T, dir, section, rung string) *ladderJSON {
	t.Helper()
	project, cleanup, err := loadCompareProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, pou := range project.order {
		for _, body := range pou.bodies {
			if body.label != section || body.ladder == nil {
				continue
			}
			for index, line := range body.ladder.rungs() {
				if strconv.Itoa(index) == rung {
					return line.node
				}
			}
		}
	}
	t.Fatalf("%s rung %s not found", section, rung)
	return nil
}
