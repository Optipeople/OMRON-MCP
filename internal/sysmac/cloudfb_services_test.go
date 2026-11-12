package sysmac

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Synthetic extension of the Cloud_FB fixture with telemetry, batch and a
// customer specific stop cause event (fourth event tag of a machine whose
// EventCode is written at runtime). No real project data is used.

const servicesFixtureTypes = `
    <DataType Name="sBatchData" BaseType="STRUCT">
      <DataType Name="BatchNumber" BaseType="STRING[128]" />
      <DataType Name="TotalAmountToProduce" BaseType="UDINT" />
      <DataType Name="ExpectedSpeed" BaseType="UINT" />
    </DataType>
    <DataType Name="eJob" BaseType="ENUM"><DataType Name="Start_Batch" BaseType="DINT" EnumValue="0" /><DataType Name="No_selection" BaseType="DINT" EnumValue="1" /></DataType>
    <DataType Name="sTelemtryGroupSetting" BaseType="STRUCT">
      <DataType Name="Name" BaseType="String[30]" />
      <DataType Name="NoOf_Tag" BaseType="USINT" />
    </DataType>
  </DataType>
</data>`

func writeServicesFixture(t *testing.T) string {
	t.Helper()
	dir := writeCloudFixture(t)
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fb := func(id, name string) string {
		return cloudEntity("FunctionBlock", "StructuredText", id, name, cloudNS, cloudEntity("Variables", "StructuredTextFunctionBlock", id+"-vars", "Variables", "", ""))
	}
	body := func(id, name string) string {
		return cloudEntity("PouBody", "Ladder", id, name, "", cloudEntity("PouBodySourceHolder", "", id+"-pbsh", "PouBodySourceHolder", "", ""))
	}
	oem := cloudFixtureOEM()
	oem = strings.Replace(oem, fb("lib-pc", "fbPartsCounter"), fb("lib-tm", "fbOpti_Telemetry_Service")+fb("lib-bt", "fbOpti_Batch_Service")+fb("lib-pc", "fbPartsCounter"), 1)
	oem = strings.Replace(oem, body("b-alarm2", "IOalarm2"), body("b-alarm2", "IOalarm2")+body("b-tel", "Telemetry")+body("b-scan", "Scanner")+body("b-stop", "StopCause"), 1)
	write("sol.oem", oem)
	write("lib-types.xml", utf8BOM+strings.Replace(cloudFixtureTypes, "  </DataType>\n</data>", servicesFixtureTypes, 1))
	write("lib-tm-vars.xml", slwd("+GN=VAR_INPUT\tGA=InputOutput\tGVT=EdgeInputOutputGroup",
		"++D="+ns+"sTelemtryGroupSetting\tN=ServiceSetting\tOrd=0\tG=VAR_IN_OUT",
		"++D=ARRAY[*] OF "+ns+"sSample\tN=Buffer\tOrd=1\tG=VAR_IN_OUT",
		"++D="+ns+"sSystemInfo\tN=SystemInfo\tOrd=2\tG=VAR_INPUT",
		"++D=BOOL\tN=Enable\tOrd=3\tG=VAR_INPUT",
		"++D=BOOL\tN=Execute_ManualSampling\tOrd=4\tG=VAR_INPUT",
		"++D=BOOL\tN=Done_Publish\tOrd=5\tG=VAR_OUTPUT",
		"++D=BOOL\tN=Busy_publish\tOrd=6\tG=VAR_OUTPUT"))
	write("lib-bt-vars.xml", slwd("+GN=VAR_INPUT\tGA=InputOutput\tGVT=EdgeInputOutputGroup",
		"++D=OmronLib\\MQTT_Comm\\sClientReference\tN=ClientReference\tOrd=0\tG=VAR_IN_OUT",
		"++D="+ns+"sSystemInfo\tN=SystemInfo\tOrd=1\tG=VAR_INPUT",
		"++D=BOOL\tN=Execute\tOrd=2\tG=VAR_INPUT",
		"++D="+ns+"eJob\tN=Job\tOrd=3\tG=VAR_INPUT",
		"++D=USINT\tN=Machine_Id\tOrd=4\tG=VAR_INPUT",
		"++D="+ns+"sBatchData\tN=Batch_Info\tOrd=5\tG=VAR_INPUT",
		"++D=BOOL\tN=Done_Publish\tOrd=6\tG=VAR_OUTPUT",
		"++D=BOOL\tN=Busy_Publish\tOrd=7\tG=VAR_OUTPUT"))
	vars := cloudFixtureFiles()["prog-vars.xml"]
	vars = strings.Replace(vars, "++D="+ns+"fbCloud_EventGroup\tN=fb_event\tG=VAR\r\n",
		"++D="+ns+"fbCloud_EventGroup\tN=fb_event\tG=VAR\r\n++D="+ns+"fbCloud_Telemetry\tN=fb_Telemetry\tG=VAR\r\n++D="+ns+"fbCloud_Batch\tN=fb_batch\tG=VAR\r\n++D=UINT\tN=Speed\tG=VAR\r\n", 1)
	write("prog-vars.xml", vars+"++D="+ns+"sBatchData\tN=BatchInfo\tG=VAR_EXTERNAL\r\n")
	write("globals.xml", slwd("+GN=VAR_GLOBAL\tGVT=GlobalNamespaceGroup",
		"++D=ARRAY[0..2] OF STRING[30]\tN=machineIdRef\tIV=['M1', '', '']\tG=VAR_GLOBAL",
		"++D="+ns+"sBatchData\tN=BatchInfo\tIV=(BatchNumber := 'B1', ExpectedLineSpeed := 5)\tG=VAR_GLOBAL",
		"++D="+ns+"eJob\tN=Job\tIV=Start_Batch\tG=VAR_GLOBAL",
		"++D="+ns+"eJob\tN=Job2\tIV=Create_Batch\tG=VAR_GLOBAL",
		"++D="+ns+"sEventGroup\tN=eventGroup\tIV=(Name := 'MachineState', NoOf_Event := 5, EventTags := ["+
			"(Name := 'state', EventCode := 'Runtime', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), "+
			"(Name := 'state', EventCode := 'Downtime', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), "+
			"(Name := 'state', EventCode := 'Omstilling', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), "+
			"(Name := 'state', EventCode := '', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), "+
			"(Name := 'state', EventCode := 'Runtime', MachineId := 1, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), "+
			"4((Name := '', EventCode := '', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S))], NoOf_Retry_before_cancel_message := 10, WaitTime_before_retry := t#60s)\tG=VAR_GLOBAL"))
	write("b-tel.xml", utf8BOM+
		`{"CLs":[{"__type":"LD","Var":"P_On"},{"__type":"FB","In":[{"__type":"PRM","Arg":"TelemetryGroupSetting","IO":true,"Type":"Toolbox\\Communication\\Cloud\\sTelemtryGroupSetting","Var":"telCfg"},{"__type":"PRM","Arg":"TelemetryGroupBuffer","IO":true,"Ix":1,"Type":"ARRAY[*] OF Toolbox\\Communication\\Cloud\\sSample","Var":"telBuf"},{"__type":"PRM","Arg":"SystemInfo","Ix":2,"Type":"Toolbox\\Communication\\Cloud\\sSystemInfo","Var":"fb_cloudConnect.SystemInfo"},{"__type":"PF","Arg":"Enable"},{"__type":"PRM","Arg":"Execute_ManualSampling","Ix":3,"Type":"BOOL","Var":"sample"}],"Ix":8,"Name":"\\\\Toolbox\\Communication\\Cloud\\fbCloud_Telemetry","Out":[{"__type":"PRM","Arg":"TelemetryGroupSetting","IO":true,"Ix":4,"Type":"Toolbox\\Communication\\Cloud\\sTelemtryGroupSetting","Var":"telCfg"},{"__type":"PRM","Arg":"TelemetryGroupBuffer","IO":true,"Ix":5,"Type":"ARRAY[*] OF Toolbox\\Communication\\Cloud\\sSample","Var":"telBuf"},{"__type":"PF","Arg":"Done_Publish"},{"__type":"PRM","Arg":"Busy_publish","Ix":6,"Type":"BOOL","Var":"busyTel"}],"UD":true,"Var":"fb_Telemetry"}],"LRI":9,"RRI":10,"VLs":[]}`+"\r\n")
	write("b-scan.xml", utf8BOM+
		`{"CLs":[{"__type":"LD","Var":"scan"},{"__type":"F","In":[{"__type":"PF","Arg":"EN"},{"__type":"PRM","Arg":"In","Ix":1,"Type":"ANY","Var":"Speed"}],"Ix":3,"Name":"MOVE","Out":[{"__type":"PF","Arg":"ENO"},{"__type":"PRM","Arg":"Out","Ix":2,"Type":"ANY","Var":"BatchInfo.ExpectedLineSpeed"}],"X":1},{"__type":"FB","In":[{"__type":"PRM","Arg":"ClientReference","IO":true,"Ix":4,"Type":"OmronLib\\MQTT_Comm\\sClientReference","Var":"MQTT"},{"__type":"PRM","Arg":"SystemInfo","Ix":5,"Type":"Toolbox\\Communication\\Cloud\\sSystemInfo","Var":"SystemInfo"},{"__type":"PF","Arg":"Execute"},{"__type":"PRM","Arg":"Job","Ix":6,"Type":"Toolbox\\Communication\\Cloud\\eJob","Var":"Job"},{"__type":"PRM","Arg":"Machine_Id","Ix":7,"Type":"USINT","Var":"0"},{"__type":"PRM","Arg":"Batch_Info","Ix":8,"Type":"Toolbox\\Communication\\Cloud\\sBatchData","Var":"BatchInfo"},{"__type":"PRM","Arg":"Product_Info","Ix":9,"Type":"Toolbox\\Communication\\Cloud\\sProductInfo","Var":"ProductInfo"}],"Ix":13,"Name":"\\\\Toolbox\\Communication\\Cloud\\fbCloud_Batch","Out":[{"__type":"PRM","Arg":"ClientReference","IO":true,"Ix":10,"Type":"OmronLib\\MQTT_Comm\\sClientReference","Var":"MQTT"},{"__type":"PF","Arg":"Done_Publish"},{"__type":"PRM","Arg":"Busy_Publish","Ix":11,"Type":"BOOL","Var":"busyBatch"}],"Var":"fb_batch"},{"__type":"ST","Ix":12,"Var":"scan","X":2}],"LRI":14,"RRI":15,"VLs":[]}`+"\r\n"+
		`{"CLs":[{"__type":"LD","Not":true,"Var":"fb_batch.Done_Publish"},{"__type":"LD","Ix":1,"Var":"BatchInfo.Comment"},{"__type":"ST","Ix":2,"Var":"retry","X":1}],"LRI":3,"RRI":4,"VLs":[]}`+"\r\n")
	write("b-ioevent.xml", utf8BOM+`{"CLs":[{"__type":"LD","Var":"IN_1"},{"__type":"ST","Ix":1,"Var":"eventGroup.EventTags[0].Trig","X":1}],"LRI":2,"RRI":3,"VLs":[]}`+"\r\n")
	write("b-stop.xml", utf8BOM+
		`{"CLs":[{"__type":"LD","Var":"Stop1"},{"__type":"F","In":[{"__type":"PF","Arg":"EN"},{"__type":"PRM","Arg":"In1","Ix":1,"Type":"ANY_ELEMENTARY, ENUM","Var":"eventGroup.EventTags[3].EventCode"},{"__type":"PRM","Arg":"In2","Ix":2,"Type":"ANY_ELEMENTARY, ENUM","Var":"''"}],"Ix":5,"Name":"=","Out":[{"__type":"PF","Arg":""}],"X":1},{"__type":"F","In":[{"__type":"PF","Arg":"EN"},{"__type":"PRM","Arg":"In","Ix":3,"Type":"ANY","Var":"'Jam'"}],"Ix":6,"Name":"MOVE","Out":[{"__type":"PF","Arg":"ENO"},{"__type":"PRM","Arg":"Out","Ix":4,"Type":"ANY","Var":"eventGroup.EventTags[3].EventCode"}],"X":2}],"LRI":7,"RRI":8,"VLs":[]}`+"\r\n"+
		`{"CLs":[{"__type":"LD","Var":"Short"},{"__type":"F","In":[{"__type":"PF","Arg":"EN"},{"__type":"PRM","Arg":"In","Ix":1,"Type":"ANY","Var":"ShortOffset"}],"Ix":4,"Name":"MOVE","Out":[{"__type":"PF","Arg":"ENO"},{"__type":"PRM","Arg":"Out","Ix":2,"Type":"ANY","Var":"eventGroup.EventTags[3].SampleTime_Offset"}],"X":1},{"__type":"ST","Ix":3,"Var":"eventGroup.EventTags[3].Trig","X":2}],"LRI":5,"RRI":6,"VLs":[]}`+"\r\n"+
		`{"CLs":[{"__type":"LD","Var":"Changeover"},{"__type":"F","In":[{"__type":"PF","Arg":"EN"},{"__type":"PRM","Arg":"In","Ix":1,"Type":"ANY","Var":"'Omstilling'"}],"Ix":3,"Name":"MOVE","Out":[{"__type":"PF","Arg":"ENO"},{"__type":"PRM","Arg":"Out","Ix":2,"Type":"ANY","Var":"eventGroup.EventTags[2].EventCode"}],"X":1},{"__type":"ST","Ix":4,"Var":"eventGroup.EventTags[2].Trig","X":2}],"LRI":5,"RRI":6,"VLs":[]}`+"\r\n")
	return dir
}

