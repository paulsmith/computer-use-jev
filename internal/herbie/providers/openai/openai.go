// Package openai implements OpenAI Chat Completions and Responses providers.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/paulsmith/computer-use-jev/internal/herbie/config"
	"github.com/paulsmith/computer-use-jev/internal/herbie/provider"
	"github.com/paulsmith/computer-use-jev/internal/herbie/providerconfig"
	"github.com/paulsmith/computer-use-jev/internal/herbie/providers/anthropic"
	providerinternal "github.com/paulsmith/computer-use-jev/internal/herbie/providers/internal"
	"github.com/paulsmith/computer-use-jev/internal/herbie/text"
	"github.com/paulsmith/computer-use-jev/internal/herbie/trace"
	"github.com/paulsmith/computer-use-jev/internal/herbie/transport"
	"net/http"
	"slices"
	"strings"
	"time"
)

type wire int

const (
	Chat wire = iota
	Responses
)

type Options struct {
	Name, DisplayName, BaseURL, APIKey  string
	Wire                                wire
	ReasoningFormat, ReasoningField     string
	ReasoningFieldPinned                bool
	SendCacheKey, RequestCost, Progress bool
	CacheTTL                            string
	lengthHint                          string
	CacheEnabled, CacheAuto             bool
	cacheWrite1H                        bool
	ExtraHeaders                        http.Header
	ExtraBody                           map[string]any
	CodexIdentity, TextVerbosity        bool
	webSearch                           bool
	FastTier                            string
	Retry                               transport.RetryPolicy
	client                              transport.Client
}

type Provider struct {
	options              Options
	name                 string
	displayName          string
	defaultModel         string
	defaultEffort        string
	efforts              []string
	reasoningFieldPinned bool
	metadata             provider.Metadata
}

func (p *Provider) Name() string          { return p.name }
func (p *Provider) DisplayName() string   { return p.displayName }
func (p *Provider) DefaultModel() string  { return p.defaultModel }
func (p *Provider) DefaultEffort() string { return p.defaultEffort }
func (p *Provider) Metadata() *provider.Metadata {
	return &p.metadata
}
func (p *Provider) SetTrace(l provider.TraceSink) { p.options.client.Trace = l }
func (p *Provider) SetCredentials(apiKey string, headers http.Header) {
	p.options.APIKey = apiKey
	p.options.ExtraHeaders = headers.Clone()
	trace.RegisterSecret(apiKey)
}
func (p *Provider) SetHTTPClient(client *http.Client) { p.options.client.HTTP = client }
func (p *Provider) Stream(ctx context.Context, c provider.Context, model string, cb provider.StreamCallback, tick provider.TickFunc) error {
	if c.Fast && p.options.FastTier == "" {
		return fmt.Errorf("%s does not support fast mode", p.name)
	}
	o := p.options
	o.CacheEnabled, o.cacheWrite1H = cachePlan(o, p.metadata.Model())
	o.ReasoningField = p.reasoningField(model)
	return stream(ctx, o, c, model, cb, tick)
}

// StreamWithCredentials streams using an immutable request-local credential snapshot.
func (p *Provider) StreamWithCredentials(ctx context.Context, c provider.Context, model string, cb provider.StreamCallback, tick provider.TickFunc, apiKey string, headers http.Header) error {
	if c.Fast && p.options.FastTier == "" {
		return fmt.Errorf("%s does not support fast mode", p.name)
	}
	o := p.options
	o.APIKey = apiKey
	o.ExtraHeaders = headers.Clone()
	o.CacheEnabled, o.cacheWrite1H = cachePlan(o, p.metadata.Model())
	o.ReasoningField = p.reasoningField(model)
	return stream(ctx, o, c, model, cb, tick)
}

