package sysmac

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Synthetic project in the state after ReplaceLibrary: a Cloud_FB program
// referencing an (abbreviated) NX-IIOT_OptiCloud_V2 library. No real project
// data is used.

func cloudEntity(typ, subtype, id, name, extra, children string) string {
	if children == "" {
		return `<Entity type="` + typ + `" subtype="` + subtype + `" id="` + id + `" name="` + name + `" version="0" dateLastModified="01/01/2026 10:00:00"` + extra + ` trackingId="t-` + id + `"><AccessInfos /><ChildEntities /></Entity>`
	}
	return `<Entity type="` + typ + `" subtype="` + subtype + `" id="` + id + `" name="` + name + `" version="0" dateLastModified="01/01/2026 10:00:00"` + extra + ` trackingId="t-` + id + `"><AccessInfos /><ChildEntities>` + children + `</ChildEntities></Entity>`
}

const cloudNS = ` namespace="Toolbox\Communication\Cloud"`

func cloudFixtureOEM() string {
	fb := func(id, name string) string {
		return cloudEntity("FunctionBlock", "StructuredText", id, name, cloudNS, cloudEntity("Variables", "StructuredTextFunctionBlock", id+"-vars", "Variables", "", ""))
	}
	library := cloudEntity("Library", "Nex", "lib", "NX-IIOT_OptiCloud_V2_2", "",
		cloudEntity("DataType", "", "lib-types", "Data Types2", cloudNS, "")+
			fb("lib-cm", "fbConnectionManager")+
			fb("lib-pd", "fbOpti_ProductionData_service")+
			fb("lib-ev", "fbOpti_Event_Service")+
			fb("lib-pc", "fbPartsCounter"))
	body := func(id, name string) string {
		return cloudEntity("PouBody", "Ladder", id, name, "", cloudEntity("PouBodySourceHolder", "", id+"-pbsh", "PouBodySourceHolder", "", ""))
	}
	program := cloudEntity("Program", "MultipartLadder", "prog", "Cloud", "",
		cloudEntity("Variables", "Ladder", "prog-vars", "Variables", "", "")+
			body("b-connect", "Connect")+body("b-tags", "Tags")+body("b-event", "Event")+
			body("b-ioevent", "IOevent")+body("b-alarm", "IOalarm")+body("b-alarm2", "IOalarm2"))
	return `<sysmac schemaVersion="1">` + cloudEntity("Solution", "", "sol", "Demo", "",
		program+
			cloudEntity("Variables", "Global", "globals", "Global Variables", "", "")+
			cloudEntity("ReferredLibraries", "Nex", "refs", "Library", "", library)) + `</sysmac>`
}

