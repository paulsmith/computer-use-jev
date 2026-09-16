package openai

import (
	"encoding/json"
	"fmt"
	"github.com/paulsmith/computeruser/internal/herbie/provider"
	providerinternal "github.com/paulsmith/computeruser/internal/herbie/providers/internal"
	"maps"
	"slices"
)

type events struct {
	wire                           wire
	cb                             provider.StreamCallback
	calls                          map[string]string
	sawArgs                        map[string]bool
	names                          map[string]string
	pending                        map[string]string
	started                        map[string]struct{}
	terminal                       bool
	complete                       bool
	finish                         string
	failure                        string
	transient                      bool
	finishReceived                 bool
	usage                          provider.StreamUsage
	progress, cacheWrite1H         bool
	lengthHint                     string
	reasoningItemID                string
	reasoningPartIndex             int
	reasoningPartContent           bool
	reasoningPartSeen              bool
	reasoningDetails               []map[string]any
	responseID, servedModel, route string
}

func newEventsOptions(w wire, cb provider.StreamCallback, progress, cacheWrite1H bool, hints ...string) *events {
	hint := ""
	if len(hints) > 0 {
		hint = hints[0]
	}
	return &events{wire: w, cb: cb, progress: progress, cacheWrite1H: cacheWrite1H, lengthHint: hint, calls: map[string]string{}, sawArgs: map[string]bool{}, names: map[string]string{}, pending: map[string]string{}, started: map[string]struct{}{}, usage: provider.NewStreamUsage()}
}
func (e *events) emit(x provider.StreamEvent) error {
	if e.terminal {
		return nil
	}
	resetReasoning := x.Kind == provider.EventTextDelta || x.Kind == provider.EventToolCallStart || x.Kind == provider.EventReasoningItem || x.Kind == provider.EventDone || x.Kind == provider.EventError
	if x.Kind == provider.EventServerTool {
		resetReasoning = x.ID != nil || x.JSON != "" || len(x.Queries) > 0
	}
	if resetReasoning {
		e.reasoningItemID = ""
		e.reasoningPartSeen = false
	}
	return e.cb(x)
}

func (e *events) reasoningPartBreak(x map[string]any, summary bool) error {
	itemID := str(x, "item_id")
	if itemID == "" {
		return nil
	}
	partValue, exists := x["summary_index"]
	content := false
	if !exists {
		partValue = x["content_index"]
		content = true
	}
	part, ok := partValue.(float64)
	if !ok {
		return nil
	}
	partIndex := int(part)
	changed := e.reasoningPartSeen && (e.reasoningItemID != itemID || e.reasoningPartIndex != partIndex || e.reasoningPartContent != content)
	e.reasoningItemID, e.reasoningPartIndex, e.reasoningPartContent, e.reasoningPartSeen = itemID, partIndex, content, true
	if !changed {
		return nil
	}
	return e.emit(provider.StreamEvent{Kind: provider.EventReasoningDelta, Text: "  \n", ReasoningPartBreak: true, ReasoningSummary: summary})
}

func reasoningTextDetail(detail map[string]any) bool {
	return str(detail, "type") == "reasoning.text"
}
func hasReasoningMember(detail map[string]any, name string) bool {
	value, ok := detail[name]
	if !ok || value == nil {
		return false
	}
	text, ok := value.(string)
	return !ok || text != ""
}
func joinReasoningText(block, detail map[string]any) bool {
	if !reasoningTextDetail(block) || !reasoningTextDetail(detail) {
		return false
	}
	if text := str(detail, "text"); text != "" {
		block["text"] = str(block, "text") + text
	}
	for _, name := range []string{"signature", "format"} {
		if hasReasoningMember(detail, name) && !hasReasoningMember(block, name) {
			block[name] = detail[name]
		}
	}
	return true
}
func (e *events) collectReasoningDetails(delta map[string]any) {
	details, ok := delta["reasoning_details"].([]any)
	if !ok {
		return
	}
	for _, value := range details {
		detail, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if n := len(e.reasoningDetails); n > 0 && joinReasoningText(e.reasoningDetails[n-1], detail) {
			continue
		}
		e.reasoningDetails = append(e.reasoningDetails, detail)
	}
}
func (e *events) flushReasoningDetails() error {
	if len(e.reasoningDetails) == 0 {
		e.reasoningDetails = nil
		return nil
	}
	raw, err := json.Marshal(e.reasoningDetails)
	e.reasoningDetails = nil
	if err != nil {
		return err
	}
	return e.emit(provider.StreamEvent{Kind: provider.EventReasoningItem, JSON: string(raw)})
}