func (p *Provider) ListModels(ctx context.Context, tick provider.TickFunc) ([]provider.ModelInfo, error) {
	return listModels(ctx, p.options, tick)
}
func (p *Provider) ProbeModel(_ context.Context, model string) (provider.ModelProbe, error) {
	headers := authHeaders(p.options)
	return provider.ModelProbe{URL: p.options.BaseURL + "/models", Headers: headers, Timeout: 10 * time.Second, Parse: parseModelProbe}, nil
}
func (p *Provider) ListEfforts() []string { return slices.Clone(p.efforts) }
func (p *Provider) ServerTools(string) []provider.ServerToolDef {
	if !p.options.webSearch || p.options.Wire != Responses {
		return nil
	}
	return []provider.ServerToolDef{{Name: "web_search", Type: "web_search"}}
}

func parseWire(s string, fallback wire) wire {
	switch strings.ToLower(s) {
	case "chat", "openai-completions":
		return Chat
	case "responses", "openai-responses":
		return Responses
	}
	return fallback
}

// ReasoningRoundtrip resolves the reasoning_roundtrip setting to a wire field and whether it pins
// that field for every model. An empty or "auto" value resolves from model metadata at request time.
func ReasoningRoundtrip(c *config.Config, prefix string) (string, bool) {
	field := c.AnyString(prefix + "reasoning_roundtrip")
	if field == "" || strings.EqualFold(field, "auto") {
		return "", false
	}
	switch {
	case strings.EqualFold(field, "off") || field == "0":
		return "", true
	case strings.EqualFold(field, "on"):
		return "reasoning_content", true
	default:
		return field, true
	}
}
func (p *Provider) reasoningField(model string) string {
	if p.reasoningFieldPinned {
		return p.options.ReasoningField
	}
	info := p.metadata.Model()
	if info == nil || info.ID != model || !info.InterleavedReasoning.Declared {
		return p.options.ReasoningField
	}
	return info.InterleavedReasoning.Field
}
func New(o Options) *Provider {
	trace.RegisterSecret(o.APIKey)
	if o.Name == "" {
		o.Name = "openai-compatible"
	}
	if o.BaseURL == "" {
		o.BaseURL = "https://api.openai.com/v1"
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	if o.Retry.MaxAttempts == 0 {
		o.Retry = transport.DefaultRetryPolicy()
	}
	if o.Name == "openai" && o.FastTier == "fast" {
		o.webSearch = true
	}
	if o.Name == "ollama" && o.lengthHint == "" {
		o.lengthHint = recipeLengthHint(o.Name)
	}
	p := &Provider{options: o, name: o.Name, displayName: o.DisplayName, efforts: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, reasoningFieldPinned: o.ReasoningFieldPinned}
	if o.Name == "openai" {
		p.metadata.CatalogID = "openai"
	}
	return p
}
func NewOfficial(c *config.Config) *OfficialProvider {
	prefix := "providers.openai."
	key := providerconfig.APIKey(c, prefix, "OPENAI_API_KEY")
	format := c.AnyString(prefix + "reasoning_format")
	if format == "" {
		format = "nested"
	}
	field, pinned := ReasoningRoundtrip(c, prefix)
	cacheTTL := providerconfig.CacheTTL(c, prefix)
	o := Options{
		Name: "openai", DisplayName: c.AnyString(prefix + "display_name"),
		BaseURL: "https://api.openai.com/v1", APIKey: key,
		Wire: parseWire(c.AnyString(prefix+"api"), Responses), ReasoningFormat: format, ReasoningField: field, ReasoningFieldPinned: pinned,
		SendCacheKey: c.AnyBool(prefix+"send_cache_key", true), RequestCost: c.AnyBool(prefix+"request_cost", false),
		CacheEnabled: c.AnyBool(prefix+"cache", false), CacheTTL: cacheTTL, Retry: providerconfig.Retry(c), webSearch: true, FastTier: "fast",
		ExtraHeaders: providerconfig.ExtraHeaders(c, prefix), ExtraBody: providerconfig.ExtraBody(c, prefix),
	}
	p := New(o)
	p.metadata.KeepModelOrder = !c.AnyBool(prefix+"sort_models", true)
	return &OfficialProvider{Provider: p}
}

// NewConfigured builds an OpenAI-compatible provider from resolved settings.
func NewConfigured(c *config.Config, name string, fallback wire) *Provider {
	prefix := "providers." + name + "."
	base := c.AnyString(prefix + "base_url")
	if base == "" {
		return nil
	}
	field, pinned := ReasoningRoundtrip(c, prefix)
	p := New(Options{
		Name: name, DisplayName: c.AnyString(prefix + "display_name"), BaseURL: base,
		APIKey: providerconfig.APIKey(c, prefix, ""), Wire: parseWire(c.AnyString(prefix+"api"), fallback),
		ReasoningFormat: c.AnyString(prefix + "reasoning_format"), ReasoningField: field, ReasoningFieldPinned: pinned,
		SendCacheKey: c.AnyBool(prefix+"send_cache_key", false), RequestCost: c.AnyBool(prefix+"request_cost", false),
		CacheTTL: c.AnyString(prefix + "cache_ttl"), CacheEnabled: c.AnyBool(prefix+"cache", false), Retry: providerconfig.Retry(c),
		ExtraHeaders: providerconfig.ExtraHeaders(c, prefix), ExtraBody: providerconfig.ExtraBody(c, prefix),
	})
	if id, set := c.AnySet(prefix + "catalog_id"); set {
		p.metadata.CatalogID = id
	} else if name != "ollama" {
		p.metadata.CatalogID = name
	}
	p.metadata.KeepModelOrder = !c.AnyBool(prefix+"sort_models", true)
	return p
}

// NewRecipe builds a config-defined OpenAI-compatible provider.
func NewRecipe(c *config.Config, name string) provider.Provider {
	prefix := "providers." + name + "."
	api := c.AnyString(prefix + "api")
	if strings.EqualFold(api, "anthropic-messages") {
		return newAnthropicRecipe(c, name, prefix)
	}
	if api != "" && !strings.EqualFold(api, "chat") && !strings.EqualFold(api, "responses") && !strings.EqualFold(api, "openai-completions") && !strings.EqualFold(api, "openai-responses") {
		return nil
	}
	base := c.AnyString(prefix + "base_url")
	if base == "" && name == "ollama" {
		base = "http://127.0.0.1:11434/v1"
	}
	if base == "" || !text.IsIdentName(name, '-', '_') {
		return nil
	}
	key := providerconfig.APIKey(c, prefix, "")
	field, pinned := ReasoningRoundtrip(c, prefix)
	p := New(Options{Name: name, DisplayName: c.AnyString(prefix + "display_name"), BaseURL: base, APIKey: key, Wire: parseWire(c.AnyString(prefix+"api"), Chat), ReasoningFormat: c.AnyString(prefix + "reasoning_format"), ReasoningField: field, ReasoningFieldPinned: pinned, SendCacheKey: c.AnyBool(prefix+"send_cache_key", false), RequestCost: c.AnyBool(prefix+"request_cost", false), CacheEnabled: c.AnyBool(prefix+"cache", false), CacheTTL: c.AnyString(prefix + "cache_ttl"), lengthHint: recipeLengthHint(name), Retry: providerconfig.Retry(c), ExtraHeaders: providerconfig.ExtraHeaders(c, prefix), ExtraBody: providerconfig.ExtraBody(c, prefix)})
	if id, set := c.AnySet(prefix + "catalog_id"); set {
		p.metadata.CatalogID = id
	} else if name != "ollama" {
		p.metadata.CatalogID = name
	}
	p.metadata.KeepModelOrder = !c.AnyBool(prefix+"sort_models", true)
	if name == "ollama" {
		p.efforts = nil
	}
	return p
}
func matching(i provider.Item, name, model string) bool {
	return provider.ProviderIDsEqual(i.Provider, name) && i.Model == model
}
func appendReasoningDetails(dst *[]any, raw string) {
	if raw == "" {
		return
	}
	var details []any
	if json.Unmarshal([]byte(raw), &details) != nil {
		return
	}
	*dst = append(*dst, details...)
}
func messages(c provider.Context, name, model, field string) []any {
	out := []any{}
	if c.SystemPrompt != "" {
		out = append(out, map[string]any{"role": "system", "content": c.SystemPrompt})
	}
	for i := 0; i < len(c.Items); {
		x := c.Items[i]
		switch x.Kind {
		case provider.ItemUserMessage:
			out = append(out, map[string]any{"role": "user", "content": x.Text})
			i++
		case provider.ItemToolResult:
			first := i
			for i < len(c.Items) && c.Items[i].Kind == provider.ItemToolResult {
				z := c.Items[i]
				content := z.Output
				if len(z.Images) > 0 && c.ImageInput == 0 {
					for _, im := range z.Images {
						if content != "" {
							content += "\n"
						}
						content += provider.ImagePlaceholder(im)
					}
				}
				out = append(out, map[string]any{"role": "tool", "tool_call_id": z.CallID, "content": content})
				i++
			}
			if c.ImageInput != 0 {
				var parts []any
				for _, z := range c.Items[first:i] {
					for _, im := range z.Images {
						if len(parts) == 0 {
							parts = append(parts, map[string]any{"type": "text", "text": "Image(s) from the preceding tool result(s):"})
						}
						parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + providerinternal.Or(im.MIME, "image/png") + ";base64," + im.DataB64}})
					}
				}
				if len(parts) > 0 {
					out = append(out, map[string]any{"role": "user", "content": parts})
				}
			}
		case provider.ItemAssistantMessage, provider.ItemToolCall, provider.ItemReasoning:
			var text, reason string
			var calls []any
			var details []any
			for i < len(c.Items) {
				z := c.Items[i]
				if z.Kind != provider.ItemAssistantMessage && z.Kind != provider.ItemToolCall && z.Kind != provider.ItemReasoning {
					break
				}
				if z.Kind == provider.ItemAssistantMessage {
					text += z.Text
				}
				if z.Kind == provider.ItemReasoning && matching(z, name, model) {
					if z.ReasoningText != "" {
						if reason != "" {
							reason += "\n"
						}
						reason += z.ReasoningText
					}
					appendReasoningDetails(&details, z.ReasoningJSON)
				}
				if z.Kind == provider.ItemToolCall {
					calls = append(calls, map[string]any{"id": z.CallID, "type": "function", "function": map[string]any{"name": z.ToolName, "arguments": providerinternal.Or(z.ToolArgumentsJSON, "{}")}})
				}
				i++
			}
			includeReasoning := field != "" && reason != "" && len(details) == 0
			if text != "" || includeReasoning || len(calls) > 0 || len(details) > 0 {
				m := map[string]any{"role": "assistant", "content": any(text)}
				if text == "" {
					m["content"] = nil
				}
				if len(calls) > 0 {
					m["tool_calls"] = calls
				}
				if len(details) > 0 {
					m["reasoning_details"] = details
				} else if includeReasoning {
					m[field] = reason
				}
				out = append(out, m)
			}
		default:
			i++
		}
	}
	return out
}
func cachePlan(o Options, meta *provider.ModelInfo) (bool, bool) {
	send := o.CacheEnabled
	if o.CacheAuto {
		send = meta == nil || meta.CostInput < 0 || meta.CostCacheWrite < 0 || meta.CostCacheWrite >= meta.CostInput
	}
	return send, send && strings.EqualFold(o.CacheTTL, "1h") && meta != nil && meta.CostCacheWrite1H >= 0
}
func cacheMessage(m map[string]any, ttl string) bool {
	content, ok := m["content"]
	if !ok || content == nil {
		return false
	}
	var parts []any
	switch x := content.(type) {
	case string:
		parts = []any{map[string]any{"type": "text", "text": x}}
	case []any:
		parts = x
	default:
		return false
	}
	if len(parts) == 0 {
		return false
	}
	last, ok := parts[len(parts)-1].(map[string]any)
	if !ok {
		return false
	}
	cc := map[string]any{"type": "ephemeral"}
	if strings.EqualFold(ttl, "1h") {
		cc["ttl"] = "1h"
	}
	last["cache_control"] = cc
	m["content"] = parts
	return true
}
func applyCache(messages []any, ttl string) {
	if len(messages) == 0 {
		return
	}
	floor := 0
	if first, ok := messages[0].(map[string]any); ok && first["role"] == "system" && cacheMessage(first, ttl) {
		floor = 1
	}
	for i := len(messages) - 1; i >= floor; i-- {
		if m, ok := messages[i].(map[string]any); ok && cacheMessage(m, ttl) {
			return
		}
	}
}
func chatBody(o Options, c provider.Context, model string) map[string]any {
	ms := messages(c, o.Name, model, o.ReasoningField)
	if o.CacheEnabled {
		applyCache(ms, o.CacheTTL)
	}
	b := map[string]any{"model": model, "stream": true, "messages": ms, "stream_options": map[string]any{"include_usage": true}}
	if o.FastTier != "" && c.Fast {
		b["service_tier"] = o.FastTier
	}
	if len(c.Tools) > 0 {
		var ts []any
		for _, t := range c.Tools {
			ts = append(ts, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": providerinternal.Schema(t, false)}})
		}
		b["tools"] = ts
	}
	if o.SendCacheKey {
		b["prompt_cache_key"] = providerinternal.Or(c.CacheKey, o.Name)
	}
	if o.Progress {
		b["return_progress"] = true
	}
	if o.RequestCost {
		b["usage"] = map[string]any{"include": true}
	}
	if o.ReasoningFormat == "nested" && c.Effort != "" {
		r := map[string]any{"enabled": c.Effort != "none"}
		if c.Effort != "none" {
			r["effort"] = c.Effort
		}
		b["reasoning"] = r
	} else if c.Effort != "" {
		b["reasoning_effort"] = c.Effort
	}
	providerconfig.ApplyExtraBody(b, o.ExtraBody)
	return b
}
func responsesBody(o Options, c provider.Context, model string) map[string]any {
	var in []any
	for _, x := range c.Items {
		switch x.Kind {
		case provider.ItemUserMessage:
			in = append(in, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": x.Text}}})
		case provider.ItemAssistantMessage:
			in = append(in, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": x.Text}}})
		case provider.ItemToolCall:
			in = append(in, map[string]any{"type": "function_call", "call_id": x.CallID, "name": x.ToolName, "arguments": providerinternal.Or(x.ToolArgumentsJSON, "{}")})
		case provider.ItemToolResult:
			if len(x.Images) == 0 {
				in = append(in, map[string]any{"type": "function_call_output", "call_id": x.CallID, "output": x.Output})
				break
			}
			parts := []any{}
			if x.Output != "" {
				parts = append(parts, map[string]any{"type": "input_text", "text": x.Output})
			}
			for _, im := range x.Images {
				if c.ImageInput != 0 {
					parts = append(parts, map[string]any{"type": "input_image", "image_url": "data:" + providerinternal.Or(im.MIME, "image/png") + ";base64," + im.DataB64})
				} else {
					parts = append(parts, map[string]any{"type": "input_text", "text": provider.ImagePlaceholder(im)})
				}
			}
			in = append(in, map[string]any{"type": "function_call_output", "call_id": x.CallID, "output": parts})
		case provider.ItemReasoning:
			if x.ReasoningJSON != "" && matching(x, o.Name, model) {
				var q any
				if json.Unmarshal([]byte(x.ReasoningJSON), &q) == nil {
					in = append(in, q)
				}
			}
		}
	}
	b := map[string]any{"model": model, "stream": true, "store": false, "instructions": c.SystemPrompt, "input": in}
	if o.FastTier != "" && c.Fast {
		b["service_tier"] = o.FastTier
	}
	if len(c.Tools)+len(c.ServerTools) > 0 {
		ts := make([]any, 0, len(c.Tools)+len(c.ServerTools))
		for _, t := range c.Tools {
			ts = append(ts, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": providerinternal.Schema(t, false)})
		}
		for _, t := range c.ServerTools {
			ts = append(ts, map[string]any{"type": t.Type})
		}
		b["tools"] = ts
		b["tool_choice"] = "auto"
		b["parallel_tool_calls"] = true
	}
	if o.SendCacheKey || o.CodexIdentity {
		b["prompt_cache_key"] = providerinternal.Or(c.CacheKey, o.Name)
	}
	if o.TextVerbosity {
		b["text"] = map[string]any{"verbosity": "low"}
	}
	if c.Effort != "none" {
		b["include"] = []string{"reasoning.encrypted_content"}
	}
	if c.Effort != "" {
		r := map[string]any{"effort": c.Effort}
		if c.Effort != "none" {
			r["summary"] = "auto"
		}
		b["reasoning"] = r
	}
	providerconfig.ApplyExtraBody(b, o.ExtraBody)
	return b
}

// authHeaders returns the configured extra headers plus bearer authorization.
func authHeaders(o Options) http.Header {
	h := o.ExtraHeaders.Clone()
	if h == nil {
		h = http.Header{}
	}
	if o.APIKey != "" {
		h.Set("Authorization", "Bearer "+o.APIKey)
	}
	return providerconfig.ExpandSessionHeaders(h, providerconfig.ProcessSessionID())
}

// requestHeaders expands the provider-declared/configured extra headers with the
// conversation's affinity id (or the per-process id outside any conversation) before the
// wire's own headers are set; DefaultHeaders already resolved user overrides and removals.
func requestHeaders(o Options, c provider.Context) http.Header {
	h := o.ExtraHeaders.Clone()
	if h == nil {
		h = http.Header{}
	}
	if o.APIKey != "" {
		h.Set("Authorization", "Bearer "+o.APIKey)
	}
	return providerconfig.ExpandSessionHeaders(h, providerinternal.Or(c.CacheKey, providerconfig.ProcessSessionID()))
}
func listModels(ctx context.Context, o Options, tick provider.TickFunc) ([]provider.ModelInfo, error) {
	headers := authHeaders(o)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := o.client.Get(ctx, o.BaseURL+"/models", headers, transport.DefaultMaxBody)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, errors.New(transport.ModelsError(o.Name, o.BaseURL, o.APIKey != "", 0))
	}
	if r.Status < 200 || r.Status >= 300 {
		return nil, errors.New(transport.ModelsError(o.Name, o.BaseURL, o.APIKey != "", r.Status))
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(r.Body, &body); err != nil {
		return nil, err
	}
	out := make([]provider.ModelInfo, 0, len(body.Data))
	for _, x := range body.Data {
		if x.ID != "" {
			m := provider.NewModelInfo()
			m.ID = x.ID
			out = append(out, m)
		}
	}
	return out, nil
}

func stream(ctx context.Context, o Options, c provider.Context, model string, cb provider.StreamCallback, tick provider.TickFunc) error {
	if model == "" {
		return fmt.Errorf("model is required")
	}
	var b map[string]any
	path := "/chat/completions"
	if o.Wire == Responses {
		b = responsesBody(o, c, model)
		path = "/responses"
	} else {
		b = chatBody(o, c, model)
	}
	raw, e := json.Marshal(b)
	if e != nil {
		return e
	}
	headers := func() http.Header {
		h := requestHeaders(o, c)
		h.Set("Accept", "text/event-stream")
		h.Set("Content-Type", "application/json")
		if o.CodexIdentity {
			id := providerinternal.Or(c.CacheKey, o.Name)
			h.Set("Session-Id", id)
			h.Set("X-Client-Request-Id", id)
			h.Set("OpenAI-Beta", "responses=experimental")
		}
		return h
	}
	client := o.client
	// Native OpenAI and Codex answer headers promptly, so a short deadline lets a
	// stalled edge fail over fast. Compatible gateways hold headers until the
	// model's first token, which reasoning models can delay well past 10s; the
	// idle timeout covers a dead stream there.
	if o.Name == "openai" || o.CodexIdentity {
		client.ConnectTimeout = 10 * time.Second
	}
	result, err := transport.RunStreamRetry(ctx, transport.StreamRetryOptions{
		Client:  client,
		URL:     o.BaseURL + path,
		Body:    raw,
		Headers: headers,
		Retry:   o.Retry,
		NewParser: func(emit provider.StreamCallback) transport.StreamParser {
			return newEventsOptions(o.Wire, emit, o.Progress, o.cacheWrite1H, o.lengthHint)
		},
	}, cb, tick)
	if err != nil {
		var response *provider.StreamResponse
		if result.Parser != nil {
			response = result.Parser.Response()
		}
		return provider.WithStreamResponse(err, response)
	}
	if result.Parser == nil {
		return nil
	}
	if result.Response.Status < 200 || result.Response.Status >= 300 {
		return cb(provider.StreamEvent{Kind: provider.EventError, Message: transport.APIError(result.Response.Status, result.Response.Body), HTTPStatus: result.Response.Status})
	}
	return nil
}

func recipeLengthHint(name string) string {
	if name == "ollama" {
		return "ollama's context window may be too small for the prompt — restart `ollama serve` with a larger OLLAMA_CONTEXT_LENGTH (e.g. 16384), or raise num_ctx on the model"
	}
	return ""
}
func parseModelProbe(body, model string, out *provider.ModelInfo) {
	var x struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal([]byte(body), &x) != nil {
		return
	}
	for _, q := range x.Data {
		var m struct {
			ID      string `json:"id"`
			Context int64  `json:"context_length"`
			Max     int64  `json:"max_completion_tokens"`
		}
		if json.Unmarshal(q, &m) == nil && m.ID == model {
			out.Context, out.MaxOutput = m.Context, m.Max
			return
		}
	}
}

func newAnthropicRecipe(c *config.Config, name, prefix string) provider.Provider {
	base := c.AnyString(prefix + "base_url")
	if base == "" || !text.IsIdentName(name, '-', '_') {
		return nil
	}
	key := providerconfig.APIKey(c, prefix, "")
	catalogID := name
	if id, set := c.AnySet(prefix + "catalog_id"); set {
		catalogID = id
	}
	return anthropic.New(anthropic.Options{Name: name, DisplayName: c.AnyString(prefix + "display_name"), BaseURL: base, APIKey: key, Version: c.AnyString(prefix + "version"), CatalogID: catalogID, KeepModelOrder: !c.AnyBool(prefix+"sort_models", true), AllowEmptySignature: true, DefaultThinking: "budget", ThinkingMode: c.AnyString(prefix + "thinking_mode"), ThinkingBudget: int64(c.AnyInt(prefix+"thinking_budget", 0)), MaxTokens: int64(c.AnyInt(prefix+"max_tokens", 0)), CacheTTL: c.AnyString(prefix + "cache_ttl"), CacheEnabled: c.AnyBool(prefix+"cache", false), ShowReasoning: c.Bool("show_reasoning", false), Retry: providerconfig.Retry(c), ExtraHeaders: providerconfig.ExtraHeaders(c, prefix), ExtraBody: providerconfig.ExtraBody(c, prefix)})
}