func TestMigrateCloudFBServicesAndStopCauses(t *testing.T) {
	dir := writeServicesFixture(t)
	result, err := MigrateCloudFBProgram(CloudFBMigrationOptions{ProjectRoot: dir, Now: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range result.Changes {
		t.Logf("%s: %s", change.Target, change.Change)
	}
	for _, item := range result.Unresolved {
		t.Logf("UNRESOLVED %s", item)
	}
	contains := func(list []string, fragment string) bool {
		for _, item := range list {
			if strings.Contains(item, fragment) {
				return true
			}
		}
		return false
	}

	// Telemetry: instance, FB type and the three renamed group pins.
	tel := readFixture(t, dir, "b-tel.xml")
	for _, fragment := range []string{`fbOpti_Telemetry_Service`, `"Var":"TelemetryService"`, `"Arg":"ServiceSetting","IO":true`, `"Arg":"Buffer","IO":true`, `"Var":"ConnectionManager.SystemInfo"`, `"Arg":"Busy_publish","Ix":6,"Type":"BOOL","Var":"busyTel"`} {
		if !strings.Contains(tel, fragment) {
			t.Errorf("telemetry lacks %s:\n%s", fragment, tel)
		}
	}

	// Batch: FB, instance, member rename, Product_Info removed with a warning,
	// Ix shifted by -1 after the pin range.
	scan := readFixture(t, dir, "b-scan.xml")
	for _, fragment := range []string{`fbOpti_Batch_Service`, `"Var":"BatchService"`, `"Var":"BatchInfo.ExpectedSpeed"`, `"Var":"BatchService.Done_Publish"`, `"Ix":12,"Name":"\\\\Toolbox\\Communication\\Cloud\\fbOpti_Batch_Service"`, `"LRI":13,"RRI":14`} {
		if !strings.Contains(scan, fragment) {
			t.Errorf("scanner lacks %s:\n%s", fragment, scan)
		}
	}
	if strings.Contains(scan, "Product_Info") || strings.Contains(scan, "fb_batch") {
		t.Errorf("old batch pin or instance left:\n%s", scan)
	}
	if !contains(result.Warnings, "fbOpti_Batch_Service has no parameter Product_Info; its binding to ProductInfo is removed") {
		t.Errorf("dropped binding not warned: %v", result.Warnings)
	}
	if !contains(result.Unresolved, "BatchInfo.Comment is not a member of sBatchData") {
		t.Errorf("missing member not reported: %v", result.Unresolved)
	}
	if !contains(result.Unresolved, "Job2: initial value Create_Batch is not a member of eJob") {
		t.Errorf("enum value not reported: %v", result.Unresolved)
	}

	vars := readFixture(t, dir, "prog-vars.xml")
	for _, fragment := range []string{"N=TelemetryService", "fbOpti_Telemetry_Service", "N=BatchService", "fbOpti_Batch_Service"} {
		if !strings.Contains(vars, fragment) {
			t.Errorf("program variables lack %s", fragment)
		}
	}
	if strings.Contains(vars, "fbCloud_") {
		t.Errorf("old instances left:\n%s", vars)
	}

	// Events: the fourth tag of machine 0 is kept (extra Stop_Registered),
	// EventCode → Stop_Cause, the backdating offset reference is unchanged;
	// the unused tag of machine 1 is dropped.
	globals := readFixture(t, dir, "globals.xml")
	for _, fragment := range []string{
		"IV=(Name := 'MachineState', NoOf_Event := 4, EventTags := [(Name := 'state', Event_Type := RUN, Stop_Cause := '', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), (Name := 'state', Event_Type := Stop_UnRegistered, Stop_Cause := '', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), (Name := 'state', Event_Type := Stop_Registered, Stop_Cause := 'Omstilling', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), (Name := 'state', Event_Type := Stop_Registered, Stop_Cause := '', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), 5((Name := '', Event_Type := No_EventType_Selected",
		"N=BatchInfo\tIV=(BatchNumber := 'B1', TotalAmountToProduce := 0, ExpectedSpeed := 5)",
	} {
		if !strings.Contains(globals, fragment) {
			t.Errorf("globals lack %s\n%s", fragment, globals)
		}
	}
	stop := readFixture(t, dir, "b-stop.xml")
	if strings.Contains(stop, "EventCode") || strings.Count(stop, "eventGroup.EventTags[3].Stop_Cause") != 2 || !strings.Contains(stop, "eventGroup.EventTags[2].Stop_Cause") || !strings.Contains(stop, "eventGroup.EventTags[3].SampleTime_Offset") || !strings.Contains(stop, "eventGroup.EventTags[3].Trig") {
		t.Errorf("stop cause section:\n%s", stop)
	}
	if !contains(result.Warnings, "EventTags[4] (machine 1, EventCode 'Runtime') dropped: machine 1 is not in the machine list [0]") {
		t.Errorf("unused tag not reported: %v", result.Warnings)
	}
	if contains(result.Unresolved, "EventCode") || contains(result.Unresolved, "no event in the new layout") {
		t.Errorf("event findings left: %v", result.Unresolved)
	}
	if _, err := Inspect(dir); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	again, err := MigrateCloudFBProgram(CloudFBMigrationOptions{ProjectRoot: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Changes) != 0 {
		t.Errorf("second run changed: %v", again.Changes)
	}
}

func TestMigrateCloudFBReportsTypesMissingInNewLibrary(t *testing.T) {
	dir := writeServicesFixture(t)
	path := filepath.Join(dir, "prog-vars.xml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("++D="+ns+"fbOpti_ProductionCounter_service\tN=Counter\tG=VAR_EXTERNAL\r\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := MigrateCloudFBProgram(CloudFBMigrationOptions{ProjectRoot: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range result.Unresolved {
		if strings.Contains(item, "Counter is of type fbOpti_ProductionCounter_service, which NX-IIOT_OptiCloud_V2_2 does not define") {
			return
		}
	}
	t.Errorf("type missing in the new library not reported: %v", result.Unresolved)
}
