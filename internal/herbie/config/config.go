// Package config resolves herbie settings from run, conversation, environment, state, config, and defaults.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/paulsmith/computeruser/internal/herbie/text"
)

const DefaultValue = "(default)"

type source string

const (
	SourceRun          source = "run"
	sourceConversation source = "conversation"
	SourceEnv          source = "env"
	sourceState        source = "state"
	sourceConfig       source = "config"
	SourceDefault      source = "default"
)

type kind int

const (
	stringKind kind = iota
	intKind
	sizeKind
	tokensKind
	durationKind
)

type setting struct {
	Key, env, defaultValue, description, Choices, example string
	kind                                                  kind
	min, max                                              int64
	Editable, secret, keepEmpty                           bool
}

var descriptions = map[string]string{
	"preset":                               "Preset from presets.<name> to apply at startup; empty disables",
	"provider":                             "Backend: codex, openai, openai-compatible, anthropic, anthropic-compatible, llamacpp (llama.cpp alias), ollama, openrouter, opencode, opencode-go, mock",
	"model":                                "Model id (provider-specific; some auto-fill or require it)",
	"title.preset":                         "Preset for internal session-title generation; empty uses the session selection",
	"effort":                               "Reasoning effort (provider-specific); empty omits it",
	"fast":                                 "Use the provider's premium low-latency mode when supported",
	"system_prompt":                        "Replace the built-in base prompt (context sections still follow); @path reads a file; (none) sends no system message at all",
	"system_prompt_append":                 "Text appended after the base system prompt; @path reads a file",
	"no_env":                               "Skip the Environment section in the system prompt",
	"no_agents_md":                         "Skip AGENTS.md project instructions in the system prompt",
	"no_skills":                            "Skip the skills listing in the system prompt",
	"no_subagents":                         "Skip the subagents section in the system prompt",
	"no_tasks":                             "Disable background tasks: bash timeouts kill instead of detaching, and the task tools are not offered",
	"markdown":                             "Render Markdown in the terminal (TTY only; piped output is always raw)",
	"show_reasoning":                       "Show reasoning/CoT deltas live (default off)",
	"sort_models":                          "Sort the /model picker newest-first; auto uses the provider's own default",
	"context_limit":                        "Manual context-window size for the % display; overrides auto-detect",
	"display_width":                        "Content width: auto uses full width through 110 columns and 100 beyond that; terminal always uses full width, or set columns",
	"notify":                               "Desktop-notification style: auto, bel, osc9, off (auto detects from the terminal)",
	"theme":                                "Color theme: auto, dark, light, ansi, off (auto detects from the terminal)",
	"images":                               "Inline tool-result images: auto, kitty, sixel, off (auto detects from the terminal)",
	"tint":                                 "Identity tint for model output; an active preset's own tint wins until set here. Ignored by the ansi and off themes",
	"keep_awake":                           "Inhibit idle system sleep while a turn is running (display may still blank)",
	"compact.auto":                         "Auto-summarize history when it nears the context window (manual /compact still works)",
	"compact.threshold":                    "Auto-compact when context usage reaches this percent of the window",
	"max_turns":                            "Interactive: pause for confirmation after this many model round-trips per user turn",
	"catalog.url":                          "Model-metadata catalog endpoint (models.dev api.json shape); empty disables fetching",
	"catalog.refresh":                      "Re-fetch the cached model catalog when older than this; 0 disables fetching",
	"no_session":                           "Skip recording conversations and typed prompts; auto skips both for dev providers (mock)",
	"session_title":                        "Generate a short session title from the first prompt (one extra model call)",
	"session_retention_days":               "Delete sessions after this many inactive days; 0 disables pruning",
	"transcript":                           "Path to mirror the Ctrl-T transcript view; empty disables",
	"trace":                                "Path to a wire-level HTTP/SSE trace dump; empty disables",
	"image_input":                          "Let the model view images via the read tool; auto detects per provider/model",
	"tool_output_cap":                      "Max bytes captured from a tool's output",
	"keyword.preset":                       "Preset used by keyword_search's relevance evaluator; empty disables the tool",
	"bash.timeout":                         "Default bash-tool command timeout: the command detaches into a background task (kills when tasks are disabled); 0 disables",
	"bash.timeout_max":                     "Ceiling on the model's per-call bash timeout; 0 disables",
	"bash.timeout_grace":                   "Grace window between SIGTERM and SIGKILL for bash commands; 0 skips",
	"bash.background_yield":                "Initial output window before an explicitly backgrounded bash command detaches into a task",
	"bash.shell":                           "Shell for the bash tool, a $PATH name or path (default: bash, else sh)",
	"browser.headless":                     "Hide the owned browser window; headed mode is the default",
	"browser.timeout":                      "Default timeout for one browser action; 0 disables",
	"browser.timeout_max":                  "Ceiling on the model's per-action browser timeout; 0 disables",
	"mcp.timeout":                          "Default timeout for one remote MCP call",
	"mcp.timeout_max":                      "Ceiling on the model's per-call MCP timeout",
	"task.wait_timeout":                    "Default task_wait timeout when the model omits one; a kill request with no timeout kills immediately instead",
	"task.max_running":                     "Maximum concurrently running background tasks",
	"task.notify":                          "Unattended execution: when a background task finishes while idle at the prompt, start an agent turn without user input (the model may run tools with nobody present)",
	"task.notify_delay":                    "Quiet period after the latest task completion before an automatic turn; 0 starts one at the first opportunity",
	"task.notify_max":                      "Automatic turns allowed in a row without user input; 0 disables them",
	"http.max_retries":                     "Additional retries for transient HTTP failures",
	"http.retry_base":                      "Base backoff between HTTP retries",
	"http.idle_timeout":                    "Silence on a streaming response before giving up; 0 disables",
	"providers.openai-compatible.base_url": "Base URL for the OpenAI-compatible endpoint",
	"providers.openai-compatible.api_key":  "Bearer token for the OpenAI-compatible endpoint",
	"providers.openai-compatible.display_name":        "Display name for the OpenAI-compatible provider",
	"providers.openai-compatible.api":                 "Request protocol: chat or responses",
	"providers.openai-compatible.metadata_api":        "Model-list protocol: openai or anthropic",
	"providers.openai-compatible.reasoning_format":    "Reasoning request dialect: flat or nested",
	"providers.openai-compatible.reasoning_roundtrip": "Replay reasoning text to the model (off/on, or a field name)",
	"providers.openai-compatible.send_cache_key":      "Send a stable prompt_cache_key; auto uses the provider default",
	"providers.openai-compatible.request_cost":        "Request usage accounting; auto uses the provider default",
	"providers.openai-compatible.cache":               "Send prompt cache-control breakpoints; auto uses the provider default",
	"providers.openai-compatible.cache_ttl":           "Cache breakpoint TTL: 5m or 1h",
	"providers.anthropic-compatible.base_url":         "Base URL for the Anthropic-compatible /v1 endpoint",
	"providers.anthropic-compatible.api_key":          "x-api-key token for the Anthropic-compatible endpoint",
	"providers.anthropic-compatible.display_name":     "Display name for the Anthropic-compatible provider",
	"providers.anthropic-compatible.metadata_api":     "Model-list protocol: openai or anthropic",
	"providers.anthropic-compatible.max_tokens":       "Max output tokens; unset follows the model's own cap",
	"providers.anthropic-compatible.thinking_mode":    "Thinking mode: adaptive, budget, or off",
	"providers.anthropic-compatible.thinking_budget":  "Budget-mode thinking tokens",
	"providers.anthropic-compatible.cache":            "Send prompt cache-control breakpoints; auto uses the provider default",
	"providers.anthropic-compatible.cache_ttl":        "Cache breakpoint TTL: 5m or 1h",
	"providers.anthropic-compatible.version":          "anthropic-version request header value",
	"providers.llamacpp.base_url":                     "Full llama-server base URL; overrides port",
	"providers.llamacpp.api_key":                      "Bearer token when llama-server requires auth",
	"providers.llamacpp.port":                         "Port for the local llama-server",
	"providers.openrouter.title":                      "X-Title header for OpenRouter attribution (empty disables)",
	"providers.openrouter.referer":                    "HTTP-Referer header for OpenRouter attribution (empty disables)",
	"providers.mock.script":                           "Path to a mock-provider script",
	"providers.opencode.api_key":                      "OpenCode API key",
	"providers.opencode-go.api_key":                   "OpenCode Go API key",
	"providers.openai.display_name":                   "Display name for OpenAI",
	"providers.anthropic.display_name":                "Display name for Anthropic",
	"providers.codex.display_name":                    "Display name for Codex",
}

