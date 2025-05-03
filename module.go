package caddynotifier

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/dustin/go-humanize"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(WebSocketNotifier{})
	httpcaddyfile.RegisterHandlerDirective("websocket_notifier", parseCaddyfile)
	httpcaddyfile.RegisterDirectiveOrder("websocket_notifier", httpcaddyfile.After, "file_server")
}

// WebSocketNotifier implements a reverse proxy for WebSocket that
// group incoming WebSocket into logical channels and consolidate
// into event-driven stateless single WebSocket to the backend
// controller service
type WebSocketNotifier struct {
	// Upstream address for the backend controller
	Upstream       string         `json:"upstream,omitempty"`
	WriteWait      caddy.Duration `json:"write_wait,omitempty"`
	PongWait       caddy.Duration `json:"pong_wait,omitempty"`
	PingInterval   caddy.Duration `json:"ping_interval,omitempty"`
	MaxMessageSize int64          `json:"max_message_size,omitempty"`
	ChanSize       int            `json:"chan_size,omitempty"`
	RecoverWait    caddy.Duration `json:"recover_wait,omitempty"`

	// websocket upgrader
	upgrader *websocket.Upgrader

	// module related config
	ctx             caddy.Context
	logger          *zap.Logger
	websocketConfig *websocketConfig

	// upstreams
	upstreamRespChan chan inboundMessage[NotifierRequest, NotifierResponse]
	upstreamReqChan  chan *NotifierRequest

	// subscribers
	subscriberReqChan chan inboundMessage[SubscriberResponse, SubscriberRequest]
}

const (
	defaultWriteWait      = 10 * time.Second
	defaultPongWait       = 60 * time.Second
	defaultPingInterval   = 54 * time.Second
	defaultMaxMessageSize = 256 << 10 // 256 k
	defaultChanSize       = 16
	defaultRecoverWait    = 5 * time.Second
)

// CaddyModule returns the Caddy module information.
func (WebSocketNotifier) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.websocket_notifier",
		New: func() caddy.Module { return new(WebSocketNotifier) },
	}
}

// Provision implements caddy.Provisioner.
func (m *WebSocketNotifier) Provision(ctx caddy.Context) error {
	m.upgrader = &websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		EnableCompression: true,
	}
	writeWait := time.Duration(m.WriteWait)
	if writeWait == 0 {
		writeWait = defaultWriteWait
	}
	pongWait := time.Duration(m.PongWait)
	pingInterval := time.Duration(m.PingInterval)
	if pongWait == 0 || pingInterval == 0 {
		pongWait = defaultPongWait
		pingInterval = defaultPingInterval
	}
	maxMessageSize := m.MaxMessageSize
	if maxMessageSize == 0 {
		maxMessageSize = defaultMaxMessageSize
	}
	chanSize := m.ChanSize
	if chanSize == 0 {
		chanSize = defaultChanSize
	}

	m.ctx = ctx
	m.logger = ctx.Logger()
	m.websocketConfig = &websocketConfig{
		writeWait:      writeWait,
		pongWait:       pongWait,
		pingInterval:   pingInterval,
		maxMessageSize: maxMessageSize,
		chanSize:       chanSize,
	}

	m.upstreamRespChan = make(chan inboundMessage[NotifierRequest, NotifierResponse], m.ChanSize)
	m.upstreamReqChan = make(chan *NotifierRequest, m.ChanSize)

	m.subscriberReqChan = make(chan inboundMessage[SubscriberResponse, SubscriberRequest], m.ChanSize)

	initCaddyNotifierMetrics(ctx.GetMetricsRegistry())

	go m.upstreamMaintainer()
	go m.messageProcessor()

	return nil
}

// Cleanup implements caddy.CleanerUpper
func (m *WebSocketNotifier) Cleanup() error {
	return nil
}

