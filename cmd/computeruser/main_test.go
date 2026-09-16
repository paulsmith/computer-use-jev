package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDryRunPlansCompoundGoalWithoutExecuting(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "test")
	t.Setenv("COMPUTER_USE_PROVIDER", "openrouter")
	t.Setenv("COMPUTER_USE_MODEL", "test-model")
	t.Setenv("OPENROUTER_API_KEY", "test")
	oldArgs, oldTransport, oldStdout := os.Args, http.DefaultTransport, os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		os.Args = oldArgs
		http.DefaultTransport = oldTransport
		os.Stdout = oldStdout
		_ = read.Close()
		_ = write.Close()
	}()
	os.Stdout = write
	os.Args = []string{"computer_use", "-json", "-dry-run", "-goal", "open textedit select the text and make it bold"}
	calls := 0
	http.DefaultTransport = testTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		body := `{"answers":{"multiple":{"type":"noul","noul":0.99}}}`
		contentType := "application/json"
		if r.URL.Host == "openrouter.ai" {
			payload := map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": `["Activate TextEdit","Select all text in TextEdit","Make selected text bold in TextEdit"]`}, "finish_reason": "stop"}}}
			b, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			body = "data: " + string(b) + "\n\n"
			contentType = "text/event-stream"
		} else if r.URL.Host != "api.typesafe.ai" {
			t.Fatalf("unexpected host %s", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Type         string
		Instructions []string
	}
	if err := json.Unmarshal(output, &plan); err != nil {
		t.Fatalf("not one JSON plan: %s: %v", output, err)
	}
	if plan.Type != "plan" || len(plan.Instructions) != 3 || calls != 2 {
		t.Fatalf("plan=%+v calls=%d", plan, calls)
	}
}
