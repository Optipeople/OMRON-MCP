package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rjboer/omron-mcp/internal/sysmac"
)

func sysmacCompareTools() []tool {
	return []tool{
		{Name: "sysmac_program_compare", Description: "Function inventory of an old (before) and a new (after migration) Sysmac project for a technician's approval: POUs/sections and every rung (raw and normalised hash, so unchanged rungs show as identical), I/O mapping (globals with AT), all variables (name/type/initial value), events (count, types, MachineId, which inputs trigger them, offset and stop cause writers, order), part counter and telemetry tag lists (number, order, names, empty slots, MachineId, publish method), cloud service FB calls (bindings) and the IoT Hub connection (host, ClientID, machineIdRef; never SAS tokens). Every difference is classified as (a) expected migration change with rule id, (b) unexplained change or (c) missing function; rungs that still reference members the new library does not have count as missing. Read-only; accepts extracted Solution folders or .smc2 files.", InputSchema: objectSchema(map[string]any{
			"old_path":    stringProperty("Project before the migration: extracted Solution folder or .smc2"),
			"new_path":    stringProperty("Project after the migration: extracted Solution folder or .smc2"),
			"detail":      map[string]any{"type": "string", "enum": []string{"findings", "full"}, "description": "findings (default): summary, all non-identical findings and the key inventories; full: the complete inventory incl. every rung and variable"},
			"output_path": stringProperty("Optional .md file to write the Danish markdown report to (an existing file is overwritten)"),
			"title":       stringProperty("Optional title of the markdown report"),
		}, "old_path", "new_path")},
	}
}

func sysmacCompareToolHandlers() map[string]sysmacToolHandler {
	return map[string]sysmacToolHandler{"sysmac_program_compare": handleProgramCompare}
}

func handleProgramCompare(call sysmacToolCall) (map[string]any, error) {
	oldPath, err := call.string("old_path", true)
	if err != nil {
		return nil, err
	}
	newPath, err := call.string("new_path", true)
	if err != nil {
		return nil, err
	}
	detail, err := call.string("detail", false)
	if err != nil {
		return nil, err
	}
	output, err := call.string("output_path", false)
	if err != nil {
		return nil, err
	}
	title, err := call.string("title", false)
	if err != nil {
		return nil, err
	}
	report, err := sysmac.CompareProjects(oldPath, newPath)
	if err != nil {
		return nil, err
	}
	response := map[string]any{
		"old_project":           report.OldProject,
		"new_project":           report.NewProject,
		"old_libraries":         report.OldLibraries,
		"new_libraries":         report.NewLibraries,
		"summary":               report.Summary,
		"findings":              nonIdentical(report.Findings),
		"connection":            report.Connection,
		"event_order_identical": report.EventOrder,
		"events":                report.Events,
		"tag_groups":            report.TagGroups,
		"services":              report.Services,
		"rules":                 report.Rules,
	}
	if strings.EqualFold(detail, "full") {
		response["sections"] = report.Sections
		response["variables"] = report.Variables
		response["io"] = report.IO
	}
	if output != "" {
		if !strings.EqualFold(filepath.Ext(output), ".md") {
			return nil, fmt.Errorf("output_path must be a .md file")
		}
		if err := os.WriteFile(output, []byte(sysmac.RenderCompareMarkdown(report, title)), 0644); err != nil {
			return nil, err
		}
		response["markdown_path"] = output
	}
	return toolSuccess(response), nil
}

func nonIdentical(findings []sysmac.CompareFinding) []sysmac.CompareFinding {
	result := []sysmac.CompareFinding{}
	for _, finding := range findings {
		if finding.Class != sysmac.CompareIdentical {
			result = append(result, finding)
		}
	}
	return result
}