func (e *events) captureResponse(x map[string]any) {
	if e.wire == Responses {
		x = obj(x, "response")
	}
	if x == nil {
		return
	}
	if e.responseID == "" {
		e.responseID = str(x, "id")
	}
	if e.servedModel == "" {
		e.servedModel = str(x, "model")
	}
	if e.wire == Chat && e.route == "" {
		e.route = str(x, "provider")
	}
}

func (e *events) response() *provider.StreamResponse {
	if e.responseID == "" && e.servedModel == "" && e.route == "" {
		return nil
	}
	return &provider.StreamResponse{ID: e.responseID, Model: e.servedModel, Route: e.route}
}
func (e *events) setServiceTier(tier string) {
	var fast bool
	switch tier {
	case "fast", "priority":
		fast = true
	case "default":
		fast = false
	default:
		return
	}
	e.usage.Fast = &fast
}
func (e *events) feed(s string) error {
	if s == "" || e.terminal {
		return nil
	}
	if s == "[DONE]" {
		e.complete = true
		if e.finish == "" && !e.transient {
			e.finish = "stop"
		}
		return nil
	}
	var x map[string]any
	if err := json.Unmarshal([]byte(s), &x); err != nil {
		return fmt.Errorf("openai stream event: %w", err)
	}
	e.captureResponse(x)
	e.setServiceTier(str(x, "service_tier"))
	if cost, ok := provider.ReportedCost(x["cost"]); ok {
		e.usage.Cost = cost
	}
	if e.complete {
		return nil
	}
	if e.wire == Responses {
		return e.responses(x)
	}
	return e.chat(x)
}
func (e *events) done(reason string) error {
	if e.terminal {
		return nil
	}
	if err := e.flushReasoningDetails(); err != nil {
		return err
	}
	e.terminal = true
	if reason == "length" || reason == "content_filter" {
		return e.cb(provider.StreamEvent{Kind: provider.EventError, Message: "response incomplete: " + reason + func() string {
			if reason == "length" && e.lengthHint != "" {
				return " — " + e.lengthHint
			}
			return ""
		}(), Usage: &e.usage, Response: e.response()})
	}
	return e.cb(provider.StreamEvent{Kind: provider.EventDone, StopReason: reason, Usage: &e.usage, Response: e.response()})
}
func str(m map[string]any, k string) string         { x, _ := m[k].(string); return x }
func obj(m map[string]any, k string) map[string]any { x, _ := m[k].(map[string]any); return x }
func finishReasonIsError(reason string) bool {
	return reason == "network_error" || reason == "error"
}

func (e *events) handleChatFinish(reason, native string) error {
	if e.finishReceived {
		return nil
	}
	e.finishReceived = true
	for _, idx := range slices.Sorted(maps.Keys(e.calls)) {
		id := e.calls[idx]
		if _, started := e.started[idx]; started {
			if err := e.emit(provider.StreamEvent{Kind: provider.EventToolCallEnd, ID: &id}); err != nil {
				return err
			}
		}
	}
	if finishReasonIsError(native) || finishReasonIsError(reason) {
		if finishReasonIsError(native) {
			e.failure = "upstream error: " + native
		} else {
			e.failure = "upstream error: " + reason
		}
		e.transient = true
		return nil
	}
	e.finish = reason
	return nil
}

