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

	channelCategory    []ChannelCategory
	keepAlive          caddy.Duration
	maxEventBufferSize int

	subscribeRetries     int
	subscribeTryInterval time.Duration

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
	update atomic.Bool
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
	up.logger = m.logger.Named("upstream")
	up.websocketConfig = m.websocketConfig
	up.recoverWait = m.RecoverWait
	up.headers = m.Headers
	up.metadata = m.Metadata
	up.channelCategory = m.ChannelCategory
	up.keepAlive = m.KeepAlive
	up.maxEventBufferSize = m.MaxEventBufferSize
	up.subscribeRetries = m.SubscribeRetries
	up.subscribeTryInterval = time.Duration(m.SubscribeTryInterval)

	if _, ok := upstreamMap[upstreamUrl]; !ok {
		go up.upstreamMaintainer()
		go up.messageProcessor()
		upstreamMap[upstreamUrl] = up
	}

	up.count.Add(1)
	up.update.Store(true)
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
	logger := u.logger.Named("maintainer")
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
			if c := logger.Check(zap.InfoLevel, "connect to upstream failed"); c != nil {
				c.Write(zap.String("upstream", u.upstream), zap.Error(err))
			}
		} else {
			caddyNotifierMetrics.upstreamStatus.WithLabelValues(u.upstream).Set(1.0)
			u.pumpMessage(w, logger)
			if c := logger.Check(zap.InfoLevel, "upstream disconnected"); c != nil {
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
	config.replacer = u.replacer
	config.logger = u.logger.Named("conn")
	return newWebSocket(conn, u.upstreamRespChan, config), nil
}

func (u *upstream) pumpMessage(w *upstreamWebSocket, logger *zap.Logger) {
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
			if c := logger.Check(zap.DebugLevel, "notifier -> upstream"); c != nil {
				c.Write(zap.Any("request", v))
			}
		}
	}
}

func (u *upstream) messageProcessor() {
	logger := u.logger.Named("hub")
	hub := newMessageHub(messageHubConfig{
		logger:             logger,
		upstreamReqChan:    u.upstreamReqChan,
		metadata:           u.metadata,
		channelCategory:    u.channelCategory,
		keepAlive:          time.Duration(u.keepAlive),
		maxEventBufferSize: u.maxEventBufferSize,
		retries:            u.subscribeRetries,
	})
	defer hub.Close()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	var retryChan <-chan time.Time
	if u.subscribeTryInterval > 0 {
		retry_ticker := time.NewTicker(u.subscribeTryInterval)
		defer retry_ticker.Stop()
		retryChan = retry_ticker.C
	} else {
		retryChan = make(<-chan time.Time)
	}

	for {
		select {
		case <-u.ctx.Done():
			logger.Debug("upstream message processor done")
			return

		case v := <-u.subscriberReqChan:
			hub.handleSubReq(v)

		case v := <-u.upstreamRespChan:
			if c := logger.Check(zap.DebugLevel, "upstream -> notifier"); c != nil {
				c.Write(zap.Any("response", v.value))
			}
			hub.handleUpstreamResp(v)

		case <-u.upstreamResume:
			hub.handleUpstreamResume()

		case <-ticker.C:
			hub.pruneSubscription()
			updateMetrics(hub, u.upstream)
			if u.subscribeTryInterval <= 0 {
				hub.retrySubscribeRequests()
			}
			if u.update.CompareAndSwap(true, false) {
				if c := logger.Check(zap.DebugLevel, "hub configuration updated"); c != nil {
					c.Write(
						zap.Any("metadata", u.metadata),
						zap.Any("channel_category", u.channelCategory),
						zap.Duration("keep_alive", time.Duration(u.keepAlive)),
						zap.Int("max_event_buffer_size", u.maxEventBufferSize),
						zap.Int("retries", u.subscribeRetries),
					)
				}
				hub.updateConfig(&messageHubConfig{
					logger:             u.logger,
					metadata:           u.metadata,
					channelCategory:    u.channelCategory,
					keepAlive:          time.Duration(u.keepAlive),
					maxEventBufferSize: u.maxEventBufferSize,
					retries:            u.subscribeRetries,
				})
			}

		case <-retryChan:
			hub.retrySubscribeRequests()
		}
	}
}
