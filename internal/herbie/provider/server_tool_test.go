package provider

import "testing"

func TestPendingServerToolIDs(t *testing.T) {
	items := []Item{
		{Kind: ItemServerTool, Provider: "anthropic", Model: "claude", ServerToolJSON: `[{"type":"server_tool_use","id":"srv1"},{"type":"server_tool_use","id":"srv2"},{"type":"web_search_tool_result","tool_use_id":"srv1"}]`},
		{Kind: ItemServerTool, Provider: "anthropic", Model: "claude", ServerToolJSON: `[{"type":"web_search_tool_result","tool_use_id":"srv2"}]`},
		{Kind: ItemServerTool, Provider: "other", Model: "claude", ServerToolJSON: `[{"type":"server_tool_use","id":"foreign"}]`},
		{Kind: ItemServerTool, Provider: "anthropic", Model: "claude", ServerToolJSON: "not json"},
		{Kind: ItemServerTool, Provider: "llama.cpp", Model: "llama", ServerToolJSON: `[{"type":"server_tool_use","id":"alias"}]`},
	}
	if got := PendingServerToolIDs(items, "anthropic", "claude"); len(got) != 0 {
		t.Fatalf("pending IDs = %#v", got)
	}

	items[1].ServerToolJSON = `[{"type":"server_tool_use","id":"srv2"}]`
	got := PendingServerToolIDs(items, "anthropic", "claude")
	if len(got) != 1 {
		t.Fatalf("pending IDs = %#v", got)
	}
	if _, ok := got["srv2"]; !ok {
		t.Fatalf("pending IDs = %#v", got)
	}
	alias := PendingServerToolIDs(items, "llamacpp", "llama")
	if len(alias) != 1 {
		t.Fatalf("alias pending IDs = %#v", alias)
	}
	if _, ok := alias["alias"]; !ok {
		t.Fatalf("alias pending IDs = %#v", alias)
	}
}
