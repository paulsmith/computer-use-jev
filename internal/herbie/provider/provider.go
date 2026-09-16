// Package provider defines provider-independent conversation and streaming contracts.
package provider

import (
	"context"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"
)

const (
	ImageRequestBase64BudgetBytes = 20 * 1024 * 1024
	ImageRequestMaxCount          = 20
	CatalogTiersMax               = 4
	effortMaxLevels               = 10
	effortMaxLen                  = 16
)

type ItemKind int

const (
	ItemUserMessage ItemKind = iota
	ItemAssistantMessage
	ItemToolCall
	ItemToolResult
	ItemReasoning
	ItemTurnBoundary
	ItemTurnUsage
	ItemServerTool
)

type ItemOrigin int

const (
	ItemOriginNone ItemOrigin = iota
	ItemOriginCompactSeed
	ItemOriginContinuation
	ItemOriginInterrupted
	ItemOriginSkipped
	ItemOriginRefused
	ItemOriginSummarized
	ItemOriginTaskNote
)

type ItemImage struct {
	MIME    string
	DataB64 string
	Width   int64
	Height  int64
}

type Citation struct {
	Title string
	URL   string
	JSON  string
}

type Item struct {
	Kind              ItemKind
	Text              string
	TextJSON          string
	CallID            string
	ToolName          string
	ToolArgumentsJSON string
	Output            string
	OutputHiddenTail  int
	Images            []ItemImage
	ReasoningJSON     string
	ReasoningText     string
	ServerToolJSON    string
	ServerToolQueries []string
	Citations         []Citation
	Provider          string
	Model             string
	Origin            ItemOrigin
	Usage             *TurnUsage
}