const cloudFixtureTypes = `<?xml version="1.0" encoding="utf-8"?>
<data>
  <DataType Name="" BaseType="">
    <DataType Name="sPCtagGroupSetting" BaseType="STRUCT">
      <DataType Name="Name" BaseType="String[30]" />
      <DataType Name="NoOf_Tag" BaseType="USINT" />
      <DataType Name="Publish_methode" BaseType="Toolbox\Communication\Cloud\ePublishMethode" />
      <DataType Name="TagSetting" BaseType="ARRAY[0..2] OF Toolbox\Communication\Cloud\ssPCtagSetting" />
      <DataType Name="PartCounter_format" BaseType="Toolbox\Communication\Cloud\eCounterFormat" />
      <DataType Name="NoOf_Retry_before_cancel_message" BaseType="UINT" />
      <DataType Name="WaitTime_before_retry" BaseType="time" />
    </DataType>
    <DataType Name="ssPCtagSetting" BaseType="STRUCT">
      <DataType Name="Name" BaseType="STRING[32]" />
      <DataType Name="MachineId" BaseType="USINT" />
      <DataType Name="TagType" BaseType="Toolbox\Communication\Cloud\eType_PC" />
    </DataType>
    <DataType Name="sPCtagData" BaseType="STRUCT">
      <DataType Name="PartCounter" BaseType="UDINT" />
      <DataType Name="Measurement_Data" BaseType="REAL" />
      <DataType Name="Quality" BaseType="BOOL" />
    </DataType>
    <DataType Name="sPCsample" BaseType="STRUCT">
      <DataType Name="SampleTime" BaseType="DATE_AND_TIME" />
    </DataType>
    <DataType Name="eCounterFormat" BaseType="ENUM"><DataType Name="Acccumulated" BaseType="DINT" EnumValue="0" /><DataType Name="Sample" BaseType="DINT" EnumValue="1" /></DataType>
    <DataType Name="ePublishMethode" BaseType="ENUM"><DataType Name="NoOf_Samples" BaseType="DINT" EnumValue="0" /><DataType Name="ChangeOf_State" BaseType="DINT" EnumValue="1" /></DataType>
    <DataType Name="eType_PC" BaseType="ENUM"><DataType Name="Part_counter" BaseType="DINT" EnumValue="0" /><DataType Name="Measurement" BaseType="DINT" EnumValue="1" /></DataType>
    <DataType Name="ssEvent" BaseType="STRUCT">
      <DataType Name="Name" BaseType="String[30]" />
      <DataType Name="Event_Type" BaseType="Toolbox\Communication\Cloud\eEventType" />
      <DataType Name="Stop_Cause" BaseType="String[30]" />
      <DataType Name="MachineId" BaseType="USINT" />
      <DataType Name="Value" BaseType="REAL" />
      <DataType Name="Trig" BaseType="BOOL" />
      <DataType Name="SampleTime_Offset" BaseType="TIME" />
    </DataType>
    <DataType Name="sEventGroup" BaseType="STRUCT">
      <DataType Name="Name" BaseType="STRING[30]" />
      <DataType Name="NoOf_Event" BaseType="USINT" />
      <DataType Name="EventTags" BaseType="ARRAY[0..8] OF Toolbox\Communication\Cloud\ssEvent" />
      <DataType Name="NoOf_Retry_before_cancel_message" BaseType="UINT" />
      <DataType Name="WaitTime_before_retry" BaseType="time" />
    </DataType>
    <DataType Name="eEventType" BaseType="ENUM"><DataType Name="No_EventType_Selected" BaseType="DINT" EnumValue="0" /><DataType Name="RUN" BaseType="DINT" EnumValue="1" /><DataType Name="Stop_UnRegistered" BaseType="DINT" EnumValue="2" /><DataType Name="Stop_Registered" BaseType="DINT" EnumValue="3" /></DataType>
    <DataType Name="sSystemInfo" BaseType="STRUCT"><DataType Name="Host" BaseType="String[15]" /></DataType>
  </DataType>
</data>`

func slwd(lines ...string) string {
	return "[SLWD version=1.0]\r\n_EN=Variables\r\n" + strings.Join(lines, "\r\n") + "\r\n"
}

