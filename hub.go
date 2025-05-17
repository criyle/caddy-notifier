package caddynotifier

import (
	"bytes"
	"encoding/json"
	"runtime"

	"github.com/gorilla/websocket"
)

type messageHub struct {
	// websocket -> set of channels
	websocketChannel map[*subscriberWebSocket]map[string]struct{}
	// channel -> set of websockets
	channels map[string]map[*subscriberWebSocket]struct{}
	// websocket id (remote addr) -> websocket
	idMap map[string]*subscriberWebSocket
	// websocket -> set of creds
	websocketCred map[*subscriberWebSocket]map[string]struct{}
	// creds -> set of (*websocket -> set of channel)
	credMap map[string]map[*subscriberWebSocket]map[string]struct{}
	// upstreamChan
	upstreamReqChan chan *NotifierRequest
	// workerChan
	workerChan chan *workerRequest

	// metrics
	eventSent          int64
	subscribeRequested int64
}

type workerRequest struct {
	websockets map[*subscriberWebSocket]struct{}
	websocket  *subscriberWebSocket
	response   *SubscriberResponse
}

func newMessageHub(upstreamReqChan chan *NotifierRequest) *messageHub {
	workerCount := 4
	if cur := runtime.GOMAXPROCS(0); cur < workerCount {
		workerCount = cur
	}

	hub := &messageHub{
		websocketChannel: make(map[*subscriberWebSocket]map[string]struct{}),
		channels:         make(map[string]map[*subscriberWebSocket]struct{}),
		idMap:            make(map[string]*subscriberWebSocket),
		websocketCred:    make(map[*subscriberWebSocket]map[string]struct{}),
		credMap:          make(map[string]map[*subscriberWebSocket]map[string]struct{}),
		upstreamReqChan:  upstreamReqChan,
		workerChan:       make(chan *workerRequest, 256),
	}

	for range workerCount {
		go hub.workerLoop()
	}
	return hub
}

func (m *messageHub) Close() {
	close(m.workerChan)
}

func (m *messageHub) workerLoop() {
	for v := range m.workerChan {
		buf := new(bytes.Buffer)
		_ = json.NewEncoder(buf).Encode(v.response)
		msg := &outboundMessage{
			messageType: websocket.TextMessage,
			data:        buf.Bytes(),
		}
		if v.websocket != nil {
			select {
			case v.websocket.outboundChan <- msg:
			default:
			}
		}
		if v.websockets != nil {
			for w := range v.websockets {
				select {
				case w.outboundChan <- msg:
				default:
				}
			}
		}
	}
}

func (m *messageHub) handleSubReq(v inboundMessage[SubscriberRequest]) {
	if v.value == nil {
		m.handleSubClose(v.conn)
		return
	}
	select {
	case <-v.conn.done:
		return
	default:
	}
	switch v.value.Operation {
	case "subscribe":
		m.idMap[v.conn.id] = v.conn
		m.upstreamReqChan <- &NotifierRequest{
			Operation:    "subscribe",
			ConnectionId: v.conn.id,
			Channels:     v.value.Channels,
			Credential:   v.value.Credential,
		}
		m.subscribeRequested += int64(len(v.value.Channels))

	case "unsubscribe":
		if m.websocketChannel[v.conn] == nil {
			return
		}
		for _, c := range v.value.Channels {
			if _, ok := m.websocketChannel[v.conn][c]; !ok {
				continue
			}
			delete(m.channels[c], v.conn)
			if len(m.channels[c]) == 0 {
				delete(m.channels, c)
			}
			delete(m.websocketChannel[v.conn], c)
		}
	}
}

func (m *messageHub) handleSubClose(w *subscriberWebSocket) {
	for c := range m.websocketChannel[w] {
		delete(m.channels[c], w)
		if len(m.channels[c]) == 0 {
			delete(m.channels, c)
		}
	}
	for cred := range m.websocketCred[w] {
		delete(m.credMap[cred], w)
		if len(m.credMap[cred]) == 0 {
			delete(m.credMap, cred)
		}
	}

	delete(m.idMap, w.id)
	delete(m.websocketChannel, w)
	delete(m.websocketCred, w)
}

