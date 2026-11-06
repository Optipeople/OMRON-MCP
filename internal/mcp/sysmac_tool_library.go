package mcp

import (
	"github.com/rjboer/omron-mcp/internal/sysmac"
)

func sysmacLibraryTools() []tool {
	return []tool{
		{Name: "sysmac_library_replace", Description: "Replace a referenced library in an extracted Sysmac Solution directory with a new .slr (archive or extracted folder), the way Sysmac Studio does it: updates the ReferredLibraries payload, removes the old library subtree and its payload files and grafts the new library subtree (including nested libraries) with new entity ids. Runs in a project transaction; without confirm_commit the changes stay staged and can be inspected with sysmac_transaction_preview and committed or rolled back. Call sites of renamed function blocks are not changed; rebuild in Sysmac Studio afterwards.", InputSchema: objectSchema(map[string]any{
			"project_path":      stringProperty("Extracted Sysmac Solution directory (the <GUID> folder with the .oem)"),
			"library_path":      stringProperty("New library: .slr file or extracted .slr folder"),
			"old_library":       stringProperty("Library to replace: entity name (e.g. Cloud_FB_2_0_13), entity id or LibraryId. Optional when only one library is referenced"),
			"new_name":          stringProperty("Optional library entity name; defaults to the .slr file name without extension"),
			"library_file_path": stringProperty("Optional path recorded as LibraryFilePath; defaults to the absolute .slr path"),
			"confirm_commit":    map[string]any{"type": "boolean", "description": "true commits the transaction to project_path immediately; otherwise it stays staged"},
		}, "project_path", "library_path")},
	}
}

func sysmacLibraryToolHandlers() map[string]sysmacToolHandler {
	return map[string]sysmacToolHandler{"sysmac_library_replace": handleLibraryReplace}
}

func handleLibraryReplace(call sysmacToolCall) (map[string]any, error) {
	projectPath, err := call.string("project_path", true)
	if err != nil {
		return nil, err
	}
	libraryPath, err := call.string("library_path", true)
	if err != nil {
		return nil, err
	}
	oldLibrary, err := call.string("old_library", false)
	if err != nil {
		return nil, err
	}
	newName, err := call.string("new_name", false)
	if err != nil {
		return nil, err
	}
	libraryFilePath, err := call.string("library_file_path", false)
	if err != nil {
		return nil, err
	}
	commit := call.confirmed("confirm_commit", "") == nil
	tx, err := sysmac.StartProjectTransaction(projectPath)
	if err != nil {
		return nil, err
	}
	result, err := sysmac.ReplaceLibrary(sysmac.LibraryReplaceOptions{ProjectRoot: tx.StageRoot, LibraryPath: libraryPath, OldLibrary: oldLibrary, NewName: newName, LibraryFilePath: libraryFilePath})
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	changes, err := tx.Preview()
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	response := map[string]any{"old_library": result.OldLibrary, "new_library": result.NewLibrary, "removed_files": len(result.RemovedFiles), "written_files": len(result.WrittenFiles), "skipped_payloads": result.SkippedFiles, "changes": len(changes), "warnings": result.Warnings}
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
