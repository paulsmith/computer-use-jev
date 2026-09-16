package provider

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestItemImageHelpers(t *testing.T) {
	items := []Item{{Kind: ItemToolResult, Images: []ItemImage{{MIME: "image/png", DataB64: "aaaa"}, {DataB64: "bb"}}}, {Kind: ItemAssistantMessage, Images: []ItemImage{{DataB64: "ccc"}}}}
	if got := ItemsImageBase64Bytes(items); got != 9 {
		t.Fatalf("bytes = %d, want 9", got)
	}
	if got := ItemsImageCount(items); got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}
	if got := ImagePlaceholder(ItemImage{MIME: "image/png", DataB64: "AAAA", Width: 640, Height: 480}); got != "[image: image/png, 640x480, 3 bytes]" {
		t.Fatal(got)
	}
	if got := ImagePlaceholder(ItemImage{}); got != "[image: image, 0 bytes]" {
		t.Fatal(got)
	}
}

func TestExtractQueries(t *testing.T) {
	value := map[string]any{
		"query":   "one",
		"nested":  map[string]any{"queries": []any{"two", "one", ""}},
		"ignored": []any{"not a query"},
	}
	got := ExtractQueries(value)
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("queries = %#v", got)
	}
}

func TestItemsContextFloor(t *testing.T) {
	items := []Item{{Kind: ItemUserMessage, Origin: ItemOriginCompactSeed}, {Kind: ItemAssistantMessage}, {Kind: ItemUserMessage, Origin: ItemOriginCompactSeed}}
	if got := ItemsContextFloor(items); got != 2 {
		t.Fatalf("floor = %d", got)
	}
	if got := ItemsContextFloor(items[1:2]); got != 0 {
		t.Fatalf("floor = %d", got)
	}
}

func TestModelInfoLifecycle(t *testing.T) {
	info := NewModelInfo()
	if info.CostInput >= 0 || info.CostCacheRead >= 0 || info.CostOutput >= 0 || info.CostCacheWrite >= 0 || info.CostCacheWrite1H >= 0 {
		t.Fatalf("unknown costs: %#v", info)
	}
	info.ID, info.Description = "model", "description"
	info.Tiers = []CatalogTier{{ContextThreshold: 100}}
	info.Efforts.Values = []string{"low"}
	clone := info.Clone()
	clone.Tiers[0].ContextThreshold = 200
	clone.Efforts.Values[0] = "high"
	if info.Tiers[0].ContextThreshold != 100 || info.Efforts.Values[0] != "low" {
		t.Fatal("clone aliases mutable slices")
	}
}

func TestStreamUsageUnreported(t *testing.T) {
	u := NewStreamUsage()
	if u.InputTokens != -1 || u.OutputTokens != -1 || u.CachedTokens != -1 || u.CacheWriteTokens != -1 || u.CacheWrite1HTokens != -1 || u.Cost != -1 {
		t.Fatalf("unreported usage = %#v", u)
	}
}

