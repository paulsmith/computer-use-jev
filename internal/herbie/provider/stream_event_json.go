package provider

import "encoding/json"

// streamEventJSON is the provider-event trace boundary.
type streamEventJSON struct {
	Kind               StreamEventKind
	Text               string
	ArgsDelta          string
	ID                 *string
	Name               string
	JSON               string
	ReasoningPartBreak bool       `json:",omitempty"`
	ReasoningSummary   bool       `json:",omitempty"`
	Queries            []string   `json:",omitempty"`
	Citations          []Citation `json:",omitempty"`

	Attempt, MaxAttempts, HTTPStatus int
	DelayMS                          int64
	Processed, Total, Cache          int64
	StopReason                       string
	Usage                            *StreamUsage
	Response                         *StreamResponse
	Message                          string
}

func (event StreamEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(streamEventJSON{
		Kind: event.Kind, Text: event.Text, ArgsDelta: event.ArgsDelta, ID: event.ID, Name: event.Name, JSON: event.JSON, Queries: event.Queries, Citations: event.Citations,
		Attempt: event.Attempt, MaxAttempts: event.MaxAttempts, HTTPStatus: event.HTTPStatus, DelayMS: event.Delay.Milliseconds(),
		ReasoningPartBreak: event.ReasoningPartBreak,
		ReasoningSummary:   event.ReasoningSummary,
		Processed:          event.Processed, Total: event.Total, Cache: event.Cache, StopReason: event.StopReason, Usage: event.Usage, Response: event.Response, Message: event.Message,
	})
}