func (m *messageHub) handleAccept(channels []string, credential string, w *subscriberWebSocket) {
	for _, c := range channels {
		if m.websocketChannel[w] == nil {
			m.websocketChannel[w] = make(map[string]struct{})
		}
		m.websocketChannel[w][c] = struct{}{}

		if m.channels[c] == nil {
			m.channels[c] = make(map[*subscriberWebSocket]struct{})
		}
		m.channels[c][w] = struct{}{}

		if m.websocketCred[w] == nil {
			m.websocketCred[w] = make(map[string]struct{})
		}
		m.websocketCred[w][credential] = struct{}{}

		if m.credMap[credential] == nil {
			m.credMap[credential] = make(map[*subscriberWebSocket]map[string]struct{})
		}
		if m.credMap[credential][w] == nil {
			m.credMap[credential][w] = make(map[string]struct{})
		}
		m.credMap[credential][w][c] = struct{}{}
	}
	select {
	case m.workerChan <- &workerRequest{
		websocket: w,
		response: &SubscriberResponse{
			Operation: "subscribe",
			Channels:  channels,
		},
	}:
	default:
	}
}

func (m *messageHub) handleReject(channels []string, w *subscriberWebSocket) {
	select {
	case m.workerChan <- &workerRequest{
		websocket: w,
		response: &SubscriberResponse{
			Operation: "unsubscribe",
			Channels:  channels,
		},
	}:
	default:
	}
}

func (m *messageHub) handleUpstreamResp(v inboundMessage[NotifierResponse]) {
	if v.value == nil {
		return
	}
	switch v.value.Operation {
	case "verify":
		w, ok := m.idMap[v.value.ConnectionId]
		if !ok {
			return
		}
		select {
		case <-w.done:
			m.handleSubClose(w)
			return
		default:
		}
		m.handleAccept(v.value.Accept, v.value.Credential, w)
		m.handleReject(v.value.Reject, w)

	case "accept":
		w, ok := m.idMap[v.value.ConnectionId]
		if !ok {
			return
		}
		select {
		case <-w.done:
			m.handleSubClose(w)
			return
		default:
		}
		m.handleAccept(v.value.Channels, v.value.Credential, w)

	case "reject":
		w, ok := m.idMap[v.value.ConnectionId]
		if !ok {
			return
		}
		select {
		case <-w.done:
			m.handleSubClose(w)
			return
		default:
		}
		m.handleReject(v.value.Channels, w)

	case "event":
		websocketToNotify := make(map[*subscriberWebSocket]struct{})
		for _, c := range v.value.Channels {
			for w := range m.channels[c] {
				websocketToNotify[w] = struct{}{}
			}
		}
		if len(websocketToNotify) == 0 {
			return
		}
		select {
		case m.workerChan <- &workerRequest{
			websockets: websocketToNotify,
			response: &SubscriberResponse{
				Operation: "event",
				Channels:  v.value.Channels,
				Payload:   v.value.Payload,
			},
		}:
		default:
		}
		m.eventSent += int64(len(websocketToNotify))

	case "deauthorize":
		for w, channels := range m.credMap[v.value.Credential] {
			ch := make([]string, 0, len(channels))
			for c := range channels {
				ch = append(ch, c)
				delete(m.channels[c], w)
				if len(m.channels[c]) == 0 {
					delete(m.channels, c)
				}
				delete(m.websocketChannel[w], c)
			}
			delete(m.websocketCred[w], v.value.Credential)

			select {
			case m.workerChan <- &workerRequest{
				websocket: w,
				response: &SubscriberResponse{
					Operation: "unsubscribe",
					Channels:  ch,
				},
			}:
			default:
			}
		}
		delete(m.credMap, v.value.Credential)
	}
}
