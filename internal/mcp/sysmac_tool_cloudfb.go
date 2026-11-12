package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/rjboer/omron-mcp/internal/sysmac"
)

func sysmacCloudFBTools() []tool {
	return []tool{
		{Name: "sysmac_program_migrate_cloudfb", Description: "Migrate the user program of an extracted Sysmac Solution from Cloud_FB 2.0.x to NX-IIOT_OptiCloud_V2 after sysmac_library_replace: replaces fbCloudConnect/fbCloud_TagGroup/fbCloud_EventGroup/fbCloud_Telemetry/fbCloud_Batch instances by ConnectionManager/ProductionCounterService/EventService/TelemetryService/BatchService, retypes sTagGroupSetting/sTagData/sSample variables, renames changed struct members (sBatchData.ExpectedLineSpeed → ExpectedSpeed), converts the part counter settings and the machine state event group (3 events RUN/Stop_UnRegistered/Stop_Registered per machine, machines derived from machineIdRef and code; further event tags used in code are kept as extra events, never dropped), migrates EventTags[n].EventCode to Stop_Cause where the tag is a Stop_Registered event and every write is a stop cause literal, re-synchronises ladder FB calls with the library signatures (pin renames, new pins, Ix shift) and deletes an IOalarm section that only feeds EventCode. Inline ST boxes are never changed. Returns a change log and the findings that still need manual work. Runs in a project transaction; without confirm_commit the changes stay staged.", InputSchema: objectSchema(map[string]any{
			"project_path":   stringProperty("Extracted Sysmac Solution directory (the <GUID> folder with the .oem), already switched to NX-IIOT_OptiCloud_V2 with sysmac_library_replace"),
			"ioalarm":        map[string]any{"type": "string", "enum": []string{"delete", "keep"}, "description": "delete (default): delete an IOalarm section that only writes EventTags[].EventCode/Trig; keep: never delete, only report"},
			"machine_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 0}, "description": "Optional machine ids that get event tags; default: non-empty machineIdRef entries plus machines whose event tags are used in code"},
			"confirm_commit": map[string]any{"type": "boolean", "description": "true commits the transaction to project_path immediately; otherwise it stays staged"},
		}, "project_path")},
	}
}

func sysmacCloudFBToolHandlers() map[string]sysmacToolHandler {
	return map[string]sysmacToolHandler{"sysmac_program_migrate_cloudfb": handleProgramMigrateCloudFB}
}

func handleProgramMigrateCloudFB(call sysmacToolCall) (map[string]any, error) {
	projectPath, err := call.string("project_path", true)
	if err != nil {
		return nil, err
	}
	ioAlarm, err := call.string("ioalarm", false)
	if err != nil {
		return nil, err
	}
	var machines []int
	if raw, ok := call.arguments["machine_ids"]; ok {
		if err := json.Unmarshal(raw, &machines); err != nil {
			return nil, fmt.Errorf("argument \"machine_ids\" must be an array of integers")
		}
	}
	commit := call.confirmed("confirm_commit", "") == nil
	tx, err := sysmac.StartProjectTransaction(projectPath)
	if err != nil {
		return nil, err
	}
	result, err := sysmac.MigrateCloudFBProgram(sysmac.CloudFBMigrationOptions{ProjectRoot: tx.StageRoot, IOAlarm: ioAlarm, MachineIDs: machines})
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	changes, err := tx.Preview()
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	response := map[string]any{"library": result.Library, "machines": result.Machines, "machine_source": result.MachineSource, "changes": result.Changes, "unresolved": result.Unresolved, "warnings": result.Warnings, "written_files": len(result.WrittenFiles), "removed_files": result.RemovedFiles, "file_changes": len(changes)}
	if commit {
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		response["status"] = "committed"
		response["project_path"] = tx.SourceRoot
		return toolSuccess(response), nil
	}
	id := storeTransaction(tx)
	response["status"] = "staged"
	response["transaction_id"] = id
	response["staging_path"] = tx.StageRoot
	return toolSuccess(response), nil
}