func s(key, env, def, choices string, kind kind, min, max int64, empty bool) setting {
	return setting{Key: key, env: env, defaultValue: def, description: descriptions[key], Choices: choices, kind: kind, min: min, max: max, keepEmpty: empty}
}

var registry = []setting{
	s("preset", "HERBIE_PRESET", "", "", stringKind, 0, 0, true), s("provider", "HERBIE_PROVIDER", "", "", stringKind, 0, 0, true), s("model", "HERBIE_MODEL", "", "", stringKind, 0, 0, true), s("title.preset", "HERBIE_TITLE_PRESET", "", "", stringKind, 0, 0, true), s("effort", "HERBIE_EFFORT", "", "", stringKind, 0, 0, true), s("fast", "HERBIE_FAST", "off", "on|off", stringKind, 0, 0, false), s("system_prompt", "HERBIE_SYSTEM_PROMPT", "", "", stringKind, 0, 0, true), s("system_prompt_append", "HERBIE_SYSTEM_PROMPT_APPEND", "", "", stringKind, 0, 0, true),
	s("no_env", "HERBIE_NO_ENV", "", "on|off", stringKind, 0, 0, false), s("no_agents_md", "HERBIE_NO_AGENTS_MD", "", "on|off", stringKind, 0, 0, false), s("no_skills", "HERBIE_NO_SKILLS", "", "on|off", stringKind, 0, 0, false), s("no_subagents", "HERBIE_NO_SUBAGENTS", "", "on|off", stringKind, 0, 0, false), s("no_tasks", "HERBIE_NO_TASKS", "", "on|off", stringKind, 0, 0, false),
	s("markdown", "HERBIE_MARKDOWN", "1", "on|off", stringKind, 0, 0, false), s("show_reasoning", "HERBIE_SHOW_REASONING", "", "on|off", stringKind, 0, 0, false), s("sort_models", "HERBIE_SORT_MODELS", "auto", "auto|on|off", stringKind, 0, 0, false), s("context_limit", "HERBIE_CONTEXT_LIMIT", "", "", tokensKind, 0, 0, false), s("display_width", "HERBIE_DISPLAY_WIDTH", "auto", "auto|terminal", intKind, 20, 0, false), s("notify", "HERBIE_NOTIFY", "auto", "auto|bel|osc9|off", stringKind, 0, 0, false), s("theme", "HERBIE_THEME", "auto", "auto|dark|light|ansi|off", stringKind, 0, 0, false), s("images", "HERBIE_IMAGES", "auto", "auto|kitty|sixel|off", stringKind, 0, 0, false), s("tint", "HERBIE_TINT", "teal", "teal|violet|rose|sage", stringKind, 0, 0, false),
	s("keep_awake", "HERBIE_KEEP_AWAKE", "1", "on|off", stringKind, 0, 0, false), s("compact.auto", "HERBIE_COMPACT_AUTO", "1", "on|off", stringKind, 0, 0, false), s("compact.threshold", "HERBIE_COMPACT_THRESHOLD", "85", "", intKind, 1, 100, false), s("max_turns", "HERBIE_MAX_TURNS", "", "", intKind, 0, 0, false),
	s("catalog.url", "HERBIE_CATALOG_URL", "https://models.dev/api.json", "", stringKind, 0, 0, true), s("catalog.refresh", "HERBIE_CATALOG_REFRESH", "24h", "", durationKind, 0, 0, false),
	s("no_session", "HERBIE_NO_SESSION", "auto", "auto|on|off", stringKind, 0, 0, false), s("session_title", "HERBIE_SESSION_TITLE", "1", "on|off", stringKind, 0, 0, false), s("session_retention_days", "HERBIE_SESSION_RETENTION_DAYS", "30", "", intKind, 0, 36500, false), s("transcript", "HERBIE_TRANSCRIPT", "", "", stringKind, 0, 0, true), s("trace", "HERBIE_TRACE", "", "", stringKind, 0, 0, true),
	s("image_input", "HERBIE_IMAGE_INPUT", "auto", "auto|on|off", stringKind, 0, 0, false), s("tool_output_cap", "HERBIE_TOOL_OUTPUT_CAP", "50k", "", sizeKind, 0, 0, false), s("keyword.preset", "HERBIE_KEYWORD_PRESET", "", "", stringKind, 0, 0, true), s("bash.timeout", "HERBIE_BASH_TIMEOUT", "2m", "", durationKind, 0, 0, false), s("bash.timeout_max", "HERBIE_BASH_TIMEOUT_MAX", "30m", "", durationKind, 0, 0, false), s("bash.timeout_grace", "HERBIE_BASH_TIMEOUT_GRACE", "2s", "", durationKind, 0, 300000, false), s("bash.background_yield", "HERBIE_BASH_BACKGROUND_YIELD", "5s", "", durationKind, 0, 0, false), s("bash.shell", "HERBIE_BASH_SHELL", "", "", stringKind, 0, 0, false), s("browser.headless", "HERBIE_BROWSER_HEADLESS", "", "on|off", stringKind, 0, 0, false), s("browser.timeout", "HERBIE_BROWSER_TIMEOUT", "60s", "", durationKind, 0, 0, false), s("browser.timeout_max", "HERBIE_BROWSER_TIMEOUT_MAX", "5m", "", durationKind, 0, 0, false), s("mcp.timeout", "HERBIE_MCP_TIMEOUT", "60s", "", durationKind, 0, 0, false), s("mcp.timeout_max", "HERBIE_MCP_TIMEOUT_MAX", "5m", "", durationKind, 0, 0, false), s("task.wait_timeout", "HERBIE_TASK_WAIT_TIMEOUT", "10m", "", durationKind, 0, 0, false), s("task.max_running", "HERBIE_TASK_MAX_RUNNING", "32", "", intKind, 1, 64, false), s("task.notify", "HERBIE_TASK_NOTIFY", "0", "on|off", stringKind, 0, 0, false), s("task.notify_delay", "HERBIE_TASK_NOTIFY_DELAY", "1s", "", durationKind, 0, 0, false), s("task.notify_max", "HERBIE_TASK_NOTIFY_MAX", "5", "", intKind, 0, 0, false),
	s("http.max_retries", "HERBIE_HTTP_MAX_RETRIES", "4", "", intKind, 0, 100, false), s("http.retry_base", "HERBIE_HTTP_RETRY_BASE", "1s", "", durationKind, 1, 0, false), s("http.idle_timeout", "HERBIE_HTTP_IDLE_TIMEOUT", "10m", "", durationKind, 0, 0, false),
	s("providers.openai-compatible.base_url", "HERBIE_OPENAI_BASE_URL", "", "", stringKind, 0, 0, false),
	s("providers.openai-compatible.api_key", "HERBIE_OPENAI_API_KEY", "", "", stringKind, 0, 0, false),
	s("providers.openai-compatible.display_name", "HERBIE_OPENAI_DISPLAY_NAME", "", "", stringKind, 0, 0, true),
	s("providers.openai-compatible.api", "HERBIE_OPENAI_API", "", "chat|responses", stringKind, 0, 0, false),
	s("providers.openai-compatible.metadata_api", "", "", "auto|openai|anthropic", stringKind, 0, 0, false),
	s("providers.openai-compatible.reasoning_format", "HERBIE_OPENAI_REASONING_FORMAT", "", "flat|nested", stringKind, 0, 0, false),
	s("providers.openai-compatible.reasoning_roundtrip", "HERBIE_REASONING_ROUNDTRIP", "", "", stringKind, 0, 0, true),
	s("providers.openai-compatible.send_cache_key", "HERBIE_OPENAI_SEND_CACHE_KEY", "auto", "auto|on|off", stringKind, 0, 0, false),
	s("providers.openai-compatible.request_cost", "HERBIE_OPENAI_REQUEST_COST", "auto", "auto|on|off", stringKind, 0, 0, false),
	s("providers.openai-compatible.cache", "HERBIE_OPENAI_CACHE", "auto", "auto|on|off", stringKind, 0, 0, false),
	s("providers.openai-compatible.cache_ttl", "HERBIE_OPENAI_CACHE_TTL", "1h", "5m|1h", stringKind, 0, 0, false),
	s("providers.anthropic-compatible.base_url", "HERBIE_ANTHROPIC_BASE_URL", "", "", stringKind, 0, 0, false),
	s("providers.anthropic-compatible.api_key", "HERBIE_ANTHROPIC_API_KEY", "", "", stringKind, 0, 0, false),
	s("providers.anthropic-compatible.display_name", "HERBIE_ANTHROPIC_DISPLAY_NAME", "", "", stringKind, 0, 0, true),
	s("providers.anthropic-compatible.metadata_api", "", "", "auto|openai|anthropic", stringKind, 0, 0, false),
	s("providers.anthropic-compatible.max_tokens", "HERBIE_ANTHROPIC_MAX_TOKENS", "", "", intKind, 1, 0, false),
	s("providers.anthropic-compatible.thinking_mode", "HERBIE_ANTHROPIC_THINKING_MODE", "", "adaptive|budget|off", stringKind, 0, 0, false),
	s("providers.anthropic-compatible.thinking_budget", "HERBIE_ANTHROPIC_THINKING_BUDGET", "", "", intKind, 1, 0, false),
	s("providers.anthropic-compatible.cache", "HERBIE_ANTHROPIC_CACHE", "auto", "auto|on|off", stringKind, 0, 0, false),
	s("providers.anthropic-compatible.cache_ttl", "HERBIE_ANTHROPIC_CACHE_TTL", "1h", "5m|1h", stringKind, 0, 0, false),
	s("providers.anthropic-compatible.version", "HERBIE_ANTHROPIC_VERSION", "", "", stringKind, 0, 0, false),
	s("providers.llamacpp.base_url", "HERBIE_LLAMACPP_BASE_URL", "", "", stringKind, 0, 0, false),
	s("providers.llamacpp.api_key", "HERBIE_LLAMACPP_API_KEY", "", "", stringKind, 0, 0, false),
	s("providers.llamacpp.port", "HERBIE_LLAMACPP_PORT", "8080", "", intKind, 1, 65535, false),
	s("providers.openrouter.title", "HERBIE_OPENROUTER_TITLE", "herbie", "", stringKind, 0, 0, true),
	s("providers.openrouter.referer", "HERBIE_OPENROUTER_REFERER", "https://github.com/paulsmith/herbie", "", stringKind, 0, 0, true),
	s("providers.mock.script", "HERBIE_MOCK_SCRIPT", "", "", stringKind, 0, 0, false),
	s("providers.opencode.api_key", "HERBIE_OPENCODE_API_KEY", "", "", stringKind, 0, 0, false),
	s("providers.opencode-go.api_key", "HERBIE_OPENCODE_GO_API_KEY", "", "", stringKind, 0, 0, false),
	s("providers.openai.display_name", "", "", "", stringKind, 0, 0, true),
	s("providers.anthropic.display_name", "", "", "", stringKind, 0, 0, true),
	s("providers.codex.display_name", "", "", "", stringKind, 0, 0, true),
}
var settings = func() map[string]setting {
	for i := range registry {
		for _, key := range []string{"title.preset", "session_title", "markdown", "show_reasoning", "sort_models", "context_limit", "display_width", "notify", "theme", "images", "tint", "keep_awake", "compact.auto", "compact.threshold", "max_turns", "image_input", "tool_output_cap", "bash.timeout", "bash.timeout_max", "bash.timeout_grace", "bash.background_yield", "browser.headless", "browser.timeout", "browser.timeout_max", "mcp.timeout", "mcp.timeout_max", "task.wait_timeout", "task.max_running", "task.notify", "task.notify_delay", "task.notify_max", "http.max_retries", "http.retry_base", "http.idle_timeout"} {
			if registry[i].Key == key {
				registry[i].Editable = true
			}
		}
		if strings.HasSuffix(registry[i].Key, ".api_key") {
			registry[i].secret = true
		}
		if registry[i].Key == "display_width" {
			registry[i].example = "100"
		}
	}
	m := map[string]setting{}
	for _, x := range registry {
		m[x.Key] = x
	}
	return m
}()