const ns = `Toolbox\Communication\Cloud\`

func cloudFixtureFiles() map[string]string {
	return map[string]string{
		"sol.oem":       cloudFixtureOEM(),
		"lib-types.xml": utf8BOM + cloudFixtureTypes,
		"lib-cm-vars.xml": slwd("+GN=VAR\tGVT=DefaultGroup",
			"+GN=VAR_INPUT\tGA=InputOutput\tGVT=EdgeInputOutputGroup",
			"++D=BOOL\tN=Enable\tOrd=0\tG=VAR_INPUT",
			"++D=STRING[256]\tN=HostAdr\tOrd=1\tG=VAR_INPUT",
			"++D=BOOL\tN=Connected\tOrd=2\tG=VAR_OUTPUT",
			"++D=BOOL\tN=Heartbeat_running\tOrd=3\tG=VAR_OUTPUT",
			"++D="+ns+"sSystemInfo\tN=SystemInfo\tOrd=4\tG=VAR_OUTPUT",
			"++D=OmronLib\\MQTT_Comm\\sClientReference\tN=ClientReference\tOrd=5\tG=VAR_IN_OUT"),
		"lib-pd-vars.xml": slwd("+GN=VAR_INPUT\tGA=InputOutput\tGVT=EdgeInputOutputGroup",
			"++D="+ns+"sPCtagGroupSetting\tN=ServiceSetting\tOrd=0\tG=VAR_IN_OUT",
			"++D=ARRAY[*] OF "+ns+"sPCsample\tN=Buffer\tOrd=1\tG=VAR_IN_OUT",
			"++D=BOOL\tN=Enable\tOrd=2\tG=VAR_INPUT",
			"++D=BOOL\tN=Clear_Buffer\tE=UpDirection\tOrd=3\tG=VAR_INPUT",
			"++D=BOOL\tN=Done_Publish\tOrd=4\tG=VAR_OUTPUT"),
		"lib-ev-vars.xml": slwd("+GN=VAR_INPUT\tGA=InputOutput\tGVT=EdgeInputOutputGroup",
			"++D="+ns+"sEventGroup\tN=Event_Settings\tOrd=0\tG=VAR_IN_OUT",
			"++D=ARRAY[*] OF STRING[30]\tN=MachineId_ref\tOrd=1\tG=VAR_IN_OUT",
			"++D=BOOL\tN=Enable\tOrd=2\tG=VAR_INPUT",
			"++D=BOOL\tN=Done_Publish\tOrd=3\tG=VAR_OUTPUT"),
		"lib-pc-vars.xml": slwd("+GN=VAR_INPUT\tGA=InputOutput\tGVT=EdgeInputOutputGroup",
			"++D=BOOL\tN=Enable\tOrd=0\tG=VAR_INPUT",
			"++D=ARRAY[*] OF "+ns+"sPCtagData\tN=TagData\tOrd=1\tG=VAR_IN_OUT",
			"++D=BOOL\tN=Enabled\tOrd=2\tG=VAR_OUTPUT"),
		"prog-vars.xml": slwd("+GN=VAR\tGVT=DefaultGroup",
			"++D="+ns+"fbCloudConnect\tN=fb_cloudConnect\tG=VAR",
			"++D=STRING[256]\tN=SAS\tG=VAR",
			"++D="+ns+"fbCloud_TagGroup\tN=fb_tags\tG=VAR",
			"++D="+ns+"sTagGroupSetting\tN=tagCFG\tG=VAR",
			"++D=ARRAY[0..9] OF "+ns+"sSample\tN=Buf\tG=VAR",
			"++D="+ns+"fbCloud_EventGroup\tN=fb_event\tG=VAR",
			"+GN=VAR_EXTERNAL\tGA=External\tGVT=ExternalGroup",
			"++D="+ns+"sTagGroupSetting\tN=cfg\tG=VAR_EXTERNAL",
			"++D="+ns+"sEventGroup\tN=eventGroup\tG=VAR_EXTERNAL",
			"++D=ARRAY[0..2] OF STRING[30]\tN=machineIdRef\tG=VAR_EXTERNAL"),
		"globals.xml": slwd("+GN=VAR_GLOBAL\tGVT=GlobalNamespaceGroup",
			"++D="+ns+"sTagGroupSetting\tN=cfg\tIV=(Name := 'PartCounter', NoOf_Tag := 1, TagSetting := [(Name := 'Counter', MachineId := 0, TagType := Part_counter), 2((Name := '', MachineId := 0, TagType := Part_counter))], PartCounter_format := Sample, NoOf_Retry_before_cancel_message := 0, WaitTime_before_retry := t#0s)\tG=VAR_GLOBAL",
			"++D=ARRAY[0..1] OF "+ns+"sTagData\tN=TagData\tG=VAR_GLOBAL",
			"++D=ARRAY[0..2] OF STRING[30]\tN=machineIdRef\tIV=['M1', '', 'M3']\tG=VAR_GLOBAL",
			"++D="+ns+"sEventGroup\tN=eventGroup\tIV=(Name := 'MachineState', NoOf_Event := 9, EventTags := ["+
				"(Name := 'state', EventCode := 'runtime', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), (Name := 'state', EventCode := 'downtime', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), (Name := 'state', EventCode := '', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), "+
				"(Name := 'state', EventCode := 'runtime', MachineId := 1, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), (Name := 'state', EventCode := 'downtime', MachineId := 1, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), (Name := 'state', EventCode := '', MachineId := 1, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), "+
				"(Name := 'state', EventCode := 'runtime', MachineId := 2, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), (Name := 'state', EventCode := 'downtime', MachineId := 2, Value := 0.0, Trig := False, SampleTime_Offset := T#5S), (Name := 'state', EventCode := '', MachineId := 2, Value := 0.0, Trig := False, SampleTime_Offset := T#0S)"+
				"], NoOf_Retry_before_cancel_message := 0, WaitTime_before_retry := t#0s)\tG=VAR_GLOBAL"),
		"b-connect.xml": utf8BOM +
			`{"CLs":[{"__type":"LD","Var":"P_First_Run"},{"__type":"IST","EID":"e1","Ix":1,"TXT":"SAS:='secret-do-not-touch';\u000d\u000afb_cloudConnect.Enable := TRUE;","X":1}],"LRI":2,"RRI":3,"VLs":[]}` + "\r\n" +
			`{"CLs":[{"__type":"LD","Var":"Connect"},{"__type":"FB","In":[{"__type":"PF","Arg":"Enable"},{"__type":"PRM","Arg":"HostAdr","Ix":1,"Type":"STRING[256]","Var":"'host'"},{"__type":"PRM","Arg":"ClientReference","IO":true,"Ix":2,"Type":"OmronLib\\MQTT_Comm\\sClientReference","Var":"MQTT"}],"Ix":5,"Name":"\\\\Toolbox\\Communication\\Cloud\\fbCloudConnect","Out":[{"__type":"PF","Arg":"Connected"},{"__type":"PRM","Arg":"SystemInfo","Ix":3,"Type":"Toolbox\\Communication\\Cloud\\sSystemInfo","Var":""},{"__type":"PRM","Arg":"ClientReference","IO":true,"Ix":4,"Type":"OmronLib\\MQTT_Comm\\sClientReference","Var":"MQTT"}],"UD":true,"X":1,"Var":"fb_cloudConnect"}],"LRI":6,"RRI":7,"VLs":[{"Ix":8,"X":1}]}` + "\r\n" +
			`{"CLs":[{"__type":"LD","Not":true,"Var":"fb_cloudConnect.Connected"},{"__type":"ST","Ix":1,"Var":"x","X":1}],"CMT":"a\/b","LRI":2,"RRI":3,"VLs":[]}` + "\r\n",
		"b-connect-pbsh.xml": utf8BOM + `<?xml version="1.0" encoding="utf-8"?><data><ElementUriList ElementUriList="/Rung%5B1%5D/Element%5BN5%5D" /><ElementUriList ElementUriList="/Rung%5B1%5D/Element%5BN0%5D" /><ElementUriList ElementUriList="/Rung%5B2%5D/Element%5BN5%5D" /></data>`,
		"b-tags.xml": utf8BOM +
			`{"CLs":[{"__type":"FB","In":[{"__type":"PRM","Arg":"TagGroupSetting","IO":true,"Type":"Toolbox\\Communication\\Cloud\\sTagGroupSetting","Var":"cfg"},{"__type":"PRM","Arg":"TagGroupBuffer","IO":true,"Ix":1,"Type":"ARRAY[*] OF Toolbox\\Communication\\Cloud\\sSample","Var":"Buf"},{"__type":"PF","Arg":"Enable"},{"__type":"PRM","Arg":"Clear_TagGroupBuffer","Ix":2,"Type":"BOOL","Up":true,"Var":"clr"}],"Ix":6,"Name":"\\\\Toolbox\\Communication\\Cloud\\fbCloud_TagGroup","Out":[{"__type":"PRM","Arg":"TagGroupSetting","IO":true,"Ix":3,"Type":"Toolbox\\Communication\\Cloud\\sTagGroupSetting","Var":"cfg"},{"__type":"PRM","Arg":"TagGroupBuffer","IO":true,"Ix":4,"Type":"ARRAY[*] OF Toolbox\\Communication\\Cloud\\sSample","Var":"Buf"},{"__type":"PF","Arg":"Done_Publish"},{"__type":"PRM","Arg":"Busy","Ix":5,"Type":"BOOL","Var":"busy"}],"UD":true,"Var":"fb_tags"}],"LRI":7,"RRI":8,"VLs":[]}` + "\r\n",
		"b-event.xml": utf8BOM +
			`{"CLs":[{"__type":"FB","In":[{"__type":"PRM","Arg":"EventGroup","IO":true,"Type":"Toolbox\\Communication\\Cloud\\sEventGroup","Var":"eventGroup"},{"__type":"PRM","Arg":"MachineId_ref","IO":true,"Ix":1,"Type":"ARRAY[*] OF STRING[30]","Var":"machineIdRef"},{"__type":"PF","Arg":"Enable"}],"Ix":4,"Name":"\\\\Toolbox\\Communication\\Cloud\\fbCloud_EventGroup","Out":[{"__type":"PRM","Arg":"EventGroup","IO":true,"Ix":2,"Type":"Toolbox\\Communication\\Cloud\\sEventGroup","Var":"eventGroup"},{"__type":"PRM","Arg":"MachineId_ref","IO":true,"Ix":3,"Type":"ARRAY[*] OF STRING[30]","Var":"machineIdRef"},{"__type":"PF","Arg":"Done_Publish"}],"UD":true,"Var":"fb_event"}],"LRI":5,"RRI":6,"VLs":[]}` + "\r\n",
		"b-ioevent.xml": utf8BOM +
			`{"CLs":[{"__type":"LD","Var":"IN_1"},{"__type":"ST","Ix":1,"Var":"eventGroup.EventTags[0].Trig","X":1}],"LRI":2,"RRI":3,"VLs":[]}` + "\r\n" +
			`{"CLs":[{"__type":"LD","Var":"IN_3"},{"__type":"ST","Ix":1,"Var":"eventGroup.EventTags[7].Trig","X":1}],"LRI":2,"RRI":3,"VLs":[]}` + "\r\n",
		"b-alarm.xml": utf8BOM +
			`{"CLs":[{"__type":"LD","Var":"IN_10"},{"__type":"F","In":[{"__type":"PF","Arg":"EN"},{"__type":"PRM","Arg":"In","Ix":1,"Type":"ANY","Var":"'cause'"}],"Ix":3,"Name":"MOVE","Out":[{"__type":"PF","Arg":"ENO"},{"__type":"PRM","Arg":"Out","Ix":2,"Type":"ANY","Var":"eventGroup.EventTags[2].EventCode"}],"X":1}],"LRI":4,"RRI":5,"VLs":[]}` + "\r\n",
		"b-alarm2.xml": utf8BOM +
			`{"CLs":[{"__type":"LD","Var":"IN_11"},{"__type":"F","In":[{"__type":"PF","Arg":"EN"},{"__type":"PRM","Arg":"In","Ix":1,"Type":"ANY","Var":"'cause'"}],"Ix":3,"Name":"MOVE","Out":[{"__type":"PF","Arg":"ENO"},{"__type":"PRM","Arg":"Out","Ix":2,"Type":"ANY","Var":"eventGroup.EventTags[2].EventCode"}],"X":1}],"LRI":4,"RRI":5,"VLs":[]}` + "\r\n",
	}
}

func writeCloudFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sol")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range cloudFixtureFiles() {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readFixture(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMigrateCloudFBProgram(t *testing.T) {
	dir := writeCloudFixture(t)
	original := cloudFixtureFiles()
	result, err := MigrateCloudFBProgram(CloudFBMigrationOptions{ProjectRoot: dir, Now: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range result.Changes {
		t.Logf("%s: %s", change.Target, change.Change)
	}

	// Program variables: old instances replaced at the end of VAR, unused
	// sTagGroupSetting local deleted, buffer retyped.
	vars := readFixture(t, dir, "prog-vars.xml")
	want := slwd("+GN=VAR\tGVT=DefaultGroup",
		"++D=STRING[256]\tN=SAS\tG=VAR",
		"++D=ARRAY[0..9] OF "+ns+"sPCSample\tN=Buf\tG=VAR",
		"++D="+ns+"fbConnectionManager\tN=ConnectionManager\tG=VAR",
		"++D="+ns+"fbOpti_ProductionData_service\tN=ProductionCounterService\tG=VAR",
		"++D="+ns+"fbOpti_Event_Service\tN=EventService\tG=VAR",
		"+GN=VAR_EXTERNAL\tGA=External\tGVT=ExternalGroup",
		"++D="+ns+"sPCTagGroupSetting\tN=cfg\tG=VAR_EXTERNAL",
		"++D="+ns+"sEventGroup\tN=eventGroup\tG=VAR_EXTERNAL",
		"++D=ARRAY[0..2] OF STRING[30]\tN=machineIdRef\tG=VAR_EXTERNAL")
	if vars != want {
		t.Errorf("program variables:\n%s\nwant\n%s", vars, want)
	}

	// Globals: machines 0 and 2 (machineIdRef), retry defaults.
	globals := readFixture(t, dir, "globals.xml")
	for _, fragment := range []string{
		"N=cfg\tIV=(Name := 'PartCounter', NoOf_Tag := 1, Publish_methode := ChangeOf_State, TagSetting := [(Name := 'Counter', MachineId := 0, TagType := Part_counter), 2((Name := '', MachineId := 0, TagType := Part_counter))], PartCounter_format := Sample, NoOf_Retry_before_cancel_message := 10, WaitTime_before_retry := T#10S)",
		"++D=ARRAY[0..1] OF " + ns + "sPCTagData\tN=TagData\tIV=[2((PartCounter := 0, Measurement_Data := 0.0, Quality := False))]",
		"IV=(Name := 'MachineState', NoOf_Event := 6, EventTags := [(Name := 'state', Event_Type := RUN, Stop_Cause := '', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), (Name := 'state', Event_Type := Stop_UnRegistered, Stop_Cause := '', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), (Name := 'state', Event_Type := Stop_Registered, Stop_Cause := '', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), (Name := 'state', Event_Type := RUN, Stop_Cause := '', MachineId := 2, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), (Name := 'state', Event_Type := Stop_UnRegistered, Stop_Cause := '', MachineId := 2, Value := 0.0, Trig := False, SampleTime_Offset := T#5S), (Name := 'state', Event_Type := Stop_Registered, Stop_Cause := '', MachineId := 2, Value := 0.0, Trig := False, SampleTime_Offset := T#0S), 3((Name := '', Event_Type := No_EventType_Selected, Stop_Cause := '', MachineId := 0, Value := 0.0, Trig := False, SampleTime_Offset := T#0S))], NoOf_Retry_before_cancel_message := 10, WaitTime_before_retry := T#60S)",
	} {
		if !strings.Contains(globals, fragment) {
			t.Errorf("globals lack %s\n%s", fragment, globals)
		}
	}
	if len(result.Machines) != 2 || result.Machines[0] != 0 || result.Machines[1] != 2 {
		t.Errorf("machines = %v", result.Machines)
	}

	// Connect: inline ST untouched, new pin, Ix shift, link info shift.
	connect := readFixture(t, dir, "b-connect.xml")
	lines := strings.Split(connect, "\r\n")
	if lines[0] != strings.Split(original["b-connect.xml"], "\r\n")[0] {
		t.Error("rung with inline ST was modified")
	}
	wantRung1 := `{"CLs":[{"__type":"LD","Var":"Connect"},{"__type":"FB","In":[{"__type":"PF","Arg":"Enable"},{"__type":"PRM","Arg":"HostAdr","Ix":1,"Type":"STRING[256]","Var":"'host'"},{"__type":"PRM","Arg":"ClientReference","IO":true,"Ix":2,"Type":"OmronLib\\MQTT_Comm\\sClientReference","Var":"MQTT"}],"Ix":6,"Name":"\\\\Toolbox\\Communication\\Cloud\\fbConnectionManager","Out":[{"__type":"PF","Arg":"Connected"},{"__type":"PRM","Arg":"Heartbeat_running","Ix":3,"Type":"BOOL","Var":""},{"__type":"PRM","Arg":"ClientReference","IO":true,"Ix":4,"Type":"OmronLib\\MQTT_Comm\\sClientReference","Var":"MQTT"},{"__type":"PRM","Arg":"SystemInfo","Ix":5,"Type":"Toolbox\\Communication\\Cloud\\sSystemInfo","Var":""}],"UD":true,"X":1,"Var":"ConnectionManager"}],"LRI":7,"RRI":8,"VLs":[{"Ix":9,"X":1}]}`
	if lines[1] != wantRung1 {
		t.Errorf("connect rung 1:\n%s\nwant\n%s", lines[1], wantRung1)
	}
	if !strings.Contains(lines[2], `"Var":"ConnectionManager.Connected"`) || !strings.Contains(lines[2], `"CMT":"a\/b"`) {
		t.Errorf("connect rung 2: %s", lines[2])
	}
	pbsh := readFixture(t, dir, "b-connect-pbsh.xml")
	if !strings.Contains(pbsh, "/Rung%5B1%5D/Element%5BN6%5D") || !strings.Contains(pbsh, "/Rung%5B1%5D/Element%5BN0%5D") || !strings.Contains(pbsh, "/Rung%5B2%5D/Element%5BN5%5D") {
		t.Errorf("link info not shifted per rung: %s", pbsh)
	}
	foundIST := false
	for _, item := range result.Unresolved {
		foundIST = foundIST || strings.Contains(item, "inline ST references fb_cloudConnect")
		if strings.Contains(item, "secret") {
			t.Error("inline ST text leaked into the report")
		}
	}
	if !foundIST {
		t.Errorf("inline ST reference not reported: %v", result.Unresolved)
	}

	// Tags: pins renamed, unknown pin dropped, types from the signature.
	tags := readFixture(t, dir, "b-tags.xml")
	for _, fragment := range []string{`"Arg":"ServiceSetting","IO":true,"Type":"Toolbox\\Communication\\Cloud\\sPCtagGroupSetting","Var":"cfg"`, `"Arg":"Clear_Buffer","Ix":2,"Type":"BOOL","Up":true,"Var":"clr"`, `fbOpti_ProductionData_service`, `"Var":"ProductionCounterService"`, `"LRI":6,"RRI":7`} {
		if !strings.Contains(tags, fragment) {
			t.Errorf("tags lack %s:\n%s", fragment, tags)
		}
	}
	if strings.Contains(tags, "Busy") {
		t.Error("pin without counterpart kept")
	}

	// Event tags of machine 2 moved from 6..8 to 3..5.
	if ioevent := readFixture(t, dir, "b-ioevent.xml"); !strings.Contains(ioevent, "eventGroup.EventTags[4].Trig") || !strings.Contains(ioevent, "eventGroup.EventTags[0].Trig") {
		t.Errorf("event index not remapped: %s", ioevent)
	}

	// IOalarm deleted from index and disk; IOalarm2 (not named IOalarm) kept and reported.
	if _, err := os.Stat(filepath.Join(dir, "b-alarm.xml")); !os.IsNotExist(err) {
		t.Error("IOalarm payload not removed")
	}
	oem := readFixture(t, dir, "sol.oem")
	if strings.Contains(oem, `name="IOalarm"`) || !strings.Contains(oem, `name="IOalarm2"`) {
		t.Error("IOalarm not removed from index or IOalarm2 removed")
	}
	if !strings.Contains(oem, `id="b-tags" name="Tags" version="0" dateLastModified="11/11/2026 12:00:00"`) {
		t.Error("modified body not stamped")
	}
	reported := false
	for _, item := range result.Unresolved {
		reported = reported || strings.Contains(item, "IOalarm2 writes EventTags[].EventCode")
	}
	if !reported {
		t.Errorf("IOalarm2 not reported: %v", result.Unresolved)
	}
	if _, err := Inspect(dir); err != nil {
		t.Fatalf("inspect: %v", err)
	}

	// Second run is a no-op.
	again, err := MigrateCloudFBProgram(CloudFBMigrationOptions{ProjectRoot: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Changes) != 0 {
		t.Errorf("second run changed: %v", again.Changes)
	}
}

func TestMigrateCloudFBKeepsIOAlarmWithOtherLogic(t *testing.T) {
	dir := writeCloudFixture(t)
	alarm := utf8BOM + `{"CLs":[{"__type":"LD","Var":"IN_10"},{"__type":"ST","Ix":1,"Var":"Horn","X":1}],"LRI":2,"RRI":3,"VLs":[]}` + "\r\n" +
		`{"CLs":[{"__type":"LD","Var":"IN_10"},{"__type":"ST","Ix":1,"Var":"eventGroup.EventTags[2].EventCode","X":1}],"LRI":2,"RRI":3,"VLs":[]}` + "\r\n"
	if err := os.WriteFile(filepath.Join(dir, "b-alarm.xml"), []byte(alarm), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := MigrateCloudFBProgram(CloudFBMigrationOptions{ProjectRoot: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b-alarm.xml")); err != nil {
		t.Fatal("IOalarm with other logic was deleted")
	}
	found := false
	for _, item := range result.Unresolved {
		found = found || strings.Contains(item, "Cloud/IOalarm uses EventTags[].EventCode but also a coil on Horn")
	}
	if !found {
		t.Errorf("not reported: %v", result.Unresolved)
	}
}

func TestMigrateCloudFBRequiresNewLibrary(t *testing.T) {
	dir := writeCloudFixture(t)
	oem := strings.Replace(cloudFixtureOEM(), `name="fbConnectionManager"`, `name="fbCloudConnect"`, 1)
	if err := os.WriteFile(filepath.Join(dir, "sol.oem"), []byte(oem), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateCloudFBProgram(CloudFBMigrationOptions{ProjectRoot: dir}); err == nil || !strings.Contains(err.Error(), "sysmac_library_replace") {
		t.Fatalf("err = %v", err)
	}
}

func TestSTLiteralRoundTrip(t *testing.T) {
	for _, text := range []string{
		"(Name := 'PartCounter', NoOf_Tag := 2, TagSetting := [(Name := 'a$'b', MachineId := 0, TagType := Part_counter), 23((Name := '', MachineId := 0, TagType := Part_counter))], WaitTime_before_retry := t#0s)",
		"['OM01037', 4('')]",
		"[25((PartCounter := 0, Measurement_Data := 0.0, Quality := False))]",
	} {
		literal, err := parseSTLiteral(text)
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		if literal.format() != text {
			t.Errorf("round trip:\n%s\n%s", literal.format(), text)
		}
	}
	for text, zero := range map[string]bool{"t#0s": true, "T#0S": true, "T#10S": false, "0": true, "10": false, "T#1m30s": false} {
		if stIsZero(stScalar(text)) != zero {
			t.Errorf("stIsZero(%s) != %v", text, zero)
		}
	}
}

func TestLadderJSONRoundTrip(t *testing.T) {
	line := `{"CLs":[{"__type":"IST","TXT":"a:='x';\u000d\u000ab","X":1}],"CMT":"a\/b \"q\"","LRI":2,"VLs":[]}`
	node, err := parseLadderJSON(line)
	if err != nil {
		t.Fatal(err)
	}
	if node.render() != line {
		t.Fatalf("round trip: %s", node.render())
	}
	if got := encodeLadderString("a/b\\c\r"); got != `"a\/b\\c\u000d"` {
		t.Fatalf("encode = %s", got)
	}
}
