package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/paulsmith/computeruser/internal/herbie/provider"
	"github.com/paulsmith/computeruser/typesafe"
)

type fakeProvider struct {
	output  string
	err     error
	calls   int
	request provider.Context
}

func (p *fakeProvider) Name() string          { return "fake" }
func (p *fakeProvider) DefaultModel() string  { return "fake-model" }
func (p *fakeProvider) DefaultEffort() string { return "" }
func (p *fakeProvider) Stream(ctx context.Context, c provider.Context, model string, cb provider.StreamCallback, _ provider.TickFunc) error {
	p.calls++
	p.request = c
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.err != nil {
		return p.err
	}
	if err := cb(provider.StreamEvent{Kind: provider.EventTextDelta, Text: p.output}); err != nil {
		return err
	}
	return cb(provider.StreamEvent{Kind: provider.EventDone})
}
func classifier(t *testing.T, probability float64) *typesafe.Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			State     map[string]string            `json:"state"`
			Questions map[string]typesafe.Question `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.State["goal"] == "" || req.Questions["multiple"].Type != "noul" {
			t.Error("missing goal or multiple-instruction question")
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"multiple": map[string]any{"type": "noul", "noul": probability}}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(s.Close)
	return typesafe.New("test", typesafe.WithEndpoint(s.URL))
}
func TestSingleGoalDoesNotRequireLLM(t *testing.T) {
	t.Setenv("COMPUTER_USE_PROVIDER", "")
	t.Setenv("COMPUTER_USE_MODEL", "")
	goal := `type "bread and butter"`
	got, err := Plan(context.Background(), classifier(t, 0.01), goal)
	if err != nil || !reflect.DeepEqual(got, []string{goal}) {
		t.Fatalf("%v, %v", got, err)
	}
}
func TestCompoundUsesConfiguredSplitter(t *testing.T) {
	p := &fakeProvider{output: `["Activate TextEdit","Select all text in TextEdit","Make the selected text bold in TextEdit"]`}
	got, err := plan(context.Background(), classifier(t, 0.99), "open textedit select the text and make it bold", func() (provider.Provider, string, error) { return p, "model", nil })
	if err != nil || len(got) != 3 || p.calls != 1 {
		t.Fatalf("%v, %v, calls=%d", got, err, p.calls)
	}
	if len(p.request.Tools) != 0 || !strings.Contains(p.request.SystemPrompt, "literal") {
		t.Fatal("splitter must preserve literal text and have no tools")
	}
}
func TestPlanningFailsBeforeExecution(t *testing.T) {
	for _, prob := range []float64{0.5, -0.1, 1.1} {
		_, err := plan(context.Background(), classifier(t, prob), "goal", func() (provider.Provider, string, error) {
			t.Fatal("LLM loaded on uncertain/invalid judgment")
			return nil, "", nil
		})
		if err == nil {
			t.Fatalf("probability %v accepted", prob)
		}
	}
	t.Setenv("COMPUTER_USE_PROVIDER", "")
	t.Setenv("COMPUTER_USE_MODEL", "")
	if _, err := Plan(context.Background(), classifier(t, 0.99), "select text and make bold"); err == nil {
		t.Fatal("missing configuration accepted")
	}
}
func TestValidateSplit(t *testing.T) {
	for _, s := range []string{`[]`, `null`, `["one"]`, `["one",""]`, `["one",null]`, `["one",2]`, `{"commands":["one","two"]}`, "```json\n[\"one\",\"two\"]\n```", `["one","two"] {}`, `["one","two"] garbage`, strings.Repeat("x", maxPlanBytes+1)} {
		if _, err := parseSplit(s); err == nil {
			t.Errorf("accepted %q", s[:min(len(s), 80)])
		}
	}
	if _, err := parseSplit(`["one","two"]`); err != nil {
		t.Fatal(err)
	}
	tooMany := make([]string, maxInstructions+1)
	for i := range tooMany {
		tooMany[i] = "instruction"
	}
	b, _ := json.Marshal(tooMany)
	if _, err := parseSplit(string(b)); err == nil {
		t.Fatal("unbounded instruction count")
	}
}
func TestSplitterFailureAndCancellation(t *testing.T) {
	p := &fakeProvider{err: errors.New("provider failed")}
	if _, err := split(context.Background(), p, "model", "goal"); err == nil {
		t.Fatal("provider failure ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := split(ctx, &fakeProvider{}, "model", "goal"); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}
func TestProviderEnvironment(t *testing.T) {
	for _, tc := range []struct{ name, key string }{{"openai", "OPENAI_API_KEY"}, {"anthropic", "ANTHROPIC_API_KEY"}, {"openrouter", "OPENROUTER_API_KEY"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COMPUTER_USE_PROVIDER", tc.name)
			t.Setenv("COMPUTER_USE_MODEL", "chosen-model")
			t.Setenv(tc.key, "")
			if _, _, err := fromEnv(); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("missing key: %v", err)
			}
			t.Setenv(tc.key, "test-secret")
			p, m, err := fromEnv()
			if err != nil || p.Name() != tc.name || m != "chosen-model" {
				t.Fatalf("%v %s %v", p, m, err)
			}
		})
	}
	t.Setenv("COMPUTER_USE_PROVIDER", "lunaroute")
	t.Setenv("COMPUTER_USE_MODEL", "model")
	t.Setenv("LUNAROUTE_API_KEY", "test-secret")
	if _, _, err := fromEnv(); err == nil || !strings.Contains(err.Error(), "unsupported COMPUTER_USE_PROVIDER") {
		t.Fatalf("removed provider accepted: %v", err)
	}
	t.Setenv("COMPUTER_USE_PROVIDER", "unknown")
	t.Setenv("COMPUTER_USE_MODEL", "model")
	if _, _, err := fromEnv(); err == nil {
		t.Fatal("unknown provider accepted")
	}
	t.Setenv("COMPUTER_USE_PROVIDER", "openai")
	t.Setenv("COMPUTER_USE_MODEL", "")
	if _, _, err := fromEnv(); err == nil {
		t.Fatal("missing model accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCopiedProvidersStreamPlanWithExpectedCredentials(t *testing.T) {
	for _, tc := range []struct{ name, key, path, host, baseURL string }{
		{"openai", "OPENAI_API_KEY", "/v1/responses", "api.openai.com", ""},
		{"openai", "OPENAI_API_KEY", "/custom/v1/responses", "proxy.example", "https://proxy.example/custom/v1"},
		{"openai", "OPENAI_API_KEY", "/v1/responses", "api.openai.com", "   "},
		{"anthropic", "ANTHROPIC_API_KEY", "/v1/messages", "api.anthropic.com", "https://ignored.example/v1"},
		{"openrouter", "OPENROUTER_API_KEY", "/api/v1/chat/completions", "openrouter.ai", "https://ignored.example/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COMPUTER_USE_PROVIDER", tc.name)
			t.Setenv("COMPUTER_USE_MODEL", "chosen-model")
			t.Setenv(tc.key, "test-secret")
			t.Setenv("OPENAI_BASE_URL", tc.baseURL)
			old := http.DefaultClient
			http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != tc.host || r.URL.Path != tc.path {
					t.Errorf("wrong endpoint: %s", r.URL)
				}
				header := "Authorization"
				want := "Bearer test-secret"
				if tc.name == "anthropic" {
					header = "x-api-key"
					want = "test-secret"
				}
				if r.Header.Get(header) != want {
					t.Errorf("wrong %s", header)
				}
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				if req["model"] != "chosen-model" {
					t.Errorf("wrong model: %v", req["model"])
				}
				if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
					t.Fatal("splitter received tools")
				}
				var events []any
				switch tc.name {
				case "openai":
					events = []any{map[string]any{"type": "response.output_text.delta", "delta": "[\"one\",\"two\"]"}, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}}}
				case "anthropic":
					events = []any{
						map[string]any{"type": "message_start", "message": map[string]any{"id": "m1", "model": "chosen-model"}},
						map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
						map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "[\"one\",\"two\"]"}},
						map[string]any{"type": "content_block_stop", "index": 0},
						map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}},
						map[string]any{"type": "message_stop"},
					}
				default:
					events = []any{map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "[\"one\",\"two\"]"}, "finish_reason": "stop"}}}}
				}
				var stream strings.Builder
				for _, e := range events {
					b, err := json.Marshal(e)
					if err != nil {
						t.Fatal(err)
					}
					stream.WriteString("data: " + string(b) + "\n\n")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream.String())), Request: r}, nil
			})}
			t.Cleanup(func() { http.DefaultClient = old })
			p, m, err := fromEnv()
			if err != nil {
				t.Fatal(err)
			}
			got, err := split(context.Background(), p, m, "one then two")
			if err != nil || fmt.Sprint(got) != "[one two]" {
				t.Fatalf("%v, %v", got, err)
			}
		})
	}
}