func Settings() []setting                   { return slices.Clone(registry) }
func SettingFor(key string) (setting, bool) { x, ok := settings[key]; return x, ok }
func isKnown(key string) bool               { _, ok := settings[key]; return ok }

type value struct {
	key, Value string
	Source     source
	Set        bool
}
type ObjectEntry struct {
	Key   string
	Value any
}

type orderedJSON struct {
	kind   byte
	scalar any
	object []struct {
		key   string
		value orderedJSON
	}
	array []orderedJSON
}

type Config struct {
	env                            map[string]string
	file, state, conversation, run map[string]any
	fileOrder, stateOrder          map[string][]string
	fileUnusable                   bool
}

type LegacyProviderConfigError struct {
	Keys []string
}

func (e *LegacyProviderConfigError) Error() string {
	return fmt.Sprintf("legacy provider configuration keys %s are no longer supported; move them under providers.<id> (for example openai.base_url → providers.openai-compatible.base_url)", strings.Join(e.Keys, ", "))
}

var legacyProviderKeys = map[string]string{
	"provider_name":              "providers.<id>.display_name",
	"openai.base_url":            "providers.openai-compatible.base_url",
	"openai.api_key":             "providers.openai-compatible.api_key",
	"openai.api":                 "providers.openai-compatible.api",
	"openai.reasoning_format":    "providers.openai-compatible.reasoning_format",
	"openai.reasoning_roundtrip": "providers.openai-compatible.reasoning_roundtrip",
	"openai.send_cache_key":      "providers.openai-compatible.send_cache_key",
	"openai.request_cost":        "providers.openai-compatible.request_cost",
	"openai.cache":               "providers.openai-compatible.cache",
	"openai.cache_ttl":           "providers.openai-compatible.cache_ttl",
	"anthropic.base_url":         "providers.anthropic-compatible.base_url",
	"anthropic.api_key":          "providers.anthropic-compatible.api_key",
	"anthropic.max_tokens":       "providers.anthropic-compatible.max_tokens",
	"anthropic.thinking_mode":    "providers.anthropic-compatible.thinking_mode",
	"anthropic.thinking_budget":  "providers.anthropic-compatible.thinking_budget",
	"anthropic.cache":            "providers.anthropic-compatible.cache",
	"anthropic.cache_ttl":        "providers.anthropic-compatible.cache_ttl",
	"anthropic.version":          "providers.anthropic-compatible.version",
	"llamacpp.port":              "providers.llamacpp.port",
	"openrouter.title":           "providers.openrouter.title",
	"openrouter.referer":         "providers.openrouter.referer",
	"mock.script":                "providers.mock.script",
}

