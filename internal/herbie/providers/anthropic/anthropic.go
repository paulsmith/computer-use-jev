// Package anthropic implements the Anthropic Messages protocol.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/paulsmith/computeruser/internal/herbie/config"
	"github.com/paulsmith/computeruser/internal/herbie/provider"
	"github.com/paulsmith/computeruser/internal/herbie/providerconfig"
	providerinternal "github.com/paulsmith/computeruser/internal/herbie/providers/internal"
	"github.com/paulsmith/computeruser/internal/herbie/trace"
	"github.com/paulsmith/computeruser/internal/herbie/transport"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Name, DisplayName, BaseURL, APIKey, Version string
	CatalogID                                   string
	KeepModelOrder                              bool
	AllowEmptySignature                         bool
	DefaultThinking                             string
	ThinkingBudget, MaxTokens                   int64
	ThinkingMode, CacheTTL                      string
	ExtraHeaders                                http.Header
	ExtraBody                                   map[string]any
	ShowReasoning, CacheEnabled                 bool
	FastMode                                    bool
	webSearch                                   bool
	Retry                                       transport.RetryPolicy
	client                                      transport.Client
	models                                      *modelMetadata
}
type modelMetadata struct {
	mu     sync.RWMutex
	models map[string]provider.ModelInfo
}

type Provider struct {
	options       Options
	name          string
	displayName   string
	defaultEffort string
	metadata      provider.Metadata
}

func (p *Provider) Name() string          { return p.name }
func (p *Provider) DisplayName() string   { return p.displayName }
func (p *Provider) DefaultModel() string  { return "" }
func (p *Provider) DefaultEffort() string { return p.defaultEffort }
func (p *Provider) Metadata() *provider.Metadata {
	return &p.metadata
}
func (p *Provider) SetTrace(l provider.TraceSink) { p.options.client.Trace = l }
func (p *Provider) ListEfforts() []string {
	if mode := strings.ToLower(p.options.ThinkingMode); mode == "budget" || mode == "off" {
		return nil
	}
	return []string{"low", "medium", "high", "xhigh", "max"}
}
func (p *Provider) ServerTools(model string) []provider.ServerToolDef {
	if !p.options.webSearch || !strings.HasPrefix(strings.ToLower(model), "claude") {
		return nil
	}
	return []provider.ServerToolDef{{Name: "web_search", Type: "web_search_20250305"}}
}
func (p *Provider) Stream(ctx context.Context, c provider.Context, model string, cb provider.StreamCallback, tick provider.TickFunc) error {
	if c.Fast && !p.options.FastMode {
		return fmt.Errorf("%s does not support fast mode", p.name)
	}
	return stream(ctx, &p.options, p.metadata.Model(), c, model, cb, tick)
}
func (p *Provider) ListModels(ctx context.Context, tick provider.TickFunc) ([]provider.ModelInfo, error) {
	return list(ctx, &p.options, provider.Context{}, tick)
}
func (p *Provider) ProbeModel(_ context.Context, model string) (provider.ModelProbe, error) {
	return provider.ModelProbe{URL: p.options.BaseURL + "/models?limit=1000", Headers: p.options.headers(provider.Context{}, false), Timeout: 5 * time.Second, Parse: parseProbe}, nil
}