// Validate implements caddy.Validator.
func (m *WebSocketNotifier) Validate() error {
	if m.PingInterval > m.PongWait {
		return fmt.Errorf("ping_interval > pong_wait: %v > %v", m.PingInterval, m.PongWait)
	}
	return nil
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (m *WebSocketNotifier) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	conn, err := m.upgrader.Upgrade(w, r, w.Header())
	if err != nil {
		return err
	}

	// it will start read / write loop internally, and the message processor
	// take care of the messages, so no need to maintain any state
	_ = newWebSocket(conn, m.subscriberReqChan, m.websocketConfig)
	return nil
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler. Syntax:
//
//	websocket_notifier [<matchers>] [<upstream>] {
//	    # configurations
//	    write_wait       <interval>
//	    pong_wait        <interval>
//	    ping_interval    <interval>
//	    max_message_size <size>
//	    chan_size        <num>
//	    recovery_wait    <interval>
//	}
func (m *WebSocketNotifier) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume directive name

	if !d.Args(&m.Upstream) {
		return d.ArgErr()
	}
	u, err := url.Parse(m.Upstream)
	if err != nil {
		return d.Errf("bad upstream url value %s: %v", d.Val(), err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return d.Errf("bad websocket scheme, must be ws or wss: %s", u.Scheme)
	}
	// block
	for nesting := d.Nesting(); d.NextBlock(nesting); {
		switch d.Val() {
		case "write_wait":
			if !d.NextArg() {
				return d.ArgErr()
			}
			dur, err := caddy.ParseDuration(d.Val())
			if err != nil {
				return d.Errf("bad duration value %s: %v", d.Val(), err)
			}
			m.WriteWait = caddy.Duration(dur)

		case "pong_wait":
			if !d.NextArg() {
				return d.ArgErr()
			}
			dur, err := caddy.ParseDuration(d.Val())
			if err != nil {
				return d.Errf("bad duration value %s: %v", d.Val(), err)
			}
			m.PongWait = caddy.Duration(dur)

		case "ping_interval":
			if !d.NextArg() {
				return d.ArgErr()
			}
			dur, err := caddy.ParseDuration(d.Val())
			if err != nil {
				return d.Errf("bad duration value %s: %v", d.Val(), err)
			}
			m.PingInterval = caddy.Duration(dur)

		case "max_message_size":
			if !d.NextArg() {
				return d.ArgErr()
			}
			size, err := humanize.ParseBytes(d.Val())
			if err != nil {
				return d.Errf("bad size value %s: %v", d.Val(), err)
			}
			m.MaxMessageSize = int64(size)

		case "chan_size":
			if !d.NextArg() {
				return d.ArgErr()
			}
			size, err := strconv.Atoi(d.Val())
			if err != nil {
				return d.Errf("bad size value %s: %v", d.Val(), err)
			}
			m.ChanSize = size

		case "recovery_wait":
			if !d.NextArg() {
				return d.ArgErr()
			}
			dur, err := caddy.ParseDuration(d.Val())
			if err != nil {
				return d.Errf("bad duration value %s: %v", d.Val(), err)
			}
			m.RecoverWait = caddy.Duration(dur)

		default:
			return d.Errf("unrecognized subdirective %s", d.Val())
		}
	}
	return nil
}

// parseCaddyfile unmarshals tokens from h into a new Middleware.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m WebSocketNotifier
	err := m.UnmarshalCaddyfile(h.Dispenser)
	return &m, err
}

func (m *WebSocketNotifier) upstreamMaintainer() {
	recoverWait := time.Duration(m.RecoverWait)
	if recoverWait == 0 {
		recoverWait = defaultRecoverWait
	}
	for {
		select {
		case <-m.ctx.Done():
			return
		default:
		}
		recoverWait := time.After(recoverWait)
		w, err := m.dialUpstream()
		if err != nil {
			caddyNotifierMetrics.upstreamStatus.WithLabelValues(m.Upstream).Set(0.0)
			if c := m.logger.Check(zap.InfoLevel, "connect to upstream failed"); c != nil {
				c.Write(zap.String("upstream", m.Upstream), zap.Error(err))
			}
		} else {
			caddyNotifierMetrics.upstreamStatus.WithLabelValues(m.Upstream).Set(1.0)
			m.pumpMessage(w)
			if c := m.logger.Check(zap.InfoLevel, "upstream disconnected"); c != nil {
				c.Write(zap.String("upstream", m.Upstream), zap.Error(w.err))
			}
		}

		select {
		case <-m.ctx.Done():
			return

		case <-recoverWait:
		}
	}
}

func (m *WebSocketNotifier) dialUpstream() (*upstreamWebSocket, error) {
	ctx, cancel := context.WithTimeout(m.ctx, defaultRecoverWait)
	defer cancel()

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, m.Upstream, nil)
	if err != nil {
		return nil, err
	}
	return newWebSocket(conn, m.upstreamRespChan, m.websocketConfig), nil
}

func (m *WebSocketNotifier) pumpMessage(w *upstreamWebSocket) {
	defer w.Close()
	for {
		select {
		case <-m.ctx.Done():
			return

		case <-w.done:
			return

		case v := <-m.upstreamReqChan:
			w.outboundChan <- v
		}
	}
}

func (m *WebSocketNotifier) messageProcessor() {
	hub := newMessageHub(m.upstreamReqChan)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return

		case v := <-m.subscriberReqChan:
			hub.handleSubReq(v)

		case v := <-m.upstreamRespChan:
			hub.handleUpstreamResp(v)

		case <-ticker.C:
			updateMetrics(hub, m.Upstream)
		}
	}
}

func updateMetrics(hub *messageHub, upstream string) {
	caddyNotifierMetrics.eventSent.WithLabelValues(upstream).Add(float64(hub.eventSent))
	hub.eventSent = 0
	caddyNotifierMetrics.subscribeRequest.WithLabelValues(upstream).Add(float64(hub.subscribeRequested))
	hub.subscribeRequested = 0
	caddyNotifierMetrics.activeConnection.WithLabelValues(upstream).Set(float64(len(hub.websocketChannel)))
	caddyNotifierMetrics.channelCount.WithLabelValues(upstream).Set(float64(len(hub.channels)))
}

// Interface guards
var (
	_ caddy.Provisioner           = (*WebSocketNotifier)(nil)
	_ caddy.CleanerUpper          = (*WebSocketNotifier)(nil)
	_ caddy.Validator             = (*WebSocketNotifier)(nil)
	_ caddyhttp.MiddlewareHandler = (*WebSocketNotifier)(nil)
	_ caddyfile.Unmarshaler       = (*WebSocketNotifier)(nil)
)
