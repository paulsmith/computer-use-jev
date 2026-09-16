// Package typesafe is a minimal client for the TypeSafe System One API
// (model Jev). It covers the single endpoint the decision loop needs.
package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"time"
)

const (
	defaultModel    = "jev-latest"
	defaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	apiKeyEnv       = "TYPESAFE_API_KEY"

	errorBodyLimit = 512
)

// Question types accepted by the API.
const (
	TypeNoul   = "noul"
	TypeChoice = "choice"
	TypeScore  = "score"
)

type Client struct {
	apiKey   string
	model    string
	endpoint string
	http     *http.Client
}

type Option func(*Client)

func WithModel(model string) Option {
	return func(c *Client) { c.model = model }
}

func WithEndpoint(endpoint string) Option {
	return func(c *Client) { c.endpoint = endpoint }
}

func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:   apiKey,
		model:    defaultModel,
		endpoint: defaultEndpoint,
		http:     &http.Client{Timeout: 60 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func FromEnv(opts ...Option) (*Client, error) {
	key := os.Getenv(apiKeyEnv)
	if key == "" {
		return nil, fmt.Errorf("environment variable %s is not set", apiKeyEnv)
	}
	return New(key, opts...), nil
}

// Question covers all three question types. Instructions and Criteria are
// `any` because the shapes differ per type: noul/choice take a map, score
// takes a list of levels.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria"`
}

func Noul(instructions string, criteria map[string]string) Question {
	return Question{Type: TypeNoul, Instructions: instructions, Criteria: criteria}
}

func Choice(instructions string, criteria map[string]string) Question {
	return Question{Type: TypeChoice, Instructions: instructions, Criteria: criteria}
}

func Score(instructions string, levels []string) Question {
	return Question{Type: TypeScore, Instructions: instructions, Criteria: levels}
}

func (q Question) validate() error {
	switch q.Type {
	case TypeNoul, TypeChoice:
		if criteriaLen(q.Criteria) == 0 {
			return fmt.Errorf("%s question requires non-empty criteria", q.Type)
		}
	case TypeScore:
		if criteriaLen(q.Criteria) == 0 {
			return fmt.Errorf("score question requires at least one level")
		}
	default:
		return fmt.Errorf("unknown question type %q", q.Type)
	}
	return nil
}

func criteriaLen(criteria any) int {
	if criteria == nil {
		return 0
	}
	v := reflect.ValueOf(criteria)
	switch v.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		if v.IsNil() {
			return 0
		}
		return v.Len()
	}
	return 0
}

type Answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Legend        map[string]string  `json:"legend"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type request struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// Ask sends one request with all questions; the API answers them in parallel,
// so questions cannot see each other's answers.
func (c *Client) Ask(ctx context.Context, state any, questions map[string]Question) (Response, error) {
	var zero Response
	if state == nil {
		return zero, fmt.Errorf("state is required")
	}
	if len(questions) == 0 {
		return zero, fmt.Errorf("at least one question is required")
	}
	for id, q := range questions {
		if err := q.validate(); err != nil {
			return zero, fmt.Errorf("question %q: %w", id, err)
		}
	}

	body, err := json.Marshal(request{State: state, Model: c.model, Questions: questions})
	if err != nil {
		return zero, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return zero, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return zero, fmt.Errorf("call typesafe: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return zero, fmt.Errorf("typesafe returned %s: %s", resp.Status, bytes.TrimSpace(b))
	}

	var out Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return zero, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}