func (e *events) chat(x map[string]any) error {
	if problem := obj(x, "error"); problem != nil {
		e.terminal = true
		return e.cb(provider.StreamEvent{Kind: provider.EventError, Message: providerinternal.Or(str(problem, "message"), "provider error"), Usage: &e.usage, Response: e.response()})
	}
	if u := obj(x, "usage"); u != nil {
		num := func(k string) (int64, bool) { v, ok := u[k].(float64); return int64(v), ok }
		if v, ok := num("prompt_tokens"); ok {
			e.usage.InputTokens = v
		}
		if v, ok := num("completion_tokens"); ok {
			e.usage.OutputTokens = v
		}
		if d := obj(u, "prompt_tokens_details"); d != nil {
			if _, ok := d["cached_tokens"]; ok {
				e.usage.CachedTokens = int64num(d, "cached_tokens")
			}
			if _, ok := d["cache_write_tokens"]; ok {
				e.usage.CacheWriteTokens = int64num(d, "cache_write_tokens")
				if e.cacheWrite1H {
					e.usage.CacheWrite1HTokens = e.usage.CacheWriteTokens
				}
			}
		}
	}
	if e.wire == Chat {
		if v, ok := obj(x, "usage")["cost"].(float64); ok && v >= 0 {
			e.usage.Cost = v
		}
		if p := obj(x, "prompt_progress"); p != nil && e.progress {
			if er := e.emit(provider.StreamEvent{Kind: provider.EventProgress, Processed: int64num(p, "processed"), Total: int64num(p, "total"), Cache: int64num(p, "cache")}); er != nil {
				return er
			}
		}
	}
	choices, _ := x["choices"].([]any)
	for _, z := range choices {
		q, _ := z.(map[string]any)
		d := obj(q, "delta")
		e.collectReasoningDetails(d)
		r := str(d, "reasoning")
		if r == "" {
			r = str(d, "reasoning_content")
		}
		if r != "" {
			if er := e.emit(provider.StreamEvent{Kind: provider.EventReasoningDelta, Text: r}); er != nil {
				return er
			}
		}
		content := str(d, "content")
		calls, hasCalls := d["tool_calls"].([]any)
		if content != "" || hasCalls {
			if er := e.flushReasoningDetails(); er != nil {
				return er
			}
		}
		if content != "" {
			if er := e.emit(provider.StreamEvent{Kind: provider.EventTextDelta, Text: content}); er != nil {
				return er
			}
		}
		for _, v := range calls {
			call, _ := v.(map[string]any)
			idx := fmt.Sprintf("%v", call["index"])
			if idx == "<nil>" {
				idx = "0"
			}
			id := str(call, "id")
			if id == "" {
				id = e.calls[idx]
			}
			if id == "" {
				id = "call_" + idx
			}
			e.calls[idx] = id
			f := obj(call, "function")
			name := str(f, "name")
			if name != "" {
				e.names[idx] = name
			}
			name = e.names[idx]
			a := str(f, "arguments")
			_, started := e.started[idx]
			if a != "" && !started && name == "" {
				e.pending[idx] += a
			}
			if name != "" && !started {
				e.started[idx] = struct{}{}
				if er := e.emit(provider.StreamEvent{Kind: provider.EventToolCallStart, ID: &id, Name: name}); er != nil {
					return er
				}
				if pending := e.pending[idx]; pending != "" {
					e.pending[idx] = ""
					if er := e.emit(provider.StreamEvent{Kind: provider.EventToolCallDelta, ID: &id, ArgsDelta: pending}); er != nil {
						return er
					}
				}
				if a != "" {
					if er := e.emit(provider.StreamEvent{Kind: provider.EventToolCallDelta, ID: &id, ArgsDelta: a}); er != nil {
						return er
					}
				}
			} else if a != "" && started {
				if er := e.emit(provider.StreamEvent{Kind: provider.EventToolCallDelta, ID: &id, ArgsDelta: a}); er != nil {
					return er
				}
			}
		}
		if reason := str(q, "finish_reason"); reason != "" || finishReasonIsError(str(q, "native_finish_reason")) {
			if err := e.handleChatFinish(reason, str(q, "native_finish_reason")); err != nil {
				return err
			}
		}
	}
	return nil
}
func int64num(m map[string]any, k string) int64 { v, _ := m[k].(float64); return int64(v) }
func citation(value map[string]any) (provider.Citation, bool) {
	if str(value, "type") != "url_citation" {
		return provider.Citation{}, false
	}
	url := str(value, "url")
	if url == "" {
		return provider.Citation{}, false
	}
	raw, _ := json.Marshal(value)
	return provider.Citation{Title: str(value, "title"), URL: url, JSON: string(raw)}, true
}
func citations(value any) []provider.Citation {
	var out []provider.Citation
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if citation, ok := citation(value); ok {
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
func (e *events) responses(x map[string]any) error {
	typ := str(x, "type")
	switch typ {
	case "response.output_text.delta", "response.refusal.delta":
		if d := str(x, "delta"); d != "" {
			return e.emit(provider.StreamEvent{Kind: provider.EventTextDelta, Text: d})
		}
		return nil
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if d := str(x, "delta"); d != "" {
			summary := typ == "response.reasoning_summary_text.delta"
			if err := e.reasoningPartBreak(x, summary); err != nil {
				return err
			}
			return e.emit(provider.StreamEvent{Kind: provider.EventReasoningDelta, Text: d, ReasoningSummary: summary})
		}
		return nil
	case "response.output_text.annotation.added":
		if citation, ok := citation(obj(x, "annotation")); ok {
			return e.emit(provider.StreamEvent{Kind: provider.EventServerTool, Name: "web_search", Citations: []provider.Citation{citation}})
		}
		return nil
	case "response.output_item.added":
		it := obj(x, "item")
		if str(it, "type") != "function_call" {
			return nil
		}
		item, id, name := str(it, "id"), str(it, "call_id"), str(it, "name")
		if item == "" || id == "" || name == "" {
			return nil
		}
		e.calls[item] = id
		return e.emit(provider.StreamEvent{Kind: provider.EventToolCallStart, ID: &id, Name: name})
	case "response.function_call_arguments.delta":
		item := str(x, "item_id")
		id, ok := e.calls[item]
		if !ok {
			return nil
		}
		delta := str(x, "delta")
		if delta != "" {
			e.sawArgs[item] = true
		}
		return e.emit(provider.StreamEvent{Kind: provider.EventToolCallDelta, ID: &id, ArgsDelta: delta})
	case "response.output_item.done":
		it := obj(x, "item")
		if str(it, "type") == "function_call" {
			item := str(it, "id")
			id, ok := e.calls[item]
			if !ok {
				return nil
			}
			if !e.sawArgs[item] {
				if args := str(it, "arguments"); args != "" {
					if err := e.emit(provider.StreamEvent{Kind: provider.EventToolCallDelta, ID: &id, ArgsDelta: args}); err != nil {
						return err
					}
				}
			}
			return e.emit(provider.StreamEvent{Kind: provider.EventToolCallEnd, ID: &id})
		}
		if str(it, "type") == "web_search_call" {
			id := str(it, "id")
			return e.emit(provider.StreamEvent{Kind: provider.EventServerTool, ID: &id, Name: "web_search", Queries: provider.ExtractQueries(obj(it, "action")), Citations: citations(it)})
		}
		if str(it, "type") == "reasoning" && it["encrypted_content"] != nil {
			summary := it["summary"]
			if summary == nil {
				summary = []any{}
			}
			q := map[string]any{"type": "reasoning", "summary": summary, "encrypted_content": it["encrypted_content"]}
			b, _ := json.Marshal(q)
			return e.emit(provider.StreamEvent{Kind: provider.EventReasoningItem, JSON: string(b)})
		}
		if found := citations(it); len(found) > 0 {
			return e.emit(provider.StreamEvent{Kind: provider.EventServerTool, Name: "web_search", Citations: found})
		}
	case "response.completed", "response.done":
		if r := obj(x, "response"); r != nil {
			e.setServiceTier(str(r, "service_tier"))
			if u := obj(r, "usage"); u != nil {
				if _, ok := u["input_tokens"]; ok {
					e.usage.InputTokens = int64num(u, "input_tokens")
				}
				if _, ok := u["output_tokens"]; ok {
					e.usage.OutputTokens = int64num(u, "output_tokens")
				}
				if d := obj(u, "input_tokens_details"); d != nil {
					if _, ok := d["cached_tokens"]; ok {
						e.usage.CachedTokens = int64num(d, "cached_tokens")
					}
				}
			}
		}
		e.complete = true
		e.finish = "completed"
		return nil
	case "response.failed", "response.incomplete", "error":
		msg := str(x, "message")
		response := obj(x, "response")
		if response != nil {
			e.setServiceTier(str(response, "service_tier"))
			if u := obj(response, "usage"); u != nil {
				if _, ok := u["input_tokens"]; ok {
					e.usage.InputTokens = int64num(u, "input_tokens")
				}
				if _, ok := u["output_tokens"]; ok {
					e.usage.OutputTokens = int64num(u, "output_tokens")
				}
				if d := obj(u, "input_tokens_details"); d != nil {
					if _, ok := d["cached_tokens"]; ok {
						e.usage.CachedTokens = int64num(d, "cached_tokens")
					}
				}
			}
			if typ == "response.failed" {
				msg = str(obj(response, "error"), "message")
			}
			if typ == "response.incomplete" {
				msg = "response incomplete: " + providerinternal.Or(str(obj(response, "incomplete_details"), "reason"), "unknown")
			}
		}
		if msg == "" {
			switch typ {
			case "response.incomplete":
				msg = "response incomplete: unknown"
			case "error":
				msg = providerinternal.Or(str(x, "code"), "provider error")
			default:
				msg = typ
			}
		}
		if typ == "error" {
			e.terminal = true
			return e.cb(provider.StreamEvent{Kind: provider.EventError, Message: msg, Usage: &e.usage, Response: e.response()})
		}
		e.complete = true
		e.failure = msg
		return nil
	}
	return nil
}
func (e *events) Feed(s string) error { return e.feed(s) }
func (e *events) Finished() bool {
	return e.terminal || !e.transient && (e.complete || e.finish != "")
}
func (e *events) Finalize() error                    { return e.finalize() }
func (e *events) Usage() *provider.StreamUsage       { return &e.usage }
func (e *events) Response() *provider.StreamResponse { return e.response() }
func (e *events) finalize() error {
	if e.terminal {
		return nil
	}
	if e.failure != "" {
		if err := e.flushReasoningDetails(); err != nil {
			return err
		}
		e.terminal = true
		return e.cb(provider.StreamEvent{Kind: provider.EventError, Message: e.failure, Usage: &e.usage, Response: e.response()})
	}
	if e.complete || e.finish != "" {
		return e.done(providerinternal.Or(e.finish, "stop"))
	}
	e.terminal = true
	return e.cb(provider.StreamEvent{Kind: provider.EventError, Message: "stream ended before completion", Usage: &e.usage, Response: e.response()})
}