func hasPath(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	if _, ok := m[key]; ok {
		return true
	}
	_, ok := nestedValue(m, strings.Split(key, ".")...)
	return ok
}

func (c *Config) legacyProviderConfigError() error {
	seen := map[string]struct{}{}
	for _, tier := range []map[string]any{c.file, c.state} {
		for key := range legacyProviderKeys {
			if hasPath(tier, key) {
				seen[key] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	keys := slices.Sorted(maps.Keys(seen))
	return &LegacyProviderConfigError{Keys: keys}
}

func decodeOrdered(decoder *json.Decoder) (orderedJSON, error) {
	token, err := decoder.Token()
	if err != nil {
		return orderedJSON{}, err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			value := orderedJSON{kind: '{'}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return orderedJSON{}, err
				}
				name, ok := key.(string)
				if !ok {
					return orderedJSON{}, errors.New("configuration object key is not a string")
				}
				child, err := decodeOrdered(decoder)
				if err != nil {
					return orderedJSON{}, err
				}
				value.object = append(value.object, struct {
					key   string
					value orderedJSON
				}{key: name, value: child})
			}
			if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
				if err != nil {
					return orderedJSON{}, err
				}
				return orderedJSON{}, errors.New("configuration object is not closed")
			}
			return value, nil
		case '[':
			value := orderedJSON{kind: '['}
			for decoder.More() {
				child, err := decodeOrdered(decoder)
				if err != nil {
					return orderedJSON{}, err
				}
				value.array = append(value.array, child)
			}
			if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
				if err != nil {
					return orderedJSON{}, err
				}
				return orderedJSON{}, errors.New("configuration array is not closed")
			}
			return value, nil
		}
	}
	return orderedJSON{kind: 'v', scalar: token}, nil
}

func (value orderedJSON) any() any {
	switch value.kind {
	case '{':
		out := make(map[string]any, len(value.object))
		for _, member := range value.object {
			out[member.key] = member.value.any()
		}
		return out
	case '[':
		out := make([]any, len(value.array))
		for i, child := range value.array {
			out[i] = child.any()
		}
		return out
	default:
		return value.scalar
	}
}

func (value orderedJSON) collectOrder(path string, orders map[string][]string) {
	if value.kind != '{' {
		return
	}
	for _, member := range value.object {
		orders[path] = append(orders[path], member.key)
		childPath := member.key
		if path != "" {
			childPath = path + "." + member.key
		}
		member.value.collectOrder(childPath, orders)
	}
}

