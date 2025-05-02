package caddynotifier

import "encoding/json"

type upstreamWebSocket = webSocket[NotifierRequest, NotifierResponse]
type subscriberWebSocket = webSocket[SubscriberResponse, SubscriberRequest]

// SubscriberRequest subscriber -> notifier
type SubscriberRequest struct {
	Operation  string   `json:"operation"`
	Credential string   `json:"credential,omitempty"`
	Channels   []string `json:"channels,omitempty"`
}

// SubscriberResponse notifier -> subscriber
type SubscriberResponse struct {
	Operation string          `json:"operation"`
	Channels  []string        `json:"channels,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// NotifierRequest notifier -> backend
type NotifierRequest struct {
	Operation    string   `json:"operation"`
	ConnectionId string   `json:"connection_id"`
	Channels     []string `json:"channels"`
	Credential   string   `json:"credential,omitempty"`
}

// NotifierResponse backend -> notifier
type NotifierResponse struct {
	Operation    string          `json:"operation"`
	ConnectionId string          `json:"connection_id,omitempty"`
	Channels     []string        `json:"channels"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Credential   string          `json:"credential,omitempty"`
}
