package caddynotifier

import "encoding/json"

type upstreamWebSocket = webSocket[NotifierResponse]
type subscriberWebSocket = webSocket[SubscriberRequest]

// SubscriberRequest subscriber -> notifier
type SubscriberRequest struct {
	Operation  string   `json:"operation"`
	RequestId  string   `json:"request_id"`
	Credential string   `json:"credential,omitempty"`
	Channels   []string `json:"channels,omitempty"`
}

// SubscriberResponse notifier -> subscriber
type SubscriberResponse struct {
	Operation string          `json:"operation"`
	Channels  []string        `json:"channels,omitempty"`
	Accept    []string        `json:"accept"`
	Reject    []string        `json:"reject"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// NotifierRequest notifier -> backend
type NotifierRequest struct {
	Operation    string   `json:"operation"`
	ConnectionId string   `json:"connection_id"`
	RequestId    string   `json:"request_id"`
	Channels     []string `json:"channels"`
	Credential   string   `json:"credential,omitempty"`
}

// NotifierResponse backend -> notifier
type NotifierResponse struct {
	Operation    string          `json:"operation"`
	ConnectionId string          `json:"connection_id,omitempty"`
	Channels     []string        `json:"channels"`
	Accept       []string        `json:"accept"`
	Reject       []string        `json:"reject"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Credential   string          `json:"credential,omitempty"`
}