func parseJSON(text string) (map[string]any, map[string][]string, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	value, err := decodeOrdered(decoder)
	if err != nil {
		return nil, nil, err
	}
	if value.kind != '{' {
		return nil, nil, errors.New("configuration root is not an object")
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, nil, errors.New("configuration has trailing data")
		}
		return nil, nil, err
	} else if token != nil {
		return nil, nil, errors.New("configuration has trailing data")
	}
	orders := map[string][]string{}
	value.collectOrder("", orders)
	return value.any().(map[string]any), orders, nil
}

func New() *Config { c := NewWithEnv(os.Environ()); _ = c.Init(); return c }

func NewWithEnv(entries []string) *Config {
	e := map[string]string{}
	for _, v := range entries {
		if k, x, ok := strings.Cut(v, "="); ok {
			e[k] = x
		}
	}
	return &Config{env: e, run: map[string]any{}}
}
func (c *Config) SetRun(k, v string) { c.run[k] = v }
func (c *Config) ClearRun(k string)  { delete(c.run, k) }

// runSnapshot preserves runtime settings while a selection is applied transactionally.
type runSnapshot map[string]any

func (c *Config) SnapshotRun() runSnapshot                 { return clone(c.run) }
func (c *Config) RestoreRun(snapshot runSnapshot)          { c.run = clone(snapshot) }
func (c *Config) SnapshotConversation() runSnapshot        { return clone(c.conversation) }
func (c *Config) RestoreConversation(snapshot runSnapshot) { c.conversation = clone(snapshot) }
func (c *Config) SetConversation(k, v string) {
	if c.conversation == nil {
		c.conversation = map[string]any{}
	}
	c.conversation[k] = v
}
func scalarString(x any) (string, bool) {
	switch v := x.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), true
	case bool:
		if v {
			return "1", true
		}
		return "0", true
	default:
		return "", false
	}
}

func valueAt(m map[string]any, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	if x, ok := m[key]; ok {
		return scalarString(x)
	}
	x, ok := nestedValue(m, strings.Split(key, ".")...)
	if !ok {
		return "", false
	}
	return scalarString(x)
}
func nestedValue(m map[string]any, parts ...string) (any, bool) {
	var x any = m
	for _, part := range parts {
		q, ok := x.(map[string]any)
		if !ok {
			return nil, false
		}
		x, ok = q[part]
		if !ok {
			return nil, false
		}
	}
	return x, true
}
func present(v string, ok, skip bool) bool { return ok && (!skip || v != "") }
func (c *Config) allowed(tier map[string]any, key string) bool {
	if key != "model" && key != "effort" && key != "fast" {
		return true
	}
	p, ok := valueAt(tier, "provider")
	if !ok || p == "" {
		return true
	}
	a := c.Lookup("provider").Value
	return a != "" && providerIDsEqual(a, p)
}
func (c *Config) Lookup(key string) value {
	st, known := settings[key]
	if !known {
		return value{key: key}
	}

	skip := known && !st.keepEmpty
	tiers := []struct {
		m    map[string]any
		s    source
		bind bool
	}{{c.run, SourceRun, false}, {c.conversation, sourceConversation, true}}
	for _, t := range tiers {
		if v, ok := valueAt(t.m, key); present(v, ok, skip) && (!t.bind || c.allowed(t.m, key)) {
			return c.resolved(key, v, t.s, true)
		}
	}
	if known {
		if v, ok := c.env[st.env]; present(v, ok, skip) {
			return c.resolved(key, v, SourceEnv, true)
		}
	}
	for _, t := range []struct {
		m map[string]any
		s source
	}{{c.state, sourceState}, {c.file, sourceConfig}} {
		if v, ok := valueAt(t.m, key); present(v, ok, skip) && c.allowed(t.m, key) {
			return c.resolved(key, v, t.s, true)
		}
	}
	return c.resolved(key, st.defaultValue, SourceDefault, st.defaultValue != "")
}
func (c *Config) resolved(k, v string, s source, set bool) value {
	if v == DefaultValue {
		v = settings[k].defaultValue
	}
	return value{k, v, s, set}
}
func (c *Config) String(k string) string { return c.Lookup(k).Value }

// AnyString resolves an arbitrary config path for config-defined providers.
func (c *Config) AnySet(k string) (string, bool) {
	for _, tier := range []map[string]any{c.run, c.conversation, c.state, c.file} {
		if v, ok := valueAt(tier, k); ok {
			return v, true
		}
	}
	return "", false
}

// Node returns a deep copy of an arbitrary configuration object from the highest-precedence tier.
// It is intended for structured settings such as catalog.models.
func (c *Config) Node(k string) any {
	for _, tier := range []map[string]any{c.run, c.conversation, c.state, c.file} {
		x, ok := nestedValue(tier, strings.Split(k, ".")...)
		if ok && x != nil {
			b, _ := json.Marshal(x)
			var out any
			_ = json.Unmarshal(b, &out)
			return out
		}
	}
	return nil
}
func (c *Config) AnyString(k string) string {
	if isKnown(k) {
		return c.String(k)
	}
	for _, tier := range []map[string]any{c.run, c.conversation, c.state, c.file} {
		if v, ok := valueAt(tier, k); ok {
			return v
		}
	}
	return ""
}

// AnyInt parses a dynamic provider-block integer using the same scalar syntax as registered settings.
func (c *Config) AnyInt(k string, fallback int) int {
	if isKnown(k) {
		return c.Int(k)
	}
	v, ok := parseScaled(c.AnyString(k), intKind)
	if !ok || v < 0 {
		return fallback
	}
	return int(v)
}

// AnyTristate resolves a dynamic on/off/auto provider setting.
func (c *Config) AnyTristate(k string) (bool, bool) {
	withFalse, withTrue := c.AnyBool(k, false), c.AnyBool(k, true)
	if withFalse == withTrue {
		return withTrue, false
	}
	return false, true
}

