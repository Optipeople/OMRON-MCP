package mcp

// sysmacTools exposes the complete Sysmac tool catalog. Individual tool
// definitions and handlers live in focused files by operation family.
func sysmacTools() []tool {
	tools := make([]tool, 0, 20)
	tools = append(tools, sysmacBuildTools()...)
	tools = append(tools, sysmacInspectTextTools()...)
	tools = append(tools, sysmacSLWDTools()...)
	tools = append(tools, sysmacEntityTools()...)
	tools = append(tools, sysmacTransactionTools()...)
	tools = append(tools, sysmacLibraryTools()...)
	tools = append(tools, sysmacCloudFBTools()...)
	tools = append(tools, sysmacCompareTools()...)
	return tools
}
