package caddynotifier

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"runtime"
	"time"

	"github.com/gorilla/websocket"
)

type messageHub struct {
	// websocket id -> websocket  (verify)
	idMap map[string]*subscriberWebSocket

	// websocket -> subscription
	websocketChannel map[*subscriberWebSocket]*subscription
	// all subscription available: token -> subscription (resume)
	subscriptions map[string]*subscription
	// channel -> set of subscription (event)
	channels map[string]map[*subscription]struct{}
	// creds -> set of subscription (de-authorize)
	credMap map[string]map[*subscription]struct{}

	// upstreamChan
	upstreamReqChan chan *NotifierRequest
	// workerChan
	workerChan chan *workerRequest

	// metadata to be add to subscribe
	metadata map[string]string

	// metrics categories
	channelCategory []ChannelCategory
	// channel -> category
	channelCategoryMap map[string]string
	// category that exists
	categorySet map[string]struct{}

	keepAlive time.Duration

	// metrics
	eventSent          int64
	subscribeRequested int64
}

type subscription struct {
	token string
	// current websocket connection, nil if disconnected
	conn *subscriberWebSocket
	// set of subscribed channels
	channels map[string]struct{}
	// cred -> set of channels
	cred map[string]map[string]struct{}
	// if disconnected, it records the last active time
	lastActive time.Time
}

type workerRequest struct {
	websockets map[*subscriberWebSocket]struct{}
	websocket  *subscriberWebSocket
	response   *SubscriberResponse
}

type messageHubConfig struct {
	upstreamReqChan chan *NotifierRequest
	metadata        map[string]string
	channelCategory []ChannelCategory
	keepAlive       time.Duration
}

const (
	workerChanSize = 256
	tokenLength    = 32 // 256 bits
)

func newMessageHub(conf messageHubConfig) *messageHub {
	workerCount := 4
	if cur := runtime.GOMAXPROCS(0); cur < workerCount {
		workerCount = cur
	}

	hub := &messageHub{
		websocketChannel:   make(map[*subscriberWebSocket]*subscription),
		subscriptions:      make(map[string]*subscription),
		channels:           make(map[string]map[*subscription]struct{}),
		idMap:              make(map[string]*subscriberWebSocket),
		credMap:            make(map[string]map[*subscription]struct{}),
		upstreamReqChan:    conf.upstreamReqChan,
		workerChan:         make(chan *workerRequest, workerChanSize),
		metadata:           conf.metadata,
		channelCategory:    conf.channelCategory,
		channelCategoryMap: make(map[string]string),
		categorySet:        make(map[string]struct{}),
		keepAlive:          conf.keepAlive,
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
		var metadata map[string]string
		if m.metadata != nil {
			metadata = map[string]string{}
			for key, value := range m.metadata {
				metadata[key] = v.conn.replacer.ReplaceKnown(value, "")
			}
		}

		m.idMap[v.conn.id] = v.conn
		m.upstreamReqChan <- &NotifierRequest{
			Operation:    "subscribe",
			RequestId:    v.value.RequestId,
			ConnectionId: v.conn.id,
			Channels:     v.value.Channels,
			Credential:   v.value.Credential,
			Metadata:     metadata,
		}
		m.subscribeRequested += int64(len(v.value.Channels))

	case "unsubscribe":
		sub, ok := m.websocketChannel[v.conn]
		if !ok {
			return
		}
		ch := make([]string, 0, len(v.value.Channels))
		for _, c := range v.value.Channels {
			if _, ok := sub.channels[c]; !ok {
				continue
			}
			delete(m.channels[c], sub)
			if len(m.channels[c]) == 0 {
				delete(m.channels, c)
				delete(m.channelCategoryMap, c)
				ch = append(ch, c)
			}
			delete(sub.channels, c)
		}
		if len(ch) > 0 {
			m.upstreamReqChan <- &NotifierRequest{
				Operation: "unsubscribe",
				Channels:  ch,
			}
		}

	case "resume":
		sub, ok := m.subscriptions[v.value.ResumeToken]
		if !ok {
			return
		}
		sub.conn = v.conn
		m.websocketChannel[v.conn] = sub

		ch := make([]string, 0, len(sub.channels))
		for c := range sub.channels {
			ch = append(ch, c)
		}
		if len(sub.channels) > 0 {
			select {
			case m.workerChan <- &workerRequest{
				websocket: v.conn,
				response: &SubscriberResponse{
					Operation: "verify",
					Accept:    ch,
				},
			}:
			default:
			}
		}
		// TODO: send buffed messages
	}
}