func New(o Options) *Provider {
	trace.RegisterSecret(o.APIKey)
	if o.Name == "" {
		o.Name = "anthropic"
	}
	if o.BaseURL == "" {
		o.BaseURL = "https://api.anthropic.com/v1"
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	if o.Version == "" {
		o.Version = "2023-06-01"
	}
	if o.Retry.MaxAttempts == 0 {
		o.Retry = transport.DefaultRetryPolicy()
	}
	if o.Name == "anthropic" && o.FastMode {
		o.webSearch = true
	}
	o.models = &modelMetadata{models: map[string]provider.ModelInfo{}}
	p := &Provider{options: o, name: o.Name, displayName: o.DisplayName}
	p.metadata.CatalogID = o.CatalogID
	p.metadata.KeepModelOrder = o.KeepModelOrder
	if p.metadata.CatalogID == "" && o.Name == "anthropic" {
		p.metadata.CatalogID = "anthropic"
	}
	return p
}
func NewOfficial(c *config.Config) *OfficialProvider {
	prefix := "providers.anthropic."
	key := providerconfig.APIKey(c, prefix, "ANTHROPIC_API_KEY")
	return &OfficialProvider{Provider: New(Options{
		Name: "anthropic", DisplayName: c.AnyString(prefix + "display_name"), KeepModelOrder: !c.AnyBool(prefix+"sort_models", true),
		BaseURL: "https://api.anthropic.com/v1", APIKey: key, Version: c.AnyString(prefix + "version"),
		DefaultThinking: "adaptive", ThinkingMode: c.AnyString(prefix + "thinking_mode"),
		ThinkingBudget: int64(c.AnyInt(prefix+"thinking_budget", 0)), MaxTokens: int64(c.AnyInt(prefix+"max_tokens", 0)),
		CacheTTL: providerconfig.CacheTTL(c, prefix), CacheEnabled: c.AnyBool(prefix+"cache", true),
		ShowReasoning: c.Bool("show_reasoning", false), Retry: providerconfig.Retry(c), webSearch: true, FastMode: true,
		ExtraHeaders: providerconfig.ExtraHeaders(c, prefix), ExtraBody: providerconfig.ExtraBody(c, prefix),
	})}
}
func NewCompatible(c *config.Config) *Provider {
	prefix := "providers.anthropic-compatible."
	base := c.AnyString(prefix + "base_url")
	if base == "" {
		return nil
	}
	return New(Options{
		Name: "anthropic-compatible", DisplayName: c.AnyString(prefix + "display_name"), BaseURL: base,
		KeepModelOrder: !c.AnyBool(prefix+"sort_models", true),
		APIKey:         providerconfig.APIKey(c, prefix, ""), Version: c.AnyString(prefix + "version"),
		AllowEmptySignature: true, DefaultThinking: "budget", ThinkingMode: c.AnyString(prefix + "thinking_mode"),
		ThinkingBudget: int64(c.AnyInt(prefix+"thinking_budget", 0)), MaxTokens: int64(c.AnyInt(prefix+"max_tokens", 0)),
		CacheTTL: providerconfig.CacheTTL(c, prefix), CacheEnabled: c.AnyBool(prefix+"cache", false), Retry: providerconfig.Retry(c),
		ExtraHeaders: providerconfig.ExtraHeaders(c, prefix), ExtraBody: providerconfig.ExtraBody(c, prefix),
	})
}
func (o *Options) headers(c provider.Context, fast bool) http.Header {
	h := o.ExtraHeaders.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Anthropic-Version", o.Version)
	h.Set("Accept", "text/event-stream")
	if o.FastMode && fast {
		h.Set("Anthropic-Beta", "fast-mode-2026-02-01")
	}
	if o.APIKey != "" {
		h.Set("X-Api-Key", o.APIKey)
	}
	return providerconfig.ExpandSessionHeaders(h, providerinternal.Or(c.CacheKey, providerconfig.ProcessSessionID()))
}

const maxPauseContinuations = 16

func stream(ctx context.Context, o *Options, meta *provider.ModelInfo, c provider.Context, model string, cb provider.StreamCallback, tick provider.TickFunc) error {
	if model == "" {
		return fmt.Errorf("model is required")
	}
	base := bodyWithMeta(o, meta, c, model)
	request := base
	usage := provider.NewStreamUsage()
	var content []any
	expected := provider.PendingServerToolIDs(c.Items, o.Name, model)
	var deferred []provider.StreamEvent
	seenServerToolUses := map[string]struct{}{}
	hasClientToolCall := false
	serverBlock := func(event provider.StreamEvent) (map[string]any, string, string, error) {
		var native map[string]any
		if err := json.Unmarshal([]byte(event.JSON), &native); err != nil {
			return nil, "", "", err
		}
		id := ""
		if event.ID != nil {
			id = *event.ID
		}
		return native, valstr(native["type"]), id, nil
	}
	emitResult := func(event provider.StreamEvent, native map[string]any, id string) error {
		if _, ok := expected[id]; !ok {
			return fmt.Errorf("anthropic returned orphaned %s", valstr(native["type"]))
		}
		delete(expected, id)
		raw, err := json.Marshal([]any{native})
		if err != nil {
			return err
		}
		event.JSON = string(raw)
		return cb(event)
	}
	drain := func() error {
		for len(deferred) > 0 {
			first := deferred[0]
			if first.Kind != provider.EventServerTool || first.JSON == "" {
				deferred = deferred[1:]
				if err := cb(first); err != nil {
					return err
				}
				continue
			}
			native, typ, id, err := serverBlock(first)
			if err != nil {
				return err
			}
			if typ != "server_tool_use" {
				deferred = deferred[1:]
				if strings.HasSuffix(typ, "_tool_result") {
					if err := emitResult(first, native, id); err != nil {
						return err
					}
				} else if err := cb(first); err != nil {
					return err
				}
				continue
			}
			result := -1
			var resultNative map[string]any
			for i := 1; i < len(deferred); i++ {
				event := deferred[i]
				if event.Kind != provider.EventServerTool || event.JSON == "" {
					continue
				}
				candidate, candidateType, candidateID, err := serverBlock(event)
				if err != nil {
					return err
				}
				if strings.HasSuffix(candidateType, "_tool_result") && candidateID == id {
					result, resultNative = i, candidate
					break
				}
			}
			if result < 0 {
				return nil
			}
			raw, err := json.Marshal([]any{native, resultNative})
			if err != nil {
				return err
			}
			first.JSON = string(raw)
			first.Citations = deferred[result].Citations
			if err := cb(first); err != nil {
				return err
			}
			deferred = append(slices.Clone(deferred[1:result]), deferred[result+1:]...)
		}
		return nil
	}
	pair := func(event provider.StreamEvent) error {
		if event.Kind == provider.EventToolCallEnd {
			hasClientToolCall = true
		}
		var native map[string]any
		var typ, id string
		if event.Kind == provider.EventServerTool && event.JSON != "" {
			var err error
			native, typ, id, err = serverBlock(event)
			if err != nil {
				return err
			}
			if typ == "server_tool_use" {
				if id == "" {
					return fmt.Errorf("anthropic returned server tool use without an id")
				}
				if _, duplicate := seenServerToolUses[id]; duplicate {
					return fmt.Errorf("anthropic returned duplicate server tool use id %q", id)
				}
				if _, pending := expected[id]; pending {
					return fmt.Errorf("anthropic repeated pending server tool use id %q", id)
				}
				seenServerToolUses[id] = struct{}{}
			}
		}
		if len(deferred) != 0 {
			deferred = append(deferred, event)
			return drain()
		}
		if event.Kind != provider.EventServerTool || event.JSON == "" {
			return cb(event)
		}
		if typ == "server_tool_use" {
			deferred = append(deferred, event)
			return nil
		}
		if strings.HasSuffix(typ, "_tool_result") {
			return emitResult(event, native, id)
		}
		return cb(event)
	}
	flushDeferred := func() error {
		for _, event := range deferred {
			if event.Kind == provider.EventServerTool && event.JSON != "" {
				native, typ, id, err := serverBlock(event)
				if err != nil {
					return err
				}
				if strings.HasSuffix(typ, "_tool_result") {
					if err := emitResult(event, native, id); err != nil {
						return err
					}
					continue
				}
				if typ == "server_tool_use" {
					raw, err := json.Marshal([]any{native})
					if err != nil {
						return err
					}
					event.JSON = string(raw)
				}
			}
			if err := cb(event); err != nil {
				return err
			}
		}
		deferred = deferred[:0]
		return nil
	}
	continuations := 0
	for {
		expectedAttempt := maps.Clone(expected)
		deferredAttempt := slices.Clone(deferred)
		seenServerToolUsesAttempt := maps.Clone(seenServerToolUses)
		hasClientToolCallAttempt := hasClientToolCall
		restorePairing := func() {
			expected = maps.Clone(expectedAttempt)
			deferred = slices.Clone(deferredAttempt)
			seenServerToolUses = maps.Clone(seenServerToolUsesAttempt)
			hasClientToolCall = hasClientToolCallAttempt
		}
		data, err := json.Marshal(request)
		if err != nil {
			return provider.WithStreamUsage(err, usage)
		}
		e, terminal, retryUsageTransferred, err := streamRequest(ctx, o, c, c.Fast, data, pair, cb, restorePairing, tick)
		if e != nil && (err == nil || !retryUsageTransferred) {
			usage.Add(e.usage)
		}
		if err != nil {
			return provider.WithStreamUsage(err, usage)
		}
		if terminal.Kind == provider.EventError {
			terminal.Usage = &usage
			if err := cb(terminal); err != nil {
				return provider.WithStreamUsage(err, usage)
			}
			return nil
		}
		content = append(content, e.content()...)
		if terminal.StopReason != "pause_turn" {
			if len(deferred) != 0 {
				if terminal.StopReason != "tool_use" || !hasClientToolCall {
					return provider.WithStreamUsage(fmt.Errorf("anthropic completed with an unmatched server tool use"), usage)
				}
				if err := flushDeferred(); err != nil {
					return provider.WithStreamUsage(err, usage)
				}
			}
			if len(expected) != 0 {
				return provider.WithStreamUsage(fmt.Errorf("anthropic completed without resolving a pending server tool use"), usage)
			}
			terminal.Usage = &usage
			if err := cb(terminal); err != nil {
				return provider.WithStreamUsage(err, usage)
			}
			return nil
		}
		if continuations == maxPauseContinuations {
			return provider.WithStreamUsage(fmt.Errorf("anthropic pause_turn continuation limit exceeded (%d)", maxPauseContinuations), usage)
		}
		continuations++
		request = continuationBody(base, content)
	}
}

func continuationBody(base map[string]any, content []any) map[string]any {
	out := make(map[string]any, len(base))
	maps.Copy(out, base)
	messages, _ := base["messages"].([]any)
	messages = slices.Clone(messages)
	messages = append(messages, map[string]any{"role": "assistant", "content": slices.Clone(content)})
	out["messages"] = messages
	return out
}

func streamRequest(ctx context.Context, o *Options, c provider.Context, fast bool, body []byte, eventsCB, cb provider.StreamCallback, restorePairing func(), tick provider.TickFunc) (*events, provider.StreamEvent, bool, error) {
	var current *events
	var terminal provider.StreamEvent
	retryUsageTransferred := false
	result, err := transport.RunStreamRetry(ctx, transport.StreamRetryOptions{
		Client:  o.client,
		URL:     o.BaseURL + "/messages",
		Body:    body,
		Headers: func() http.Header { return o.headers(c, fast) },
		Retry:   o.Retry,
		NewParser: func(emit provider.StreamCallback) transport.StreamParser {
			retryUsageTransferred = false
			terminal = provider.StreamEvent{}
			e := newEvents(func(event provider.StreamEvent) error {
				if event.Kind == provider.EventDone || event.Kind == provider.EventError {
					terminal = event
					return nil
				}
				return emit(event)
			})
			current = e
			return e
		},
	}, func(event provider.StreamEvent) error {
		if event.Kind == provider.EventRetry {
			retryUsageTransferred = true
			if restorePairing != nil {
				restorePairing()
			}
			return cb(event)
		}
		return eventsCB(event)
	}, tick)
	if err != nil {
		if current == nil {
			return nil, provider.StreamEvent{}, retryUsageTransferred, err
		}
		return current, provider.StreamEvent{}, retryUsageTransferred, provider.WithStreamResponse(err, current.response())
	}
	if current == nil {
		return nil, provider.StreamEvent{}, retryUsageTransferred, nil
	}
	if result.Response.Status < 200 || result.Response.Status >= 300 {
		return current, provider.StreamEvent{Kind: provider.EventError, Message: transport.APIError(result.Response.Status, result.Response.Body), HTTPStatus: result.Response.Status}, retryUsageTransferred, nil
	}
	return current, terminal, retryUsageTransferred, nil
}
func cacheControl(ttl string) map[string]any {
	x := map[string]any{"type": "ephemeral"}
	if strings.EqualFold(ttl, "1h") {
		x["ttl"] = "1h"
	}
	return x
}
func maxTokens(o *Options, meta *provider.ModelInfo, model string) int64 {
	configured := o.MaxTokens
	limit := int64(0)
	if meta != nil {
		limit = meta.MaxOutput
	} else {
		o.models.mu.RLock()
		limit = o.models.models[model].MaxOutput
		o.models.mu.RUnlock()
	}
	if configured > 0 {
		if limit > 0 && configured > limit {
			return limit
		}
		return configured
	}
	if limit > 0 {
		return limit
	}
	return 32000
}
func bodyWithMeta(o *Options, meta *provider.ModelInfo, c provider.Context, model string) map[string]any {
	max := maxTokens(o, meta, model)
	msgs := messages(c, o.Name, model, o.AllowEmptySignature)
	x := map[string]any{"model": model, "max_tokens": max, "stream": true, "messages": msgs}
	if o.FastMode && c.Fast {
		x["speed"] = "fast"
	}
	cache := o.CacheEnabled
	if c.SystemPrompt != "" {
		system := map[string]any{"type": "text", "text": c.SystemPrompt}
		if cache {
			system["cache_control"] = cacheControl(o.CacheTTL)
		}
		x["system"] = []any{system}
	}
	if len(c.Tools)+len(c.ServerTools) > 0 {
		a := make([]any, 0, len(c.Tools)+len(c.ServerTools))
		for i, d := range c.Tools {
			q := map[string]any{"name": d.Name, "description": d.Description, "input_schema": providerinternal.Schema(d, true)}
			if cache && i == len(c.Tools)-1 {
				q["cache_control"] = cacheControl(o.CacheTTL)
			}
			a = append(a, q)
		}
		for _, d := range c.ServerTools {
			a = append(a, map[string]any{"name": d.Name, "type": d.Type})
		}
		x["tools"] = a
	}
	if cache && len(msgs) > 0 {
		if m, ok := msgs[len(msgs)-1].(map[string]any); ok {
			if a, ok := m["content"].([]any); ok && len(a) > 0 {
				if q, ok := a[len(a)-1].(map[string]any); ok && q["type"] != "thinking" && q["type"] != "redacted_thinking" {
					q["cache_control"] = cacheControl(o.CacheTTL)
				}
			}
		}
	}
	mode := strings.ToLower(o.ThinkingMode)
	if mode == "" {
		if c.Effort != "" {
			mode = "adaptive"
		} else {
			mode = o.DefaultThinking
		}
	}
	if mode == "adaptive" {
		display := "omitted"
		if o.ShowReasoning {
			display = "summarized"
		}
		x["thinking"] = map[string]any{"type": "adaptive", "display": display}
		if c.Effort != "" {
			x["output_config"] = map[string]any{"effort": c.Effort}
		}
	} else if mode == "budget" && max > 1 {
		b := o.ThinkingBudget
		if b <= 0 || b >= max {
			b = max - 1
		}
		x["thinking"] = map[string]any{"type": "enabled", "budget_tokens": b}
	}
	providerconfig.ApplyExtraBody(x, o.ExtraBody)
	return x
}
func messages(c provider.Context, name, model string, allow bool) []any {
	var out []any
	for i := 0; i < len(c.Items); {
		z := c.Items[i]
		switch z.Kind {
		case provider.ItemUserMessage:
			out = append(out, map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": z.Text}}})
			i++
		case provider.ItemToolResult:
			var a []any
			for i < len(c.Items) && c.Items[i].Kind == provider.ItemToolResult {
				q := c.Items[i]
				v := any(q.Output)
				if len(q.Images) > 0 {
					var parts []any
					if q.Output != "" {
						parts = append(parts, map[string]any{"type": "text", "text": q.Output})
					}
					for _, im := range q.Images {
						if c.ImageInput != 0 {
							parts = append(parts, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": providerinternal.Or(im.MIME, "image/png"), "data": im.DataB64}})
						} else {
							parts = append(parts, map[string]any{"type": "text", "text": provider.ImagePlaceholder(im)})
						}
					}
					v = parts
				}
				a = append(a, map[string]any{"type": "tool_result", "tool_use_id": q.CallID, "content": v})
				i++
			}
			out = append(out, map[string]any{"role": "user", "content": a})
		case provider.ItemAssistantMessage, provider.ItemToolCall, provider.ItemReasoning, provider.ItemServerTool:
			var a []any
			for i < len(c.Items) {
				q := c.Items[i]
				if q.Kind != provider.ItemAssistantMessage && q.Kind != provider.ItemToolCall && q.Kind != provider.ItemReasoning && q.Kind != provider.ItemServerTool {
					break
				}
				if q.Kind == provider.ItemAssistantMessage {
					if provider.ProviderIDsEqual(q.Provider, name) && q.Model == model && q.TextJSON != "" {
						var block map[string]any
						if json.Unmarshal([]byte(q.TextJSON), &block) == nil && block["type"] == "text" {
							a = append(a, block)
						}
					} else if q.Text != "" {
						a = append(a, map[string]any{"type": "text", "text": q.Text})
					}
				}
				if q.Kind == provider.ItemToolCall {
					var in any = map[string]any{}
					_ = json.Unmarshal([]byte(q.ToolArgumentsJSON), &in)
					if _, ok := in.(map[string]any); !ok {
						in = map[string]any{}
					}
					a = append(a, map[string]any{"type": "tool_use", "id": q.CallID, "name": q.ToolName, "input": in})
				}
				if q.Kind == provider.ItemReasoning && provider.ProviderIDsEqual(q.Provider, name) && q.Model == model && q.ReasoningJSON != "" {
					var v map[string]any
					if json.Unmarshal([]byte(q.ReasoningJSON), &v) == nil {
						if v["type"] == "thinking" && !allow && valstr(v["signature"]) == "" {
							if s := valstr(v["thinking"]); s != "" {
								a = append(a, map[string]any{"type": "text", "text": s})
							}
						} else {
							a = append(a, v)
						}
					}
				}
				if q.Kind == provider.ItemServerTool && provider.ProviderIDsEqual(q.Provider, name) && q.Model == model && q.ServerToolJSON != "" {
					var blocks []any
					if json.Unmarshal([]byte(q.ServerToolJSON), &blocks) == nil {
						a = append(a, blocks...)
					}
				}
				i++
			}
			if len(a) > 0 {
				out = append(out, map[string]any{"role": "assistant", "content": a})
			}
		default:
			i++
		}
	}
	return out
}
func valstr(x any) string { s, _ := x.(string); return s }
func parseModel(b []byte, out *provider.ModelInfo) {
	var x map[string]any
	if json.Unmarshal(b, &x) != nil {
		return
	}
	if v, ok := x["max_input_tokens"].(float64); ok && v > 0 {
		out.Context = int64(v)
	}
	if v, ok := x["max_tokens"].(float64); ok && v > 0 {
		out.MaxOutput = int64(v)
	}
	caps := obj(x, "capabilities")
	if image := obj(caps, "image_input"); image != nil {
		if v, ok := image["supported"].(bool); ok {
			if v {
				out.ImageInput = provider.ProviderCapYes
			} else {
				out.ImageInput = provider.ProviderCapNo
			}
		}
	}
	effort := obj(caps, "effort")
	if effort != nil {
		if v, ok := effort["supported"].(bool); ok && !v {
			out.Efforts.Known = true
		} else {
			for _, v := range []string{"low", "medium", "high", "xhigh", "max"} {
				if q := obj(effort, v); q != nil && q["supported"] == true {
					out.Efforts.Add(v)
				}
			}
			for k, q := range effort {
				if k != "supported" {
					if z, ok := q.(map[string]any); ok && z["supported"] == true {
						out.Efforts.Add(k)
					}
				}
			}
		}
	}
}
func parseProbe(b, m string, out *provider.ModelInfo) {
	var x struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal([]byte(b), &x) != nil {
		return
	}
	for _, q := range x.Data {
		var z struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(q, &z) == nil && z.ID == m {
			parseModel(q, out)
			return
		}
	}
}
func list(ctx context.Context, o *Options, c provider.Context, t provider.TickFunc) ([]provider.ModelInfo, error) {
	var out []provider.ModelInfo
	after := ""
	for range 50 {
		u := o.BaseURL + "/models?limit=1000"
		if after != "" {
			u += "&after_id=" + url.QueryEscape(after)
		}
		r, e := o.client.Get(ctx, u, o.headers(c, false), 0)
		if e != nil {
			return nil, e
		}
		if r.Status < 200 || r.Status >= 300 {
			return nil, errors.New(transport.ModelsError(o.Name, o.BaseURL, o.APIKey != "", r.Status))
		}
		var x struct {
			Data []json.RawMessage `json:"data"`
			More bool              `json:"has_more"`
			Last string            `json:"last_id"`
		}
		if json.Unmarshal(r.Body, &x) != nil {
			return nil, fmt.Errorf("anthropic sent an empty or truncated /models response")
		}
		for _, q := range x.Data {
			var v struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(q, &v) != nil || v.ID == "" {
				continue
			}
			z := provider.NewModelInfo()
			z.ID = v.ID
			parseModel(q, &z)
			o.models.mu.Lock()
			o.models.models[z.ID] = z.Clone()
			o.models.mu.Unlock()
			out = append(out, z)
		}
		if !x.More || x.Last == "" || x.Last == after {
			break
		}
		after = x.Last
	}
	return out, nil
}
