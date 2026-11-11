package mcp

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestProgramMigrateCloudFBToolRequiresLibraryReplace(t *testing.T) {
	project := filepath.Join(t.TempDir(), "sol")
	writeFiles(t, project, map[string]string{"sol.oem": libToolProject, "refs.xml": "<data />", "old.xml": "<data />"})
	_, err := callArgs(t, "sysmac_program_migrate_cloudfb", map[string]any{"project_path": project})
	if err == nil || !strings.Contains(err.Error(), "sysmac_library_replace") {
		t.Fatalf("err = %v", err)
	}
}

func TestProgramMigrateCloudFBToolStages(t *testing.T) {
	project := filepath.Join(t.TempDir(), "sol")
	oem := strings.Replace(libToolProject, `<Entity type="Library" subtype="Nex" id="old" name="Old_1_0" trackingId="t2">`+"\r\n"+`            <AccessInfos />`+"\r\n"+`            <ChildEntities />`,
		`<Entity type="Library" subtype="Nex" id="old" name="NX-IIOT_OptiCloud_V2_2" trackingId="t2">`+"\r\n"+`            <AccessInfos />`+"\r\n"+`            <ChildEntities><Entity type="FunctionBlock" subtype="StructuredText" id="cm" name="fbConnectionManager" trackingId="t3"><AccessInfos /><ChildEntities /></Entity></ChildEntities>`, 1)
	if oem == libToolProject {
		t.Fatal("fixture replace failed")
	}
	writeFiles(t, project, map[string]string{"sol.oem": oem})
	result, err := callArgs(t, "sysmac_program_migrate_cloudfb", map[string]any{"project_path": project, "ioalarm": "keep", "machine_ids": []int{0}})
	if err != nil {
		t.Fatal(err)
	}
	content := result["structuredContent"].(map[string]any)
	if content["status"] != "staged" || content["library"] != "NX-IIOT_OptiCloud_V2_2" {
		t.Fatalf("result = %#v", content)
	}
	if _, err := callArgs(t, "sysmac_transaction_rollback", map[string]any{"transaction_id": content["transaction_id"]}); err != nil {
		t.Fatal(err)
	}
}