func orderedObjectKeys(object map[string]any, order []string) []string {
	seen := make(map[string]struct{}, len(object))
	keys := make([]string, 0, len(object))
	for _, key := range order {
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		if _, ok := object[key]; ok {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}
	remaining := make([]string, 0, len(object)-len(keys))
	for key := range object {
		if _, ok := seen[key]; !ok {
			remaining = append(remaining, key)
		}
	}
	slices.Sort(remaining)
	return append(keys, remaining...)
}

// ObjectEntries returns an object's immediate members in source order from the highest-precedence tier.
func (c *Config) ObjectEntries(k string) []ObjectEntry {
	for _, tier := range []struct {
		values map[string]any
		order  map[string][]string
	}{{c.run, nil}, {c.conversation, nil}, {c.state, c.stateOrder}, {c.file, c.fileOrder}} {
		object, ok := objectAt(tier.values, k)
		if !ok {
			continue
		}
		keys := orderedObjectKeys(object, tier.order[k])
		out := make([]ObjectEntry, 0, len(keys))
		for _, key := range keys {
			out = append(out, ObjectEntry{Key: key, Value: object[key]})
		}
		return out
	}
	return nil
}

func objectAt(m map[string]any, key string) (map[string]any, bool) {
	if m == nil {
		return nil, false
	}
	if value, ok := m[key]; ok {
		object, ok := value.(map[string]any)
		return object, ok
	}
	value, ok := nestedValue(m, strings.Split(key, ".")...)
	if !ok {
		return nil, false
	}
	object, ok := value.(map[string]any)
	return object, ok
}

// ObjectKeys returns the immediate member names found under a configuration object.
func (c *Config) ObjectKeys(k string) []string {
	seen := map[string]struct{}{}
	for _, tier := range []map[string]any{c.run, c.conversation, c.state, c.file} {
		if value, ok := nestedValue(tier, strings.Split(k, ".")...); ok {
			if object, ok := value.(map[string]any); ok {
				for name := range object {
					seen[name] = struct{}{}
				}
			}
		}
		prefix := k + "."
		for name := range tier {
			if rest, ok := strings.CutPrefix(name, prefix); ok {
				if child, _, ok := strings.Cut(rest, "."); ok {
					seen[child] = struct{}{}
				} else if rest != "" {
					seen[rest] = struct{}{}
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// CanonicalProviderID maps selection aliases to the stable provider identity.
func CanonicalProviderID(name string) string {
	if name == "llama.cpp" {
		return "llamacpp"
	}
	return name
}

func providerIDsEqual(left, right string) bool {
	return CanonicalProviderID(left) == CanonicalProviderID(right)
}
func (c *Config) ProviderNames() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(name string) {
		if _, dup := seen[name]; !dup {
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	for _, tier := range []map[string]any{c.run, c.conversation, c.state, c.file} {
		if x, ok := tier["providers"].(map[string]any); ok {
			for name := range x {
				add(name)
			}
		}
		for key := range tier {
			if rest, ok := strings.CutPrefix(key, "providers."); ok {
				if name, _, ok := strings.Cut(rest, "."); ok {
					add(name)
				}
			}
		}
	}
	return out
}
func (c *Config) Source(k string) source { return c.Lookup(k).Source }
func (c *Config) Bool(k string, fallback bool) bool {
	if v, ok := parseBool(c.Lookup(k).Value); ok {
		return v
	}
	return fallback
}
func (c *Config) AnyBool(k string, fallback bool) bool {
	if isKnown(k) {
		return c.Bool(k, fallback)
	}
	if v, ok := parseBool(c.AnyString(k)); ok {
		return v
	}
	return fallback
}
func parseBool(v string) (bool, bool) {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}

// parseScaled parses numbers with unit suffixes: durations (ms/s/m/h, bare seconds),
// decimal token counts (k = 1000), and binary sizes (k = 1024).
func parseScaled(v string, kind kind) (int64, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return 0, false
	}
	mult := int64(1)
	if kind == durationKind {
		if !strings.HasSuffix(v, "ms") && !strings.HasSuffix(v, "s") && !strings.HasSuffix(v, "m") && !strings.HasSuffix(v, "h") {
			mult = 1000
		}
		for _, x := range []struct {
			s string
			m int64
		}{{"ms", 1}, {"s", 1000}, {"m", 60000}, {"h", 3600000}} {
			if strings.HasSuffix(v, x.s) {
				mult = x.m
				v = strings.TrimSuffix(v, x.s)
				break
			}
		}
	} else if kind == tokensKind {
		if strings.HasSuffix(v, "k") {
			mult = 1000
			v = strings.TrimSuffix(v, "k")
		} else if strings.HasSuffix(v, "m") {
			mult = 1000 * 1000
			v = strings.TrimSuffix(v, "m")
		}
	} else {
		if strings.HasSuffix(v, "k") {
			mult = 1024
			v = strings.TrimSuffix(v, "k")
		} else if strings.HasSuffix(v, "m") {
			mult = 1024 * 1024
			v = strings.TrimSuffix(v, "m")
		}
	}
	v = strings.TrimSpace(v)
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n > (1<<63-1)/mult || n < (-1<<63)/mult {
		return 0, false
	}
	return n * mult, true
}
func (c *Config) typed(k string, kind kind) int64 {
	st := settings[k]
	v, ok := parseScaled(c.Lookup(k).Value, st.kind)
	if !ok || (kind == sizeKind && v <= 0) || (kind != sizeKind && v < 0) || (st.min > 0 && v < st.min) || (st.max > 0 && v > st.max) {
		v, _ = parseScaled(st.defaultValue, st.kind)
	}
	return v
}
func (c *Config) Int(k string) int    { return int(c.typed(k, intKind)) }
func (c *Config) Size(k string) int64 { return c.typed(k, sizeKind) }

// Tokens resolves a decimal-suffixed token count (context_limit).
func (c *Config) Tokens(k string) int64 {
	return c.typed(k, tokensKind)
}
func (c *Config) Duration(k string) time.Duration {
	return time.Duration(c.typed(k, durationKind)) * time.Millisecond
}
func (c *Config) Init() error {
	c.fileUnusable = false
	configErr := c.loadFile(configPath("config.json"), &c.file, &c.fileOrder)
	if configErr != nil {
		c.fileUnusable = true
	}
	stateErr := c.loadFile(StatePath("state.json"), &c.state, &c.stateOrder)
	return errors.Join(configErr, stateErr, c.legacyProviderConfigError())
}
func (c *Config) Load(text string) error { return c.load(text, &c.file, &c.fileOrder) }
func (c *Config) loadFile(path string, out *map[string]any, order *map[string][]string) error {
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		*out = nil
		*order = nil
		return nil
	}
	if e != nil {
		return e
	}
	if len(b) > 1024*1024 {
		return errors.New("configuration exceeds 1 MiB")
	}
	return c.load(string(b), out, order)
}
func normalize(x any) any {
	switch v := x.(type) {
	case map[string]any:
		for k, z := range v {
			v[k] = normalize(z)
		}
		return v
	default:
		return x
	}
}
func (c *Config) load(text string, out *map[string]any, order *map[string][]string) error {
	*out = nil
	*order = nil
	if strings.TrimSpace(text) == "" {
		return nil
	}
	m, orders, err := parseJSON(text)
	if err != nil {
		return err
	}
	*out = normalize(m).(map[string]any)
	*order = orders
	return nil
}
func configPath(name string) string {
	return xdg("XDG_CONFIG_HOME", filepath.Join(home(), ".config"), name)
}

func StatePath(name string) string {
	return xdg("XDG_STATE_HOME", filepath.Join(home(), ".local", "state"), name)
}
func xdg(key, def, name string) string {
	base := os.Getenv(key)
	if base == "" {
		base = def
	}
	return filepath.Join(base, "herbie", name)
}
func home() string {
	h, e := os.UserHomeDir()
	if e != nil {
		return ""
	}
	return h
}
func setNested(m map[string]any, key string, value *string) {
	parts := strings.Split(key, ".")
	for _, p := range parts[:len(parts)-1] {
		n, ok := m[p].(map[string]any)
		if !ok {
			n = map[string]any{}
			m[p] = n
		}
		m = n
	}
	if value == nil {
		delete(m, parts[len(parts)-1])
	} else {
		m[parts[len(parts)-1]] = *value
	}
}
func clone(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	b, _ := json.Marshal(m)
	var r map[string]any
	_ = json.Unmarshal(b, &r)
	return r
}
func completeObjectOrder(value any, path string, orders map[string][]string) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	keys := orderedObjectKeys(object, orders[path])
	orders[path] = keys
	for _, key := range keys {
		childPath := key
		if path != "" {
			childPath = path + "." + key
		}
		completeObjectOrder(object[key], childPath, orders)
	}
}

func writeOrderedJSON(out *bytes.Buffer, value any, path string, orders map[string][]string) error {
	switch value := value.(type) {
	case map[string]any:
		if err := out.WriteByte('{'); err != nil {
			return err
		}
		for i, key := range orderedObjectKeys(value, orders[path]) {
			if i > 0 {
				if err := out.WriteByte(','); err != nil {
					return err
				}
			}
			encodedKey, err := json.Marshal(key)
			if err != nil {
				return err
			}
			if _, err := out.Write(encodedKey); err != nil {
				return err
			}
			if err := out.WriteByte(':'); err != nil {
				return err
			}
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if err := writeOrderedJSON(out, value[key], childPath, orders); err != nil {
				return err
			}
		}
		return out.WriteByte('}')
	case []any:
		if err := out.WriteByte('['); err != nil {
			return err
		}
		for i, child := range value {
			if i > 0 {
				if err := out.WriteByte(','); err != nil {
					return err
				}
			}
			childPath := path + "[]"
			if err := writeOrderedJSON(out, child, childPath, orders); err != nil {
				return err
			}
		}
		return out.WriteByte(']')
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = out.Write(encoded)
		return err
	}
}

func marshalOrderedJSON(value map[string]any, orders map[string][]string) ([]byte, error) {
	var raw bytes.Buffer
	if err := writeOrderedJSON(&raw, value, "", orders); err != nil {
		return nil, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	return append(pretty.Bytes(), '\n'), nil
}

func atomicJSONWithOrder(path string, m map[string]any, orders map[string][]string) error {
	originalPath := path
	for i := range 32 {
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			break
		}
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
		if i == 31 {
			return &os.PathError{Op: "lstat", Path: originalPath, Err: syscall.ELOOP}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, e := marshalOrderedJSON(m, orders)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".tmp.")
	if e != nil {
		return e
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	if e2 := f.Close(); e == nil {
		e = e2
	}
	if e != nil {
		return e
	}
	return os.Rename(name, path)
}
func (c *Config) persist(tier *map[string]any, orders *map[string][]string, path, key string, value *string) error {
	n := clone(*tier)
	delete(n, key)
	setNested(n, key, value)
	nextOrder := cloneOrder(*orders)
	if nextOrder == nil {
		nextOrder = map[string][]string{}
	}
	completeObjectOrder(n, "", nextOrder)
	if e := atomicJSONWithOrder(path, n, nextOrder); e != nil {
		return e
	}
	*tier = n
	*orders = nextOrder
	return nil
}
func (c *Config) Persist(key string, value *string) error {
	if c.fileUnusable {
		return errors.New("config file is unusable")
	}
	return c.persist(&c.file, &c.fileOrder, configPath("config.json"), key, value)
}
func (c *Config) PersistState(key string, value *string) error {
	return c.persist(&c.state, &c.stateOrder, StatePath("state.json"), key, value)
}
func (c *Config) PersistSelection(provider string, model, effort, fast *string) error {
	if provider == "" {
		return errors.New("provider is required")
	}
	n := clone(c.state)
	old, _ := valueAt(c.state, "provider")
	changed := old != provider
	delete(n, "preset")
	n["provider"] = provider
	if model != nil || changed {
		if model == nil {
			x := DefaultValue
			model = &x
		}
		n["model"] = *model
	}
	if effort != nil || changed {
		if effort == nil {
			x := DefaultValue
			effort = &x
		}
		n["effort"] = *effort
	}
	if fast != nil || changed {
		if fast == nil {
			x := DefaultValue
			fast = &x
		}
		n["fast"] = *fast
	}
	nextOrder := cloneOrder(c.stateOrder)
	if nextOrder == nil {
		nextOrder = map[string][]string{}
	}
	completeObjectOrder(n, "", nextOrder)
	if e := atomicJSONWithOrder(StatePath("state.json"), n, nextOrder); e != nil {
		return e
	}
	c.state = n
	c.stateOrder = nextOrder
	return nil
}
func (c *Config) preset(name string) (map[string]any, bool) {
	for _, m := range []map[string]any{c.state, c.file} {
		if p, ok := valueObject(m, "presets", name); ok {
			return p, true
		}
		if p, ok := m["presets."+name].(map[string]any); ok {
			return p, true
		}
	}
	return nil, false
}
func valueObject(m map[string]any, parts ...string) (map[string]any, bool) {
	x, _ := nestedValue(m, parts...)
	q, ok := x.(map[string]any)
	return q, ok
}
func (c *Config) Preset(name string) (presetConfig, error) {
	p, ok := c.preset(name)
	if !ok {
		return presetConfig{}, fmt.Errorf("unknown preset %q", name)
	}
	preset, err := validatePreset(name, p)
	if err != nil {
		return presetConfig{}, err
	}
	for _, k := range []string{"system_prompt", "system_prompt_append"} {
		if v, ok := p[k].(string); ok {
			if _, err := c.ExpandPrompt(v); err != nil {
				return presetConfig{}, err
			}
		}
	}
	return preset, nil
}

func cloneOrder(orders map[string][]string) map[string][]string {
	if orders == nil {
		return nil
	}
	out := maps.Clone(orders)
	for key, values := range out {
		out[key] = slices.Clone(values)
	}
	return out
}

func (c *Config) TaskSelection(selection Selection) *Config {
	next := &Config{
		env:          maps.Clone(c.env),
		file:         clone(c.file),
		state:        clone(c.state),
		fileOrder:    cloneOrder(c.fileOrder),
		stateOrder:   cloneOrder(c.stateOrder),
		run:          map[string]any{},
		fileUnusable: c.fileUnusable,
	}
	next.run["provider"] = selection.Provider
	if selection.Model != "" {
		next.run["model"] = selection.Model
	} else {
		next.run["model"] = DefaultValue
	}
	if selection.EffortSet {
		next.run["effort"] = selection.Effort
	} else {
		next.run["effort"] = DefaultValue
	}
	if selection.FastSet {
		if selection.Fast {
			next.run["fast"] = "on"
		} else {
			next.run["fast"] = "off"
		}
	} else {
		next.run["fast"] = DefaultValue
	}
	return next
}

func (c *Config) TaskPreset(name string) (*Config, Selection, error) {
	preset, err := c.Preset(name)
	if err != nil {
		return nil, Selection{}, err
	}
	selection := Selection{Provider: preset.Provider, Model: preset.Model, Effort: preset.Effort, EffortSet: preset.EffortSet, Fast: preset.Fast, FastSet: preset.FastSet}
	return c.TaskSelection(selection), selection, nil
}

func (c *Config) validatePreset(name string) error {
	_, err := c.Preset(name)
	return err
}

func (c *Config) PresetApply(name string, conversation bool) error {
	p, _ := c.preset(name)
	if err := c.validatePreset(name); err != nil {
		return err
	}
	provider, _ := p["provider"].(string)
	target := &c.run
	if conversation {
		target = &c.conversation
		if *target == nil {
			*target = map[string]any{}
		}
	}
	c.PresetExit(conversation)
	(*target)["provider"] = provider
	for _, k := range []string{"model", "effort", "fast"} {
		if v, ok := p[k].(string); ok {
			(*target)[k] = v
		} else {
			(*target)[k] = DefaultValue
		}
	}
	for _, k := range []string{"system_prompt", "system_prompt_append"} {
		if v, ok := p[k].(string); ok {
			(*target)[k] = v
		}
	}
	delete(*target, "tint")
	(*target)["preset"] = name
	return nil
}
func (c *Config) PresetExit(conversation bool) {
	target := &c.run
	if conversation {
		target = &c.conversation
		if *target == nil {
			*target = map[string]any{}
		}
		(*target)["preset"] = ""
		delete(*target, "system_prompt")
		delete(*target, "system_prompt_append")
		return
	}
	(*target)["preset"] = ""
	delete(*target, "system_prompt")
	delete(*target, "system_prompt_append")
	if c.conversation != nil {
		c.conversation["preset"] = ""
		delete(c.conversation, "system_prompt")
		delete(c.conversation, "system_prompt_append")
	}
}

func (c *Config) RestoreSelection(conversation bool, provider, model, effort string, fast bool, preset string) error {
	target := &c.run
	if conversation {
		target = &c.conversation
		if *target == nil {
			*target = map[string]any{}
		}
	}
	if provider != "" && provider != "none" {
		(*target)["provider"] = provider
		if model != "" {
			(*target)["model"] = model
		} else {
			(*target)["model"] = DefaultValue
		}
		if effort != "" {
			(*target)["effort"] = effort
		} else {
			(*target)["effort"] = DefaultValue
		}
		if fast {
			(*target)["fast"] = "on"
		} else {
			(*target)["fast"] = "off"
		}
	}
	c.PresetExit(conversation)
	if preset != "" {
		return c.PresetApply(preset, conversation)
	}
	return nil
}
func (c *Config) ExpandPrompt(value string) (string, error) {
	if !strings.HasPrefix(value, "@") {
		return value, nil
	}
	spec := strings.TrimPrefix(value, "@")
	if strings.HasPrefix(spec, "~") {
		spec = filepath.Join(home(), strings.TrimPrefix(spec, "~/"))
	} else if !filepath.IsAbs(spec) {
		spec = configPath(spec)
	}
	b, e := os.ReadFile(spec)
	if e != nil {
		return "", fmt.Errorf("couldn't read prompt file %s", spec)
	}
	if len(b) > 64*1024 {
		return "", fmt.Errorf("prompt file %s exceeds 65536 bytes", spec)
	}
	return strings.TrimRight(text.SanitizeUTF8(b), "\r\n"), nil
}
func ValueValid(st setting, v string) bool {
	if v == "" {
		return st.keepEmpty
	}
	if st.Choices != "" {
		if st.Choices == "on|off" {
			_, ok := parseBool(v)
			return ok
		}
		if st.Choices == "auto|on|off" {
			if strings.EqualFold(v, "auto") {
				return true
			}
			_, ok := parseBool(v)
			return ok
		}
		for x := range strings.SplitSeq(st.Choices, "|") {
			if strings.EqualFold(x, v) {
				return true
			}
		}
		if st.kind == stringKind {
			return false
		}
	}
	if st.kind == stringKind {
		return true
	}
	n, ok := parseScaled(v, st.kind)
	if !ok || n < 0 || (st.kind == sizeKind && n == 0) {
		return false
	}
	return (st.min == 0 || n >= st.min) && (st.max == 0 || n <= st.max)
}

// ApplyStartupPreset applies an explicit run/environment preset, or a persisted preset unless
// another selection environment variable makes that stance inapplicable.
func (c *Config) ApplyStartupPreset(explicitSelection bool) error {
	v := c.Lookup("preset")
	if v.Value == "" {
		return nil
	}
	if explicitSelection && v.Source != SourceRun {
		return nil
	}
	return c.PresetApply(v.Value, false)
}
