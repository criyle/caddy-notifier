package caddynotifier

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/headers"
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
	Upstream         string           `json:"upstream,omitempty"`
	WriteWait        caddy.Duration   `json:"write_wait,omitempty"`
	PongWait         caddy.Duration   `json:"pong_wait,omitempty"`
	PingInterval     caddy.Duration   `json:"ping_interval,omitempty"`
	MaxMessageSize   int64            `json:"max_message_size,omitempty"`
	ChanSize         int              `json:"chan_size,omitempty"`
	RecoverWait      caddy.Duration   `json:"recover_wait,omitempty"`
	Headers          *headers.Handler `json:"headers,omitempty"`
	Compression      string           `json:"compression,omitempty"`
	ShortyResetCount int              `json:"shorty_reset_count,omitempty"`

	// websocket upgrader
	upgrader *websocket.Upgrader

	// module related config
	ctx             caddy.Context
	logger          *zap.Logger
	websocketConfig *websocketConfig

	// upstream
	upstream *upstream
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
		EnableCompression: m.Compression == "permessage-deflate",
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
	if m.Headers != nil {
		err := m.Headers.Provision(ctx)
		if err != nil {
			return fmt.Errorf("provisioning embedded headers handler: %v", err)
		}
	}

	m.ctx = ctx
	m.logger = ctx.Logger()
	m.websocketConfig = &websocketConfig{
		writeWait:        writeWait,
		pongWait:         pongWait,
		pingInterval:     pingInterval,
		maxMessageSize:   maxMessageSize,
		chanSize:         chanSize,
		shortyResetCount: m.ShortyResetCount,
		shorty:           m.Compression == "shorty",
		metrics:          true,
	}
	m.upstream = getUpstream(m.Upstream, m)

	initCaddyNotifierMetrics(ctx.GetMetricsRegistry())
	return nil
}

// Cleanup implements caddy.CleanerUpper
func (m *WebSocketNotifier) Cleanup() error {
	removeUpstream(m.Upstream)
	return nil
}

