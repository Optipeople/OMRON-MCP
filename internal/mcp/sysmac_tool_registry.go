package mcp

func sysmacToolHandlers() map[string]sysmacToolHandler {
	handlers := make(map[string]sysmacToolHandler, 20)
	for _, group := range []map[string]sysmacToolHandler{
		sysmacBuildToolHandlers(),
		sysmacInspectTextToolHandlers(),
		sysmacSLWDToolHandlers(),
		sysmacEntityToolHandlers(),
		sysmacTransactionToolHandlers(),
		sysmacLibraryToolHandlers(),
		sysmacCloudFBToolHandlers(),
	} {
		for name, handler := range group {
			handlers[name] = handler
		}
	}
	return handlers
}
