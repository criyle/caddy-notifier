package caddynotifier

import "encoding/json"

type upstreamWebSocket = webSocket[NotifierResponse]
type subscriberWebSocket = webSocket[SubscriberRequest]

// SubscriberRequest subscriber -> notifier
type SubscriberRequest struct {
	Operation  string   `json:"operation"`
	RequestId  string   `json:"request_id,omitempty"`
	Credential string   `json:"credential,omitempty"`
	Channels   []string `json:"channels,omitempty"`
}

// SubscriberResponse notifier -> subscriber
type SubscriberResponse struct {
	Operation string          `json:"operation"`
	Channels  []string        `json:"channels,omitempty"`
	Accept    []string        `json:"accept,omitempty"`
	Reject    []string        `json:"reject,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// NotifierRequest notifier -> backend
type NotifierRequest struct {
	Operation    string   `json:"operation"`
	ConnectionId string   `json:"connection_id,omitempty"`
	RequestId    string   `json:"request_id,omitempty"`
	Channels     []string `json:"channels,omitempty"`
	Credential   string   `json:"credential,omitempty"`
}

// NotifierResponse backend -> notifier
type NotifierResponse struct {
	Operation    string          `json:"operation"`
	ConnectionId string          `json:"connection_id,omitempty"`
	Channels     []string        `json:"channels,omitempty"`
	Accept       []string        `json:"accept,omitempty"`
	Reject       []string        `json:"reject,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Credential   string          `json:"credential,omitempty"`
}
