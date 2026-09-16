package provider

import (
	"encoding/json"
	"strings"
)

// PendingServerToolIDs reconstructs unresolved native server-tool uses from history.
func PendingServerToolIDs(items []Item, providerName, model string) map[string]struct{} {
	pending := map[string]struct{}{}
	for _, item := range items {
		if item.Kind != ItemServerTool || !ProviderIDsEqual(item.Provider, providerName) || item.Model != model || item.ServerToolJSON == "" {
			continue
		}
		var blocks []map[string]any
		if json.Unmarshal([]byte(item.ServerToolJSON), &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			typ, _ := block["type"].(string)
			switch {
			case typ == "server_tool_use":
				if id, _ := block["id"].(string); id != "" {
					pending[id] = struct{}{}
				}
			case strings.HasSuffix(typ, "_tool_result"):
				if id, _ := block["tool_use_id"].(string); id != "" {
					delete(pending, id)
				}
			}
		}
	}
	return pending
}
