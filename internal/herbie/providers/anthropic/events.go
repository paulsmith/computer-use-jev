package anthropic

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/paulsmith/computer-use-jev/internal/herbie/provider"
	providerinternal "github.com/paulsmith/computer-use-jev/internal/herbie/providers/internal"
)

type block struct {
	kind, id, name, data, thinking, sig, input string
	raw                                        map[string]any
	started                                    bool
}
type events struct {
	cb                      provider.StreamCallback
	blocks                  map[int]*block
	usage                   provider.StreamUsage
	stop                    string
	serverTool              string
	terminal                bool
	complete                bool
	responseID, servedModel string
}

func newEvents(cb provider.StreamCallback) *events {
	return &events{cb: cb, blocks: map[int]*block{}, usage: provider.NewStreamUsage()}
}
func (e *events) emit(x provider.StreamEvent) error {
	if e.terminal {
		return nil
	}
	return e.cb(x)
}

func (e *events) response() *provider.StreamResponse {
	if e.responseID == "" && e.servedModel == "" {
		return nil
	}
	return &provider.StreamResponse{ID: e.responseID, Model: e.servedModel}
}
func (e *events) feed(s string) error {
	if e.terminal || s == "" {
		return nil
	}
	var x map[string]any
	if err := json.Unmarshal([]byte(s), &x); err != nil {
		return fmt.Errorf("anthropic stream event: %w", err)
	}
	if cost, ok := provider.ReportedCost(x["cost"]); ok {
		e.usage.Cost = cost
	}
	if e.complete {
		return nil
	}
	typ, _ := x["type"].(string)
	switch typ {
	case "content_block_start":
		i := num(x, "index")
		c := obj(x, "content_block")
		if c == nil {
			c = map[string]any{}
		}
		b := &block{kind: str(c, "type"), id: str(c, "id"), name: str(c, "name"), data: str(c, "data"), raw: c}
		e.blocks[i] = b
		if b.kind == "thinking" || b.kind == "redacted_thinking" {
			return e.emit(provider.StreamEvent{Kind: provider.EventReasoningDelta})
		}
		if b.kind == "tool_use" {
			b.started = true
			id := b.id
			return e.emit(provider.StreamEvent{Kind: provider.EventToolCallStart, ID: &id, Name: b.name})
		}
		if b.kind == "server_tool_use" {
			e.serverTool = b.name
		}
	case "content_block_delta":
		b := e.blocks[num(x, "index")]
		d := obj(x, "delta")
		if b == nil {
			return nil
		}
		switch str(d, "type") {
		case "text_delta":
			if q := str(d, "text"); q != "" {
				b.raw["text"] = str(b.raw, "text") + q
				return e.emit(provider.StreamEvent{Kind: provider.EventTextDelta, Text: q})
			}
		case "thinking_delta":
			q := str(d, "thinking")
			b.thinking += q
			b.raw["thinking"] = str(b.raw, "thinking") + q
			return e.emit(provider.StreamEvent{Kind: provider.EventReasoningDelta, Text: q})
		case "signature_delta":
			q := str(d, "signature")
			b.sig += q
			b.raw["signature"] = str(b.raw, "signature") + q
		case "input_json_delta":
			q := str(d, "partial_json")
			b.input += q
			if q != "" && b.kind == "tool_use" {
				id := b.id
				return e.emit(provider.StreamEvent{Kind: provider.EventToolCallDelta, ID: &id, ArgsDelta: q})
			}
		case "citations_delta":
			citation := obj(d, "citation")
			if citation == nil {
				return nil
			}
			list, _ := b.raw["citations"].([]any)
			b.raw["citations"] = append(list, citation)
			if normalized, ok := webCitation(citation); ok {
				return e.emit(provider.StreamEvent{Kind: provider.EventServerTool, Name: providerinternal.Or(e.serverTool, "web_search"), Citations: []provider.Citation{normalized}})
			}
		}
	case "content_block_stop":
		b := e.blocks[num(x, "index")]
		if b == nil {
			return nil
		}
		if b.input != "" {
			var input any
			if json.Unmarshal([]byte(b.input), &input) == nil {
				b.raw["input"] = input
			}
		}
		if b.kind == "text" {
			text := str(b.raw, "text")
			if text == "" {
				return nil
			}
			raw, _ := json.Marshal(b.raw)
			return e.emit(provider.StreamEvent{Kind: provider.EventTextItem, Text: text, JSON: string(raw)})
		}
		if b.kind == "tool_use" && b.started {
			id := b.id
			return e.emit(provider.StreamEvent{Kind: provider.EventToolCallEnd, ID: &id})
		}
		if b.kind == "server_tool_use" {
			id := b.id
			raw, _ := json.Marshal(b.raw)
			return e.emit(provider.StreamEvent{Kind: provider.EventServerTool, ID: &id, Name: b.name, JSON: string(raw), Queries: provider.ExtractQueries(b.raw["input"])})
		}
		if strings.HasSuffix(b.kind, "_tool_result") {
			id := str(b.raw, "tool_use_id")
			raw, _ := json.Marshal(b.raw)
			return e.emit(provider.StreamEvent{Kind: provider.EventServerTool, ID: &id, Name: resultToolName(b.kind), JSON: string(raw), Citations: webCitations(b.raw)})
		}
		if b.kind == "thinking" || b.kind == "redacted_thinking" {
			if b.kind == "thinking" && b.thinking == "" && b.sig == "" {
				return nil
			}
			if b.kind == "redacted_thinking" && b.data == "" {
				return nil
			}
			q, _ := json.Marshal(b.raw)
			return e.emit(provider.StreamEvent{Kind: provider.EventReasoningItem, JSON: string(q)})
		}
	case "message_start":
		message := obj(x, "message")
		if e.responseID == "" {
			e.responseID = str(message, "id")
		}
		if e.servedModel == "" {
			e.servedModel = str(message, "model")
		}
		e.usageOf(obj(message, "usage"))
	case "message_delta":
		e.stop = str(obj(x, "delta"), "stop_reason")
		e.usageOf(obj(x, "usage"))
	case "message_stop":
		e.complete = true
		return nil
	case "error":
		e.terminal = true
		return e.cb(provider.StreamEvent{Kind: provider.EventError, Message: providerinternal.Or(str(obj(x, "error"), "message"), "provider error"), Usage: &e.usage, Response: e.response()})
	}
	return nil
}
func resultToolName(kind string) string {
	return strings.TrimSuffix(kind, "_tool_result")
}
func webCitation(value map[string]any) (provider.Citation, bool) {
	url := str(value, "url")
	if url == "" {
		return provider.Citation{}, false
	}
	raw, _ := json.Marshal(value)
	return provider.Citation{Title: str(value, "title"), URL: url, JSON: string(raw)}, true
}
func webCitations(value any) []provider.Citation {
	var out []provider.Citation
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if citation, ok := webCitation(value); ok {
				out = append(out, citation)
				return
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(value)
	return out
}
func (e *events) content() []any {
	indexes := make([]int, 0, len(e.blocks))
	for index := range e.blocks {
		indexes = append(indexes, index)
	}
	slices.Sort(indexes)
	out := make([]any, 0, len(indexes))
	for _, index := range indexes {
		block := e.blocks[index]
		if block.kind == "text" && str(block.raw, "text") == "" {
			continue
		}
		out = append(out, block.raw)
	}
	return out
}
func (e *events) usageOf(u map[string]any) {
	if u == nil {
		return
	}
	if speed := str(u, "speed"); speed != "" {
		fast := speed == "fast"
		if fast || speed == "standard" {
			e.usage.Fast = &fast
		}
	}
	in, ok := n64(u, "input_tokens")
	read, _ := n64(u, "cache_read_input_tokens")
	write, _ := n64(u, "cache_creation_input_tokens")
	if ok {
		e.usage.InputTokens = in
		if read > 0 {
			e.usage.InputTokens += read
		}
		if write > 0 {
			e.usage.InputTokens += write
		}
	}
	if _, ok := n64(u, "cache_read_input_tokens"); ok {
		e.usage.CachedTokens = read
	}
	if _, ok := n64(u, "cache_creation_input_tokens"); ok {
		e.usage.CacheWriteTokens = write
	}
	if v, ok := n64(obj(u, "cache_creation"), "ephemeral_1h_input_tokens"); ok {
		e.usage.CacheWrite1HTokens = v
	}
	if v, ok := n64(u, "output_tokens"); ok {
		e.usage.OutputTokens = v
	}
}
func (e *events) done() error {
	if e.stop == "max_tokens" {
		e.terminal = true
		return e.cb(provider.StreamEvent{Kind: provider.EventError, Message: "response incomplete: max_tokens — raise anthropic.max_tokens or lower the effort level", Usage: &e.usage, Response: e.response()})
	}
	e.terminal = true
	return e.cb(provider.StreamEvent{Kind: provider.EventDone, StopReason: providerinternal.Or(e.stop, "end_turn"), Usage: &e.usage, Response: e.response()})
}
func (e *events) Feed(s string) error                { return e.feed(s) }
func (e *events) Finished() bool                     { return e.complete || e.terminal }
func (e *events) Finalize() error                    { return e.finalize() }
func (e *events) Usage() *provider.StreamUsage       { return &e.usage }
func (e *events) Response() *provider.StreamResponse { return e.response() }
func (e *events) finalize() error {
	if e.terminal {
		return nil
	}
	if e.complete {
		return e.done()
	}
	e.terminal = true
	return e.cb(provider.StreamEvent{Kind: provider.EventError, Message: "stream ended before completion", Usage: &e.usage, Response: e.response()})
}
func obj(x map[string]any, k string) map[string]any { v, _ := x[k].(map[string]any); return v }
func str(x map[string]any, k string) string         { v, _ := x[k].(string); return v }
func num(x map[string]any, k string) int            { v, _ := x[k].(float64); return int(v) }
func n64(x map[string]any, k string) (int64, bool)  { v, ok := x[k].(float64); return int64(v), ok }
