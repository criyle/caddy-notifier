package caddynotifier

import "encoding/json"

type upstreamWebSocket = webSocket[NotifierResponse]
type subscriberWebSocket = webSocket[SubscriberRequest]

// SubscriberRequest subscriber -> notifier
type SubscriberRequest struct {
	Operation   string   `json:"operation"`
	RequestId   string   `json:"request_id,omitempty"`
	Credential  string   `json:"credential,omitempty"`
	Channels    []string `json:"channels,omitempty"`
	ResumeToken string   `json:"resume_token,omitempty"`
	Seq         uint64   `json:"seq,omitempty"`
}

// SubscriberResponse notifier -> subscriber
type SubscriberResponse struct {
	Operation   string          `json:"operation"`
	RequestId   string          `json:"request_id,omitempty"`
	Channels    []string        `json:"channels,omitempty"`
	Accept      []string        `json:"accept,omitempty"`
	Reject      []string        `json:"reject,omitempty"`
	Message     string          `json:"message,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	ResumeToken string          `json:"resume_token,omitempty"`
	Seq         uint64          `json:"seq,omitempty"`
}

// NotifierRequest notifier -> backend
type NotifierRequest struct {
	Operation      string            `json:"operation"`
	SubscriptionId string            `json:"subscription_id,omitempty"`
	RequestId      string            `json:"request_id,omitempty"`
	Channels       []string          `json:"channels,omitempty"`
	Credential     string            `json:"credential,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// NotifierResponse backend -> notifier
type NotifierResponse struct {
	Operation      string          `json:"operation"`
	SubscriptionId string          `json:"subscription_id,omitempty"`
	RequestId      string          `json:"request_id,omitempty"`
	Channels       []string        `json:"channels,omitempty"`
	Accept         []string        `json:"accept,omitempty"`
	Reject         []string        `json:"reject,omitempty"`
	Message        string          `json:"message,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	Credential     string          `json:"credential,omitempty"`
}