func TestStreamUsageAdd(t *testing.T) {
	fast := true
	for _, tc := range []struct {
		name  string
		total StreamUsage
		extra StreamUsage
		want  StreamUsage
	}{
		{
			name: "both priced",
			total: StreamUsage{
				InputTokens: 1, OutputTokens: 2, CachedTokens: 3, CacheWriteTokens: 4, CacheWrite1HTokens: 5, Cost: .25,
			},
			extra: StreamUsage{
				InputTokens: 6, OutputTokens: 7, CachedTokens: 8, CacheWriteTokens: 9, CacheWrite1HTokens: 10, Cost: .75, Fast: &fast,
			},
			want: StreamUsage{
				InputTokens: 7, OutputTokens: 9, CachedTokens: 11, CacheWriteTokens: 13, CacheWrite1HTokens: 15, Cost: 1, Fast: &fast,
			},
		},
		{
			name: "partial without cost",
			total: StreamUsage{
				InputTokens: 1, OutputTokens: -1, CachedTokens: -1, CacheWriteTokens: -1, CacheWrite1HTokens: -1, Cost: .25,
			},
			extra: StreamUsage{
				InputTokens: 2, OutputTokens: -1, CachedTokens: -1, CacheWriteTokens: -1, CacheWrite1HTokens: -1, Cost: -1,
			},
			want: StreamUsage{
				InputTokens: 3, OutputTokens: -1, CachedTokens: -1, CacheWriteTokens: -1, CacheWrite1HTokens: -1, Cost: -1,
			},
		},
		{
			name:  "empty extra",
			total: StreamUsage{InputTokens: 1, OutputTokens: -1, CachedTokens: -1, CacheWriteTokens: -1, CacheWrite1HTokens: -1, Cost: .25},
			extra: NewStreamUsage(),
			want:  StreamUsage{InputTokens: 1, OutputTokens: -1, CachedTokens: -1, CacheWriteTokens: -1, CacheWrite1HTokens: -1, Cost: .25},
		},
		{
			name: "total unpriced with priced extra",
			total: StreamUsage{
				InputTokens: 1, OutputTokens: 2, CachedTokens: -1, CacheWriteTokens: -1, CacheWrite1HTokens: -1, Cost: -1,
			},
			extra: StreamUsage{
				InputTokens: 6, OutputTokens: 7, CachedTokens: 8, CacheWriteTokens: 9, CacheWrite1HTokens: 10, Cost: .75, Fast: &fast,
			},
			want: StreamUsage{
				InputTokens: 7, OutputTokens: 9, CachedTokens: 8, CacheWriteTokens: 9, CacheWrite1HTokens: 10, Cost: -1, Fast: &fast,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total := tc.total
			total.Add(tc.extra)
			if total.InputTokens != tc.want.InputTokens || total.OutputTokens != tc.want.OutputTokens || total.CachedTokens != tc.want.CachedTokens || total.CacheWriteTokens != tc.want.CacheWriteTokens || total.CacheWrite1HTokens != tc.want.CacheWrite1HTokens || total.Cost != tc.want.Cost {
				t.Fatalf("usage = %#v, want %#v", total, tc.want)
			}
			if (total.Fast == nil) != (tc.want.Fast == nil) || total.Fast != nil && *total.Fast != *tc.want.Fast {
				t.Fatalf("fast = %v, want %v", total.Fast, tc.want.Fast)
			}
		})
	}
}

func TestStreamEventJSONKeepsMillisecondRetryDelay(t *testing.T) {
	data, err := json.Marshal(StreamEvent{Kind: EventRetry, Attempt: 2, MaxAttempts: 3, HTTPStatus: 429, Delay: 1500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"Kind":6,"Text":"","ArgsDelta":"","ID":null,"Name":"","JSON":"","Attempt":2,"MaxAttempts":3,"HTTPStatus":429,"DelayMS":1500,"Processed":0,"Total":0,"Cache":0,"StopReason":"","Usage":null,"Response":null,"Message":""}`
	if string(data) != want {
		t.Fatalf("event JSON = %s", data)
	}
}

func TestStreamEventJSONReasoningSummary(t *testing.T) {
	data, err := json.Marshal(StreamEvent{
		Kind:             EventReasoningDelta,
		Text:             "checking",
		ReasoningSummary: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"ReasoningSummary":true`)) {
		t.Fatalf("event JSON = %s", data)
	}

	data, err = json.Marshal(StreamEvent{Kind: EventReasoningDelta, Text: "checking"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("ReasoningSummary")) {
		t.Fatalf("false summary metadata was not omitted: %s", data)
	}
}

func TestEffortSet(t *testing.T) {
	var efforts EffortSet
	if efforts.Add("") || !efforts.Known {
		t.Fatal("empty level must only mark set known")
	}
	if !efforts.Add("low") || efforts.Add("low") || !efforts.Add("high") {
		t.Fatal("add behavior")
	}
	if got := efforts.Clamp("medium"); got != "low" {
		t.Fatalf("clamp = %q", got)
	}
	if got := efforts.Clamp("none"); got != "low" {
		t.Fatalf("clamp upward = %q", got)
	}
	if !efforts.Add("vendor-custom") {
		t.Fatal("add custom effort")
	}
	if got := efforts.Clamp("vendor-custom"); got != "vendor-custom" {
		t.Fatalf("custom exact clamp = %q", got)
	}
	if got := efforts.Clamp("unknown"); got != "" {
		t.Fatalf("unknown clamp = %q", got)
	}
}