func ImagePlaceholder(image ItemImage) string {
	bytes := len(image.DataB64) / 4 * 3
	var size string
	switch {
	case bytes >= 1024*1024:
		size = fmt.Sprintf("%.1f MiB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		size = fmt.Sprintf("%.1f KiB", float64(bytes)/1024)
	default:
		size = fmt.Sprintf("%d bytes", bytes)
	}
	mime := image.MIME
	if mime == "" {
		mime = "image"
	}
	if image.Width > 0 && image.Height > 0 {
		return fmt.Sprintf("[image: %s, %dx%d, %s]", mime, image.Width, image.Height, size)
	}
	return fmt.Sprintf("[image: %s, %s]", mime, size)
}

func ItemsImageBase64Bytes(items []Item) int {
	total := 0
	for _, item := range items {
		for _, image := range item.Images {
			total += len(image.DataB64)
		}
	}
	return total
}

func ItemsImageCount(items []Item) int {
	total := 0
	for _, item := range items {
		total += len(item.Images)
	}
	return total
}

// ExtractQueries keeps native-provider query presentation consistent.
func ExtractQueries(value any) []string {
	var out []string
	var walk func(any, bool)
	walk = func(value any, collect bool) {
		switch value := value.(type) {
		case map[string]any:
			for _, key := range []string{"query", "queries"} {
				if child, ok := value[key]; ok {
					walk(child, true)
				}
			}
			for _, key := range slices.Sorted(maps.Keys(value)) {
				if key == "query" || key == "queries" {
					continue
				}
				walk(value[key], false)
			}
		case []any:
			for _, child := range value {
				walk(child, collect)
			}
		case string:
			if !collect || value == "" {
				return
			}
			if slices.Contains(out, value) {
				return
			}
			out = append(out, value)
		}
	}
	walk(value, false)
	return out
}

func ItemsContextFloor(items []Item) int {
	for i, item := range slices.Backward(items) {
		if item.Kind == ItemUserMessage && item.Origin == ItemOriginCompactSeed {
			return i
		}
	}
	return 0
}

type ToolParam struct {
	Name, Type, ItemType, Description string
	Required                          bool
	Minimum                           int64 // Zero means omitted, matching the C schema contract.
}

type ToolDef struct {
	Name, Description string
	Params            []ToolParam
}

type ServerToolDef struct {
	Name string
	Type string
}

type ServerToolProvider interface {
	ServerTools(model string) []ServerToolDef
}

type Context struct {
	// CacheKey is a stable per-session prompt-cache key; empty disables session scoping.
	CacheKey     string
	SystemPrompt string
	Items        []Item
	Tools        []ToolDef
	ServerTools  []ServerToolDef
	Effort       string
	Fast         bool
	ImageInput   int // 1 yes, 0 no, -1 unknown
}

// NewStreamUsage initializes every provider-reported usage field to its unreported (-1) sentinel.
func NewStreamUsage() StreamUsage {
	return StreamUsage{InputTokens: -1, OutputTokens: -1, CachedTokens: -1, CacheWriteTokens: -1, CacheWrite1HTokens: -1, Cost: -1}
}

// StreamUsage is provider-reported accounting. A negative count or cost is unreported.
type StreamUsage struct {
	InputTokens, OutputTokens, CachedTokens, CacheWriteTokens, CacheWrite1HTokens int64
	Cost                                                                          float64
	Fast                                                                          *bool
}

// Add accumulates reported usage from extra. Reported input or output tokens without a cost make the total cost unknown.
func (u *StreamUsage) Add(extra StreamUsage) {
	unpriced := u.Cost < 0 && (u.InputTokens >= 0 || u.OutputTokens >= 0) || extra.Cost < 0 && (extra.InputTokens >= 0 || extra.OutputTokens >= 0)
	for _, pair := range [][2]*int64{
		{&u.InputTokens, &extra.InputTokens},
		{&u.OutputTokens, &extra.OutputTokens},
		{&u.CachedTokens, &extra.CachedTokens},
		{&u.CacheWriteTokens, &extra.CacheWriteTokens},
		{&u.CacheWrite1HTokens, &extra.CacheWrite1HTokens},
	} {
		if *pair[1] >= 0 {
			if *pair[0] < 0 {
				*pair[0] = 0
			}
			*pair[0] += *pair[1]
		}
	}
	if extra.Fast != nil {
		fast := *extra.Fast
		u.Fast = &fast
	}
	if unpriced {
		u.Cost = -1
	} else if extra.Cost >= 0 {
		if u.Cost < 0 {
			u.Cost = 0
		}
		u.Cost += extra.Cost
	}
}

// StreamUsageError is optionally implemented by returned stream errors that carry usage.
type StreamUsageError interface {
	error
	StreamUsage() StreamUsage
}

// streamUsageError wraps an error with provider-reported usage.
type streamUsageError struct {
	err   error
	usage StreamUsage
}

func (e streamUsageError) Error() string            { return e.err.Error() }
func (e streamUsageError) Unwrap() error            { return e.err }
func (e streamUsageError) StreamUsage() StreamUsage { return e.usage }

// WithStreamUsage attaches provider-reported usage to err and preserves errors.As traversal.
func WithStreamUsage(err error, usage StreamUsage) error {
	if err == nil {
		return nil
	}
	return streamUsageError{err: err, usage: usage}
}

// StreamResponseError is optionally implemented by returned stream errors that carry response identity.
type StreamResponseError interface {
	error
	StreamResponse() StreamResponse
}

// StreamResponse is the provider-reported identity of one response. Empty fields are unreported.
type StreamResponse struct {
	ID, Model, Route string
}

// streamResponseError wraps an error with a StreamResponse so callers can recover
// the response identity of a failed or completed stream.
type streamResponseError struct {
	err      error
	response StreamResponse
}

func (e streamResponseError) Error() string                  { return e.err.Error() }
func (e streamResponseError) Unwrap() error                  { return e.err }
func (e streamResponseError) StreamResponse() StreamResponse { return e.response }

// WithStreamResponse attaches response identity to err when it is non-empty and
// returns err unchanged otherwise.
func WithStreamResponse(err error, response *StreamResponse) error {
	if err == nil || response == nil || response.ID == "" && response.Model == "" && response.Route == "" {
		return err
	}
	return streamResponseError{err: err, response: *response}
}

// ReportedCost parses a cost value reported in an SSE wire map. It accepts a
// JSON number or a numeric string and rejects negative, infinite, and NaN values.
func ReportedCost(value any) (float64, bool) {
	var cost float64
	switch value := value.(type) {
	case float64:
		cost = value
	case string:
		var err error
		cost, err = strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return cost, cost >= 0 && !math.IsInf(cost, 0) && !math.IsNaN(cost)
}

type TurnUsage struct {
	Usage               StreamUsage
	Elapsed             time.Duration
	UncachedInputTokens int64
	CostInput           float64
	CostCacheRead       float64
	CostCacheWrite      float64
	CostOutput          float64
	CostTotal           float64
	CostEstimated       bool
	Fast                bool
	ProviderLabel       string
	ModelLabel          string
	Effort              string
	ServedModel         string
	Route               string
	ResponseID          string
}

type StreamEventKind int

const (
	EventTextDelta StreamEventKind = iota
	EventToolCallStart
	EventToolCallDelta
	EventToolCallEnd
	EventReasoningItem
	EventReasoningDelta
	EventRetry
	EventProgress
	EventDone
	EventError
	EventServerTool
	EventTextItem
)

// StreamEvent payload fields are meaningful only for their corresponding Kind.
type StreamEvent struct {
	Kind      StreamEventKind
	Text      string
	ArgsDelta string
	// ID is nil when absent; a non-nil pointer to "" is a valid empty tool ID.
	ID   *string
	Name string
	JSON string
	// ReasoningPartBreak marks a display-only hard break between streamed reasoning parts.
	ReasoningPartBreak bool
	// ReasoningSummary marks user-displayable summary text rather than ordinary reasoning.
	ReasoningSummary bool
	Queries          []string
	Citations        []Citation

	Attempt, MaxAttempts, HTTPStatus int
	Delay                            time.Duration
	Processed, Total, Cache          int64
	StopReason                       string
	Usage                            *StreamUsage
	Response                         *StreamResponse
	Message                          string
}

type ProviderCap int

const (
	ProviderCapUnknown ProviderCap = iota
	ProviderCapYes
	ProviderCapNo
)

// Rates are per-million-token rates. A negative rate is unreported.
type Rates struct {
	CostInput, CostOutput, CostCacheRead, CostCacheWrite, CostCacheWrite1H float64
}

func NewRates() Rates {
	return Rates{CostInput: -1, CostOutput: -1, CostCacheRead: -1, CostCacheWrite: -1, CostCacheWrite1H: -1}
}

func (r Rates) Known() bool {
	return r.CostInput >= 0 || r.CostOutput >= 0 || r.CostCacheRead >= 0 || r.CostCacheWrite >= 0 || r.CostCacheWrite1H >= 0
}

// Merge fills unreported rates from src.
func (r *Rates) Merge(src Rates) {
	for _, pair := range []struct{ dst, src *float64 }{
		{&r.CostInput, &src.CostInput},
		{&r.CostOutput, &src.CostOutput},
		{&r.CostCacheRead, &src.CostCacheRead},
		{&r.CostCacheWrite, &src.CostCacheWrite},
		{&r.CostCacheWrite1H, &src.CostCacheWrite1H},
	} {
		if *pair.dst < 0 {
			*pair.dst = *pair.src
		}
	}
}

type CatalogTier struct {
	ContextThreshold int64
	Rates
}

type EffortSet struct {
	Values []string
	Known  bool
}

type ReasoningHint struct {
	Field    string
	Declared bool
}

type ModelInfo struct {
	ID, Description string
	API             string
	// MaxContext is the provider-declared override ceiling for the normal Context; zero means no ceiling.
	Context, MaxContext, MaxOutput int64
	ImageInput, Tools              ProviderCap
	FastMode                       ProviderCap
	Rates
	Tiers                []CatalogTier
	Efforts              EffortSet
	InterleavedReasoning ReasoningHint
}

func NewModelInfo() ModelInfo { return ModelInfo{Rates: NewRates()} }

func (info ModelInfo) Clone() ModelInfo {
	info.Tiers = slices.Clone(info.Tiers)
	info.Efforts.Values = slices.Clone(info.Efforts.Values)
	return info
}

type ModelProbe struct {
	URL     string
	Headers http.Header
	Timeout time.Duration
	Parse   func(body, model string, out *ModelInfo)
}

type ProviderAvailability struct {
	Available bool
	Reason    string
	URL       string
	Headers   http.Header
	Timeout   time.Duration
}

// StreamCallback receives events in provider order. Returning a non-nil error aborts the stream.
type StreamCallback func(StreamEvent) error

// TickFunc pumps UI state periodically and when stream data arrives.
type TickFunc func()

// TraceSink receives provider transport activity.
type TraceSink interface {
	Request(string, string, http.Header, []byte)
	Response(int, []byte)
	SSE(string, []byte)
}

// Provider is the core contract required to run a model stream. Name returns its stable registry ID.
// DefaultModel returns "" when no safe default exists: only values derived from live state (server
// discovery, a companion tool's config) qualify; compiled-in model names go stale in shipped binaries.
type Provider interface {
	Name() string
	DefaultModel() string
	DefaultEffort() string
	Stream(context.Context, Context, string, StreamCallback, TickFunc) error
}

// FastModeProvider is implemented only by providers with a first-party fast request mode.
type FastModeProvider interface {
	ValidateFastMode(string) error
	FastModeRates(string, ModelInfo) ModelInfo
}

// ValidateFastMode rejects enabled fast mode before an unsupported request reaches the wire.
func ValidateFastMode(p Provider, model string, enabled bool) error {
	if !enabled {
		return nil
	}
	fast, ok := p.(FastModeProvider)
	if !ok {
		name := "provider"
		if p != nil && DisplayName(p) != "" {
			name = DisplayName(p)
		}
		return fmt.Errorf("%s does not support fast mode", name)
	}
	return fast.ValidateFastMode(model)
}

// ModelLister is implemented by providers that can enumerate models.
type ModelLister interface {
	ListModels(context.Context, TickFunc) ([]ModelInfo, error)
}

// EffortLister is implemented by providers with a fixed reasoning-effort ladder.
type EffortLister interface {
	ListEfforts() []string
}

// ModelProber is implemented by providers that can fetch metadata for one model.
type ModelProber interface {
	ProbeModel(context.Context, string) (ModelProbe, error)
}

// UsageQuerier is implemented by providers that can display account usage.
type UsageQuerier interface {
	QueryUsage(out io.Writer) error
}

// Traceable is implemented by providers whose transport trace can be replaced.
type Traceable interface {
	SetTrace(TraceSink)
}

// Destroyer is implemented by providers that hold resources requiring explicit release.
type Destroyer interface {
	Destroy()
}

// DisplayName returns the provider's user-facing label, falling back to its stable name.
func DisplayName(p Provider) string {
	if p == nil {
		return ""
	}
	if display, ok := p.(interface{ DisplayName() string }); ok {
		if name := display.DisplayName(); name != "" {
			return name
		}
	}
	return p.Name()
}

// CanonicalProviderID maps selection aliases to the stable provider identity.
func CanonicalProviderID(name string) string {
	if name == "llama.cpp" {
		return "llamacpp"
	}
	return name
}

func ProviderIDsEqual(left, right string) bool {
	return CanonicalProviderID(left) == CanonicalProviderID(right)
}

// ModelLabeler is implemented by providers with model-specific display labels.
type ModelLabeler interface {
	ModelLabel(string) string
}

// Metadata is mutable runtime metadata shared with model resolution.
type Metadata struct {
	CatalogID       string
	ModelDiscovered bool
	KeepModelOrder  bool

	mu    sync.RWMutex
	model *ModelInfo
}

// Model returns a private snapshot of the current model metadata.
func (m *Metadata) Model() *ModelInfo {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.model == nil {
		return nil
	}
	model := m.model.Clone()
	return &model
}

// SetModel publishes a private snapshot of model metadata.
func (m *Metadata) SetModel(model *ModelInfo) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if model == nil {
		m.model = nil
		return
	}
	copy := model.Clone()
	m.model = &copy
}

// MetadataProvider is implemented by providers that expose model and catalog metadata.
type MetadataProvider interface {
	Metadata() *Metadata
}

// ProviderFactory constructs providers with a registered name.
type ProviderFactory interface {
	Name() string
	New(string) Provider
}

// AvailabilityPreparer reports whether a factory can be selected automatically.
type AvailabilityPreparer interface {
	PrepareAvailability(string) ProviderAvailability
}

// InternalFactory marks factories omitted from user-facing provider lists.
type InternalFactory interface {
	Internal() bool
}

// Add records level unless it is invalid, duplicated, or the fixed C-compatible capacity is full.
func (s *EffortSet) Add(level string) bool {
	s.Known = true
	if level == "" || len(level) >= effortMaxLen || len(s.Values) >= effortMaxLevels || s.Has(level) {
		return false
	}
	s.Values = append(s.Values, level)
	return true
}

func (s EffortSet) Has(level string) bool {
	if !s.Known {
		return false
	}
	return slices.Contains(s.Values, level)
}

// Clamp returns a supported requested effort, or the nearest supported level. It returns "" when
// the set is empty or requested is not a recognized wire effort.
func (s EffortSet) Clamp(requested string) string {
	if !s.Known || len(s.Values) == 0 || requested == "" {
		return ""
	}
	// Providers may advertise custom effort names. An exact offered value must
	// round-trip even when it has no position in the standard effort ordering.
	if s.Has(requested) {
		return requested
	}
	order := map[string]int{"none": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5, "max": 6}
	rank, ok := order[requested]
	if !ok {
		return ""
	}
	bestLower, bestHigher := "", ""
	for _, value := range s.Values {
		valueRank, known := order[value]
		if !known {
			continue
		}
		if valueRank <= rank && (bestLower == "" || order[bestLower] < valueRank) {
			bestLower = value
		}
		if valueRank > rank && (bestHigher == "" || order[bestHigher] > valueRank) {
			bestHigher = value
		}
	}
	if bestLower != "" {
		return bestLower
	}
	return bestHigher
}
