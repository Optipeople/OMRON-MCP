package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/rjboer/omron-mcp/internal/sysmac"
)

func sysmacSLWDTools() []tool {
	return []tool{
		{Name: "sysmac_slwd_read", Description: "Read an SLWD variable source.", InputSchema: objectSchema(map[string]any{"path": stringProperty("SLWD variable source path")}, "path")},
		{Name: "sysmac_slwd_write", Description: "Update an SLWD variable source (TAB separated native format) while preserving every untouched line byte for byte. Each record names a variable; existing variables only get the fields present in the record (an empty string removes initialValue, comment or an attribute), missing variables are added to the section of their group. Requires explicit confirmation.", InputSchema: objectSchema(map[string]any{"path": stringProperty("SLWD variable source path"), "variables": map[string]any{"type": "array", "description": "Desired variable records", "items": map[string]any{"type": "object", "properties": map[string]any{"name": stringProperty("Variable name (required)"), "type": stringProperty("Data type, e.g. ARRAY[0..24] OF sTagData"), "initialValue": stringProperty("Initial value; empty string removes it"), "group": stringProperty("VAR, VAR_INPUT, VAR_OUTPUT, VAR_IN_OUT, VAR_EXTERNAL, VAR_GLOBAL or RETURN"), "comment": stringProperty("Comment; empty string removes it"), "rename_to": stringProperty("Optional new name"), "attributes": map[string]any{"type": "object", "description": "Other native fields such as Ord, AT, R, RO, Const, E, EC; empty string removes a field", "additionalProperties": map[string]any{"type": "string"}}}, "required": []string{"name"}}}, "delete_names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "confirm_direct_write": map[string]any{"type": "boolean", "description": "Must be true to mutate project files"}, "protected_root": stringProperty("Optional root that must not be written")}, "path", "variables", "confirm_direct_write")},
	}
}

func sysmacSLWDToolHandlers() map[string]sysmacToolHandler {
	return map[string]sysmacToolHandler{
		"sysmac_slwd_read":  handleSLWDRead,
		"sysmac_slwd_write": handleSLWDWrite,
	}
}

func handleSLWDRead(call sysmacToolCall) (map[string]any, error) {
	path, err := call.string("path", true)
	if err != nil {
		return nil, err
	}
	variables, err := sysmac.ReadSLWDVariables(path)
	if err != nil {
		return nil, err
	}
	return toolSuccess(map[string]any{"status": "read", "path": path, "variables": variables}), nil
}

func handleSLWDWrite(call sysmacToolCall) (map[string]any, error) {
	path, err := call.string("path", true)
	if err != nil {
		return nil, err
	}
	if err := call.confirmed("confirm_direct_write", "confirm_direct_write must be true for direct project mutation"); err != nil {
		return nil, err
	}
	var records []map[string]json.RawMessage
	if rawVariables, ok := call.arguments["variables"]; !ok || json.Unmarshal(rawVariables, &records) != nil {
		return nil, errors.New("variables must be an array of SLWD variable records")
	}
	patches := make([]sysmac.SLWDVariablePatch, 0, len(records))
	for _, record := range records {
		patch, err := slwdPatchFromRecord(record)
		if err != nil {
			return nil, err
		}
		patches = append(patches, patch)
	}
	var deleteNames []string
	if rawDelete, ok := call.arguments["delete_names"]; ok && json.Unmarshal(rawDelete, &deleteNames) != nil {
		return nil, errors.New("delete_names must be an array of strings")
	}
	protectedRoot, err := call.string("protected_root", false)
	if err != nil {
		return nil, err
	}
	before, err := sysmac.SHA256File(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read SLWD source: %w", err)
	}
	document, err := sysmac.ParseSLWDDocument(data)
	if err != nil {
		return nil, err
	}
	for _, patch := range patches {
		if document.HasVariable(patch.Name) {
			if err := document.PatchVariable(patch); err != nil {
				return nil, err
			}
			continue
		}
		if err := document.AddVariablePatch(patch); err != nil {
			return nil, err
		}
	}
	for _, name := range deleteNames {
		if err := document.DeleteVariable(name); err != nil {
			return nil, err
		}
	}
	if err := sysmac.WriteSLWDAtomic(path, document, protectedRoot); err != nil {
		return nil, err
	}
	after, err := sysmac.SHA256File(path)
	if err != nil {
		return nil, err
	}
	return toolSuccess(map[string]any{"status": "applied", "path": path, "changed_files": []string{path, path + ".backup"}, "source_hash_before": before, "source_hash_after": after, "backup_path": path + ".backup"}), nil
}

func slwdPatchFromRecord(record map[string]json.RawMessage) (sysmac.SLWDVariablePatch, error) {
	var patch sysmac.SLWDVariablePatch
	optional := func(key string) (*string, error) {
		raw, ok := record[key]
		if !ok {
			return nil, nil
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("SLWD variable field %q must be a string", key)
		}
		return &value, nil
	}
	name, err := optional("name")
	if err != nil {
		return patch, err
	}
	if name == nil || *name == "" {
		return patch, errors.New("every SLWD variable record needs a name")
	}
	patch.Name = *name
	if patch.Type, err = optional("type"); err != nil {
		return patch, err
	}
	if patch.InitialValue, err = optional("initialValue"); err != nil {
		return patch, err
	}
	if patch.Group, err = optional("group"); err != nil {
		return patch, err
	}
	if patch.Comment, err = optional("comment"); err != nil {
		return patch, err
	}
	if patch.RenameTo, err = optional("rename_to"); err != nil {
		return patch, err
	}
	if raw, ok := record["attributes"]; ok {
		var attributes map[string]*string
		if err := json.Unmarshal(raw, &attributes); err != nil {
			return patch, errors.New("SLWD variable attributes must be an object of strings")
		}
		for key, value := range attributes {
			if value == nil {
				empty := ""
				attributes[key] = &empty
			}
		}
		patch.Attributes = attributes
	}
	for key := range record {
		switch key {
		case "name", "type", "initialValue", "group", "comment", "rename_to", "attributes":
		default:
			return patch, fmt.Errorf("unknown SLWD variable field %q", key)
		}
	}
	return patch, nil
}
