package mcp

func sysmacToolHandlers() map[string]sysmacToolHandler {
	handlers := make(map[string]sysmacToolHandler, 18)
	for _, group := range []map[string]sysmacToolHandler{
		sysmacBuildToolHandlers(),
		sysmacInspectTextToolHandlers(),
		sysmacSLWDToolHandlers(),
		sysmacEntityToolHandlers(),
		sysmacTransactionToolHandlers(),
		sysmacLibraryToolHandlers(),
	} {
		for name, handler := range group {
			handlers[name] = handler
		}
	}
	return handlers
}