func (m *messageHub) handleSubClose(w *subscriberWebSocket) {
	sub := m.websocketChannel[w]
	sub.conn = nil
	sub.lastActive = time.Now()
	delete(m.idMap, w.id)
	delete(m.websocketChannel, w)
}

func (m *messageHub) newSubscription(conn *subscriberWebSocket) *subscription {
	buf := make([]byte, tokenLength)
	_, err := io.ReadFull(rand.Reader, buf)
	if err != nil {
		// TODO: grace handel
		return nil
	}
	token := base64.URLEncoding.EncodeToString(buf)
	sub := &subscription{
		token:    token,
		conn:     conn,
		channels: make(map[string]struct{}),
		cred:     make(map[string]map[string]struct{}),
	}
	m.subscriptions[token] = sub
	return sub
}

func (m *messageHub) handleAccept(channels []string, credential string, w *subscriberWebSocket) {
	if m.websocketChannel[w] == nil {
		m.websocketChannel[w] = m.newSubscription(w)
	}
	sub := m.websocketChannel[w]
	if sub.cred[credential] == nil {
		sub.cred[credential] = make(map[string]struct{})
	}
	if m.credMap[credential] == nil {
		m.credMap[credential] = map[*subscription]struct{}{}
	}
	m.credMap[credential][sub] = struct{}{}

	for _, c := range channels {
		sub.channels[c] = struct{}{}
		sub.cred[credential][c] = struct{}{}

		if m.channels[c] == nil {
			m.channels[c] = make(map[*subscription]struct{})
			for _, cate := range m.channelCategory {
				if cate.re.Match([]byte(c)) {
					m.channelCategoryMap[c] = cate.Category
					m.categorySet[cate.Category] = struct{}{}
					break
				}
			}
		}
		m.channels[c][sub] = struct{}{}
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
		select {
		case m.workerChan <- &workerRequest{
			websocket: w,
			response: &SubscriberResponse{
				Operation: "verify",
				Accept:    v.value.Accept,
				Reject:    v.value.Reject,
			},
		}:
		default:
		}

	case "event":
		websocketToNotify := make(map[*subscriberWebSocket]struct{})
		for _, c := range v.value.Channels {
			for sub := range m.channels[c] {
				if sub.conn == nil {
					continue
				}
				websocketToNotify[sub.conn] = struct{}{}
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
		unsubscribes := make([]string, 0)
		for sub := range m.credMap[v.value.Credential] {
			ch := make([]string, 0, len(sub.cred[v.value.Credential]))
			for c := range sub.cred[v.value.Credential] {
				ch = append(ch, c)
				delete(m.channels[c], sub)
				if len(m.channels[c]) == 0 {
					delete(m.channels, c)
					delete(m.channelCategoryMap, c)
					unsubscribes = append(unsubscribes, c)
				}
				delete(m.websocketChannel, sub.conn)
			}
			delete(sub.cred, v.value.Credential)

			if sub.conn != nil {
				select {
				case m.workerChan <- &workerRequest{
					websocket: sub.conn,
					response: &SubscriberResponse{
						Operation: "unsubscribe",
						Channels:  ch,
					},
				}:
				default:
				}
			}
		}

		delete(m.credMap, v.value.Credential)
		if len(unsubscribes) > 0 {
			m.upstreamReqChan <- &NotifierRequest{
				Operation: "unsubscribe",
				Channels:  unsubscribes,
			}
		}
	}
}

func (m *messageHub) handleUpstreamResume() {
	ch := make([]string, 0, len(m.channels))
	for k := range m.channels {
		ch = append(ch, k)
	}
	if len(ch) > 0 {
		m.upstreamReqChan <- &NotifierRequest{
			Operation: "resume",
			Channels:  ch,
		}
	}
}

func (m *messageHub) pruneSubscription() {
	for token, sub := range m.subscriptions {
		if sub.conn != nil {
			continue
		}
		if time.Since(sub.lastActive) < m.keepAlive {
			continue
		}

		ch := make([]string, 0, len(sub.channels))
		for c := range sub.channels {
			delete(m.channels[c], sub)
			if len(m.channels[c]) == 0 {
				delete(m.channels, c)
				delete(m.channelCategoryMap, c)
				ch = append(ch, c)
			}
		}

		if len(ch) > 0 {
			m.upstreamReqChan <- &NotifierRequest{
				Operation: "unsubscribe",
				Channels:  ch,
			}
		}

		for cred := range sub.cred {
			delete(m.credMap[cred], sub)
			if len(m.credMap[cred]) == 0 {
				delete(m.credMap, cred)
			}
		}
		delete(m.subscriptions, token)
	}
}