// Validate implements caddy.Validator.
func (m *WebSocketNotifier) Validate() error {
	if m.PingInterval > m.PongWait {
		return fmt.Errorf("ping_interval > pong_wait: %v > %v", m.PingInterval, m.PongWait)
	}
	if m.Compression != "permessage-deflate" && m.Compression != "shorty" && m.Compression != "off" && m.Compression != "" {
		return fmt.Errorf("bad compression value: %s", m.Compression)
	}
	return nil
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (m *WebSocketNotifier) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	repl := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	if m.Headers != nil && m.Headers.Request != nil {
		m.Headers.Request.ApplyToRequest(r)
	}
	if m.Headers != nil && m.Headers.Response != nil {
		m.Headers.Response.ApplyTo(w.Header(), repl)
	}

	conn, err := m.upgrader.Upgrade(w, r, w.Header())
	if err != nil {
		return err
	}

	// it will start read / write loop internally, and the message processor
	// take care of the messages, so no need to maintain any state
	_ = newWebSocket(conn, m.upstream.subscriberReqChan, m.websocketConfig)
	return nil
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler. Syntax:
//
//	websocket_notifier [<matchers>] [<upstream>] {
//	  # configurations
//	  write_wait       <interval>
//	  pong_wait        <interval>
//	  ping_interval    <interval>
//	  max_message_size <size>
//	  chan_size        <num>
//	  recovery_wait    <interval>
//	  compression <permessage-deflate | shorty | off>
//	  shorty_reset_count <num>
//
//	  header_up   [+|-]<field> [<value|regexp> [<replacement>]]
//	  header_down [+|-]<field> [<value|regexp> [<replacement>]]
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

		case "compression":
			if !d.NextArg() {
				return d.ArgErr()
			}
			m.Compression = d.Val()

		case "shorty_reset_count":
			if !d.NextArg() {
				return d.ArgErr()
			}
			count, err := strconv.Atoi(d.Val())
			if err != nil {
				return d.Errf("bad shorty_reset_count value %s: %v", d.Val(), err)
			}
			m.ShortyResetCount = count

			// from https://github.com/caddyserver/caddy/blob/master/modules/caddyhttp/reverseproxy/caddyfile.go#L711
		case "header_up":
			var err error

			if m.Headers == nil {
				m.Headers = new(headers.Handler)
			}
			if m.Headers.Request == nil {
				m.Headers.Request = new(headers.HeaderOps)
			}
			args := d.RemainingArgs()

			switch len(args) {
			case 1:
				err = headers.CaddyfileHeaderOp(m.Headers.Request, args[0], "", nil)
			case 2:
				// some lint checks, I guess
				if strings.EqualFold(args[0], "host") && (args[1] == "{hostport}" || args[1] == "{http.request.hostport}") {
					caddy.Log().Named("caddyfile").Warn("Unnecessary header_up Host: the reverse proxy's default behavior is to pass headers to the upstream")
				}
				if strings.EqualFold(args[0], "x-forwarded-for") && (args[1] == "{remote}" || args[1] == "{http.request.remote}" || args[1] == "{remote_host}" || args[1] == "{http.request.remote.host}") {
					caddy.Log().Named("caddyfile").Warn("Unnecessary header_up X-Forwarded-For: the reverse proxy's default behavior is to pass headers to the upstream")
				}
				if strings.EqualFold(args[0], "x-forwarded-proto") && (args[1] == "{scheme}" || args[1] == "{http.request.scheme}") {
					caddy.Log().Named("caddyfile").Warn("Unnecessary header_up X-Forwarded-Proto: the reverse proxy's default behavior is to pass headers to the upstream")
				}
				if strings.EqualFold(args[0], "x-forwarded-host") && (args[1] == "{host}" || args[1] == "{http.request.host}" || args[1] == "{hostport}" || args[1] == "{http.request.hostport}") {
					caddy.Log().Named("caddyfile").Warn("Unnecessary header_up X-Forwarded-Host: the reverse proxy's default behavior is to pass headers to the upstream")
				}
				err = headers.CaddyfileHeaderOp(m.Headers.Request, args[0], args[1], nil)
			case 3:
				err = headers.CaddyfileHeaderOp(m.Headers.Request, args[0], args[1], &args[2])
			default:
				return d.ArgErr()
			}

			if err != nil {
				return d.Err(err.Error())
			}

		case "header_down":
			var err error

			if m.Headers == nil {
				m.Headers = new(headers.Handler)
			}
			if m.Headers.Response == nil {
				m.Headers.Response = &headers.RespHeaderOps{
					HeaderOps: new(headers.HeaderOps),
				}
			}
			args := d.RemainingArgs()

			switch len(args) {
			case 1:
				err = headers.CaddyfileHeaderOp(m.Headers.Response.HeaderOps, args[0], "", nil)
			case 2:
				err = headers.CaddyfileHeaderOp(m.Headers.Response.HeaderOps, args[0], args[1], nil)
			case 3:
				err = headers.CaddyfileHeaderOp(m.Headers.Response.HeaderOps, args[0], args[1], &args[2])
			default:
				return d.ArgErr()
			}

			if err != nil {
				return d.Err(err.Error())
			}

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

func updateMetrics(hub *messageHub, upstream string) {
	caddyNotifierMetrics.eventSent.WithLabelValues(upstream).Add(float64(hub.eventSent))
	hub.eventSent = 0
	caddyNotifierMetrics.subscribeRequest.WithLabelValues(upstream).Add(float64(hub.subscribeRequested))
	hub.subscribeRequested = 0
	caddyNotifierMetrics.activeConnection.WithLabelValues(upstream).Set(float64(len(hub.websocketChannel)))
	caddyNotifierMetrics.channelCount.WithLabelValues(upstream).Set(float64(len(hub.channels)))
	inbound := websocketInboundBytes.Swap(0)
	caddyNotifierMetrics.websocketInboundBytes.WithLabelValues(upstream).Add(float64(inbound))
	outbound := websocketOutboundBytes.Swap(0)
	caddyNotifierMetrics.websocketOutboundBytes.WithLabelValues(upstream).Add(float64(outbound))
	compressed := websocketCompressedBytes.Swap(0)
	caddyNotifierMetrics.websocketCompressedBytes.WithLabelValues(upstream).Add(float64(compressed))
}

// Interface guards
var (
	_ caddy.Provisioner           = (*WebSocketNotifier)(nil)
	_ caddy.CleanerUpper          = (*WebSocketNotifier)(nil)
	_ caddy.Validator             = (*WebSocketNotifier)(nil)
	_ caddyhttp.MiddlewareHandler = (*WebSocketNotifier)(nil)
	_ caddyfile.Unmarshaler       = (*WebSocketNotifier)(nil)
)
