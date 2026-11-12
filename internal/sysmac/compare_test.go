package sysmac

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func migratedServicesFixture(t *testing.T) (string, string) {
	t.Helper()
	before := writeServicesFixture(t)
	after := filepath.Join(t.TempDir(), "sol")
	if err := CopySolutionDirectory(before, after); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateCloudFBProgram(CloudFBMigrationOptions{ProjectRoot: after, Now: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	return before, after
}

func findingsOf(report CompareReport, class CompareClass) []CompareFinding {
	var result []CompareFinding
	for _, finding := range report.Findings {
		if finding.Class == class {
			result = append(result, finding)
		}
	}
	return result
}

func TestCompareProjectsAfterMigration(t *testing.T) {
	before, after := migratedServicesFixture(t)
	report, err := CompareProjects(before, after)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range report.Findings {
		t.Logf("%s %s %s %v: %s", finding.Class, finding.Area, finding.Item, finding.Rules, finding.Detail)
	}
	// The fixture's IOevent/IOalarm2 sections are kept and its missing
	// BatchInfo.Comment member is a known unresolved item; nothing of the
	// migrated functions may be reported missing apart from that rung.
	for _, finding := range findingsOf(report, CompareMissing) {
		if !strings.Contains(finding.Item, "Scanner rung 1") {
			t.Errorf("unexpected missing function: %+v", finding)
		}
	}
	expect := map[string]string{
		"Cloud/Scanner rung 0":    "FB-SERVICE",
		"Cloud/Telemetry rung 0":  "FB-PINS",
		"Cloud/StopCause rung 0":  "EVENT-STOPCAUSE",
		"eventGroup.EventTags[3]": "EVENT-LAYOUT",
		"Cloud/IOalarm":           "IOALARM",
	}
	for item, rule := range expect {
		found := false
		for _, finding := range report.Findings {
			if finding.Item == item && finding.Class == CompareExpected {
				for _, got := range finding.Rules {
					found = found || got == rule
				}
			}
		}
		if !found {
			t.Errorf("%s not classified as expected with %s", item, rule)
		}
	}
	if !report.EventOrder["eventGroup"] {
		t.Error("event order should be identical (tags 0..3 kept in place)")
	}
	// Removed Product_Info binding: ProductInfo is never written.
	for _, service := range report.Services {
		if service.OldFB == "fbCloud_Batch" && (service.Class != CompareExpected || !strings.Contains(service.Detail, "Product_Info=ProductInfo fjernet")) {
			t.Errorf("batch service: %+v", service)
		}
	}
	markdown := RenderCompareMarkdown(report, "")
	for _, fragment := range []string{"## (c) MANGLER", "Event-rækkefølge for `eventGroup` identisk", "## Regler"} {
		if !strings.Contains(markdown, fragment) {
			t.Errorf("markdown lacks %s", fragment)
		}
	}
	if strings.Contains(markdown, "secret-do-not-touch") {
		t.Error("inline ST secret leaked into the report")
	}
}

func TestCompareProjectsDetectsLostFunctions(t *testing.T) {
	before, after := migratedServicesFixture(t)
	globals := filepath.Join(after, "globals.xml")
	data, err := os.ReadFile(globals)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	// Drop the fourth (stop cause) event and rename a part counter tag.
	broken := strings.Replace(text, "NoOf_Event := 4", "NoOf_Event := 3", 1)
	broken = strings.Replace(broken, "(Name := 'state', Event_Type := Stop_Registered, Stop_Cause := '', MachineId := 0", "(Name := '', Event_Type := No_EventType_Selected, Stop_Cause := '', MachineId := 0", 1)
	if broken == text {
		t.Fatal("fixture edit failed")
	}
	if err := os.WriteFile(globals, []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	// Part counter tag list in the old project vs. a changed one.
	cfgBefore := filepath.Join(before, "globals.xml")
	old, _ := os.ReadFile(cfgBefore)
	withCounter := strings.Replace(string(old), "++D=ARRAY[0..2] OF STRING[30]\tN=machineIdRef", "++D="+ns+"sTagGroupSetting\tN=cfg\tIV=(Name := 'PartCounter', NoOf_Tag := 3, TagSetting := [(Name := 'Counter', MachineId := 0, TagType := Part_counter), (Name := '', MachineId := 0, TagType := Part_counter), (Name := 'Meter', MachineId := 0, TagType := Part_counter), 0((Name := '', MachineId := 0, TagType := Part_counter))], PartCounter_format := Sample)\tG=VAR_GLOBAL\r\n++D=ARRAY[0..2] OF STRING[30]\tN=machineIdRef", 1)
	if err := os.WriteFile(cfgBefore, []byte(withCounter), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globals, []byte(strings.Replace(broken, "++D=ARRAY[0..2] OF STRING[30]\tN=machineIdRef", "++D="+ns+"sPCTagGroupSetting\tN=cfg\tIV=(Name := 'PartCounter', NoOf_Tag := 2, Publish_methode := ChangeOf_State, TagSetting := [(Name := 'Counter', MachineId := 0, TagType := Part_counter), (Name := 'Meter', MachineId := 0, TagType := Part_counter), (Name := '', MachineId := 0, TagType := Part_counter)], PartCounter_format := Sample)\tG=VAR_GLOBAL\r\n++D=ARRAY[0..2] OF STRING[30]\tN=machineIdRef", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	// An unchanged rung that still references EventCode after the library
	// change is broken even though it is byte-identical.
	stop := filepath.Join(after, "b-stop.xml")
	stopData, _ := os.ReadFile(stop)
	if err := os.WriteFile(stop, []byte(strings.Replace(string(stopData), "eventGroup.EventTags[2].Stop_Cause", "eventGroup.EventTags[2].EventCode", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := CompareProjects(before, after)
	if err != nil {
		t.Fatal(err)
	}
	missing := map[string]string{}
	for _, finding := range findingsOf(report, CompareMissing) {
		missing[finding.Item] = finding.Detail
		t.Logf("missing %s: %s", finding.Item, finding.Detail)
	}
	if !strings.Contains(missing["eventGroup.EventTags[3]"], "bruges i koden") {
		t.Error("lost stop cause event not reported missing")
	}
	if !strings.Contains(missing["cfg"], "tag-listen er ikke identisk") {
		t.Error("changed part counter tag list not reported missing")
	}
	if !strings.Contains(missing["Cloud/StopCause rung 2"], "eventGroup.EventTags[2].EventCode") {
		t.Error("reference to a removed member not reported")
	}
	for _, group := range report.TagGroups {
		if group.Variable == "cfg" && group.TagListIdentical {
			t.Error("tag list reported identical")
		}
	}
}

// A project that is already on V2 (event group in V2 layout) compared with
// itself is identical: no V1 interpretation of its event tags.
func TestCompareProjectsAlreadyV2(t *testing.T) {
	_, after := migratedServicesFixture(t)
	report, err := CompareProjects(after, after)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's deliberate BatchInfo.Comment reference is the only finding.
	if report.Summary.Missing != 1 || report.Summary.Unexplained != 0 || report.Summary.Expected != 0 {
		for _, finding := range report.Findings {
			t.Logf("%+v", finding)
		}
		t.Fatalf("summary = %+v", report.Summary)
	}
}
