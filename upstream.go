package caddynotifier

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

type upstream struct {
	// config
	replacer        *caddy.Replacer
	logger          *zap.Logger
	websocketConfig *websocketConfig

	upstream    string
	recoverWait caddy.Duration
	headers     *headers.Handler
	metadata    map[string]string

	channelCategory []ChannelCategory

	// upstreams
	upstreamRespChan chan inboundMessage[NotifierResponse]
	upstreamReqChan  chan *NotifierRequest
	upstreamResume   chan struct{}

	// subscribers
	subscriberReqChan chan inboundMessage[SubscriberRequest]

	// reference count
	ctx    context.Context
	cancel context.CancelFunc
	count  atomic.Int32
}

var (
	upstreamMu  sync.Mutex
	upstreamMap map[string]*upstream = make(map[string]*upstream)
)

func getUpstream(upstreamUrl string, m *WebSocketNotifier) *upstream {
	upstreamMu.Lock()
	defer upstreamMu.Unlock()

	var up *upstream
	if u, ok := upstreamMap[upstreamUrl]; ok {
		up = u
	} else {
		up = &upstream{
			upstreamRespChan:  make(chan inboundMessage[NotifierResponse], m.ChanSize),
			upstreamReqChan:   make(chan *NotifierRequest, m.ChanSize),
			subscriberReqChan: make(chan inboundMessage[SubscriberRequest], m.ChanSize),
			upstreamResume:    make(chan struct{}, m.ChanSize),
			upstream:          upstreamUrl,
		}
		ctx, cancel := context.WithCancel(context.Background())
		up.ctx = ctx
		up.cancel = cancel
	}
	repl, ok := m.ctx.Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	if !ok {
		repl = caddy.NewReplacer()
	}
	up.replacer = repl
	up.logger = m.logger
	up.websocketConfig = m.websocketConfig
	up.recoverWait = m.RecoverWait
	up.headers = m.Headers
	up.metadata = m.Metadata
	up.channelCategory = m.ChannelCategory

	if _, ok := upstreamMap[upstreamUrl]; !ok {
		go up.upstreamMaintainer()
		go up.messageProcessor()
		upstreamMap[upstreamUrl] = up
	}

	up.count.Add(1)
	return up
}

func removeUpstream(upstreamUrl string) {
	upstreamMu.Lock()
	defer upstreamMu.Unlock()

	if u, ok := upstreamMap[upstreamUrl]; ok {
		u.count.Add(-1)
		if u.count.Load() == 0 {
			u.cancel()
			delete(upstreamMap, upstreamUrl)
		}
	}
}

func (u *upstream) upstreamMaintainer() {
	recoverWait := time.Duration(u.recoverWait)
	if recoverWait == 0 {
		recoverWait = defaultRecoverWait
	}
	for {
		select {
		case <-u.ctx.Done():
			return
		default:
		}
		recoverWait := time.After(recoverWait)
		w, err := u.dialUpstream()
		if err != nil {
			caddyNotifierMetrics.upstreamStatus.WithLabelValues(u.upstream).Set(0.0)
			if c := u.logger.Check(zap.InfoLevel, "connect to upstream failed"); c != nil {
				c.Write(zap.String("upstream", u.upstream), zap.Error(err))
			}
		} else {
			caddyNotifierMetrics.upstreamStatus.WithLabelValues(u.upstream).Set(1.0)
			u.pumpMessage(w)
			if c := u.logger.Check(zap.InfoLevel, "upstream disconnected"); c != nil {
				c.Write(zap.String("upstream", u.upstream), zap.Error(w.err))
			}
		}

		select {
		case <-u.ctx.Done():
			return
		case <-recoverWait:
		}
	}
}

func (u *upstream) dialUpstream() (*upstreamWebSocket, error) {
	ctx, cancel := context.WithTimeout(u.ctx, defaultRecoverWait)
	defer cancel()

	h := make(http.Header)
	u.headers.Request.ApplyTo(h, u.replacer)

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.upstream, h)
	if err != nil {
		return nil, err
	}
	config := *u.websocketConfig
	config.shorty = false
	config.metrics = false
	config.pingText = false
	return newWebSocket(conn, u.upstreamRespChan, &config, u.replacer), nil
}

func (u *upstream) pumpMessage(w *upstreamWebSocket) {
	defer w.Close()
	u.upstreamResume <- struct{}{}
	for {
		select {
		case <-u.ctx.Done():
			return

		case <-w.done:
			return

		case v := <-u.upstreamReqChan:
			buf := new(bytes.Buffer)
			if err := json.NewEncoder(buf).Encode(v); err != nil {
				return
			}
			w.outboundChan <- &outboundMessage{messageType: websocket.TextMessage, data: buf.Bytes()}
		}
	}
}

func (u *upstream) messageProcessor() {
	hub := newMessageHub(u.upstreamReqChan, u.metadata, u.channelCategory)
	defer hub.Close()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-u.ctx.Done():
			return

		case v := <-u.subscriberReqChan:
			hub.handleSubReq(v)

		case v := <-u.upstreamRespChan:
			hub.handleUpstreamResp(v)

		case <-u.upstreamResume:
			hub.handleUpstreamResume()

		case <-ticker.C:
			updateMetrics(hub, u.upstream)
		}
	}
}
