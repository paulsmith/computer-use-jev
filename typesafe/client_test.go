package typesafe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAskRequestShape(t *testing.T) {
	var got request
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		io.WriteString(w, `{"model":"jev-1.13.0","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	c := New("k-123", WithEndpoint(srv.URL), WithModel("jev-1.12"))
	state := map[string]any{"apps": []string{"Finder"}}
	questions := map[string]Question{
		"action": Choice("which action next?", map[string]string{"apps": "list apps", "done": ""}),
		"target": Noul("is a target selected?", map[string]string{"true": "yes", "false": "no"}),
		"amount": Score("how much?", []string{"none", "some", "lots"}),
	}
	if _, err := c.Ask(context.Background(), state, questions); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if gotAuth != "Bearer k-123" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if got.Model != "jev-1.12" {
		t.Errorf("model = %q, want jev-1.12", got.Model)
	}
	if !jsonEqual(t, got.State, any(state)) {
		t.Errorf("state = %#v, want %#v", got.State, state)
	}
	if len(got.Questions) != 3 {
		t.Fatalf("questions = %#v", got.Questions)
	}
	if q := got.Questions["action"]; q.Type != TypeChoice || q.Instructions != "which action next?" {
		t.Errorf("action question = %#v", q)
	}
	if q := got.Questions["target"]; q.Type != TypeNoul {
		t.Errorf("target question = %#v", q)
	}
	if q := got.Questions["amount"]; q.Type != TypeScore {
		t.Errorf("amount question = %#v", q)
	}
	levels, ok := got.Questions["amount"].Criteria.([]any)
	if !ok || len(levels) != 3 || levels[0] != "none" {
		t.Errorf("score criteria = %#v", got.Questions["amount"].Criteria)
	}
}

func TestAskStringStatePassthrough(t *testing.T) {
	var got request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"answers":{}}`)
	}))
	defer srv.Close()

	c := New("k", WithEndpoint(srv.URL))
	if _, err := c.Ask(context.Background(), "goal: open TextEdit", map[string]Question{
		"q": Noul("done?", map[string]string{"true": "yes", "false": "no"}),
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got.State != "goal: open TextEdit" {
		t.Errorf("state = %#v", got.State)
	}
}

func TestAskDefaultOptions(t *testing.T) {
	c := New("k")
	if c.model != defaultModel {
		t.Errorf("model = %q", c.model)
	}
	if c.endpoint != defaultEndpoint {
		t.Errorf("endpoint = %q", c.endpoint)
	}
	if c.http.Timeout != 60*time.Second {
		t.Errorf("timeout = %v", c.http.Timeout)
	}
}

func TestAskDecodesAllAnswerTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{
			"model": "jev-1.13.0",
			"answers": {
				"done": {"type": "noul", "noul": 0.999},
				"action": {"type": "choice", "choice": "technical", "probabilities": {"billing": 0.159, "technical": 0.8}, "confidence": 0.596},
				"amount": {"type": "score", "score": 1.035, "legend": {"0": "none", "1": "some"}, "confidence": 0.842}
			},
			"usage": {"input_tokens": 312, "output_tokens": 48}
		}`)
	}))
	defer srv.Close()

	c := New("k", WithEndpoint(srv.URL))
	resp, err := c.Ask(context.Background(), "state", map[string]Question{
		"done":   Noul("done?", map[string]string{"true": "yes", "false": "no"}),
		"action": Choice("action?", map[string]string{"technical": "technical"}),
		"amount": Score("amount?", []string{"none", "some"}),
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if resp.Model != "jev-1.13.0" {
		t.Errorf("model = %q", resp.Model)
	}
	if resp.Usage.InputTokens != 312 || resp.Usage.OutputTokens != 48 {
		t.Errorf("usage = %#v", resp.Usage)
	}
	if a := resp.Answers["done"]; a.Type != TypeNoul || a.Noul != 0.999 {
		t.Errorf("done answer = %#v", a)
	}
	a := resp.Answers["action"]
	if a.Type != TypeChoice || a.Choice != "technical" || a.Confidence != 0.596 {
		t.Errorf("action answer = %#v", a)
	}
	if a.Probabilities["billing"] != 0.159 || a.Probabilities["technical"] != 0.8 {
		t.Errorf("probabilities = %#v", a.Probabilities)
	}
	s := resp.Answers["amount"]
	if s.Type != TypeScore || s.Score != 1.035 || s.Confidence != 0.842 {
		t.Errorf("amount answer = %#v", s)
	}
	if s.Legend["1"] != "some" {
		t.Errorf("legend = %#v", s.Legend)
	}
}

func TestAskErrors(t *testing.T) {
	ctx := context.Background()
	valid := map[string]Question{"q": Noul("done?", map[string]string{"true": "yes"})}

	t.Run("missing api key", func(t *testing.T) {
		t.Setenv(apiKeyEnv, "")
		if _, err := FromEnv(); err == nil {
			t.Fatal("expected error for unset key")
		}
	})

	t.Run("empty api key", func(t *testing.T) {
		t.Setenv(apiKeyEnv, "")
		if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), apiKeyEnv) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("from env ok", func(t *testing.T) {
		t.Setenv(apiKeyEnv, "env-key")
		c, err := FromEnv(WithEndpoint("http://example.invalid"))
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		if c.apiKey != "env-key" {
			t.Errorf("apiKey = %q", c.apiKey)
		}
	})

	t.Run("nil state", func(t *testing.T) {
		if _, err := New("k").Ask(ctx, nil, valid); err == nil {
			t.Fatal("expected error for nil state")
		}
	})

	t.Run("no questions", func(t *testing.T) {
		if _, err := New("k").Ask(ctx, "s", nil); err == nil {
			t.Fatal("expected error for empty questions")
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		q := map[string]Question{"q": {Type: "verdict", Criteria: []string{"a"}}}
		if _, err := New("k").Ask(ctx, "s", q); err == nil {
			t.Fatal("expected error for unknown type")
		}
	})

	t.Run("empty criteria", func(t *testing.T) {
		for _, q := range []Question{
			{Type: TypeChoice, Criteria: map[string]string{}},
			{Type: TypeScore, Criteria: []string{}},
			{Type: TypeNoul},
		} {
			if _, err := New("k").Ask(ctx, "s", map[string]Question{"q": q}); err == nil {
				t.Fatalf("expected error for %#v", q)
			}
		}
	})

	t.Run("non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid api key"}`)
		}))
		defer srv.Close()

		_, err := New("k", WithEndpoint(srv.URL)).Ask(ctx, "s", valid)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid api key") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("error body truncated", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, strings.Repeat("x", errorBodyLimit*2))
		}))
		defer srv.Close()

		_, err := New("k", WithEndpoint(srv.URL)).Ask(ctx, "s", valid)
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Count(err.Error(), "x") != errorBodyLimit {
			t.Errorf("body not capped: %d bytes", strings.Count(err.Error(), "x"))
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"answers": `)
		}))
		defer srv.Close()

		if _, err := New("k", WithEndpoint(srv.URL)).Ask(ctx, "s", valid); err == nil {
			t.Fatal("expected decode error")
		}
	})

	t.Run("context canceled", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer srv.Close()

		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := New("k", WithEndpoint(srv.URL)).Ask(cctx, "s", valid); err == nil {
			t.Fatal("expected context error")
		}
	})

	t.Run("transport error", func(t *testing.T) {
		if _, err := New("k", WithEndpoint("http://127.0.0.1:1")).Ask(ctx, "s", valid); err == nil {
			t.Fatal("expected transport error")
		}
	})
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	ab, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	bb, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(ab) == string(bb) {
		return true
	}
	t.Logf("got %s want %s", ab, bb)
	return false
}
