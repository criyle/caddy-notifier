package caddynotifier

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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
	Upstream             string            `json:"upstream,omitempty"`
	WriteWait            caddy.Duration    `json:"write_wait,omitempty"`
	PongWait             caddy.Duration    `json:"pong_wait,omitempty"`
	PingInterval         caddy.Duration    `json:"ping_interval,omitempty"`
	MaxMessageSize       int64             `json:"max_message_size,omitempty"`
	ChanSize             int               `json:"chan_size,omitempty"`
	RecoverWait          caddy.Duration    `json:"recover_wait,omitempty"`
	Headers              *headers.Handler  `json:"headers,omitempty"`
	Compression          string            `json:"compression,omitempty"`
	ShortyResetCount     int               `json:"shorty_reset_count,omitempty"`
	PingType             string            `json:"ping_type,omitempty"`
	Metadata             map[string]string `json:"metadata,omitempty"`
	ChannelCategory      []ChannelCategory `json:"channel_category,omitempty"`
	KeepAlive            caddy.Duration    `json:"keep_alive,omitempty"`
	MaxEventBufferSize   int               `json:"max_event_buffer_size,omitempty"`
	SubscribeRetries     int               `json:"subscribe_retries,omitempty"`
	SubscribeTryInterval caddy.Duration    `json:"subscribe_try_interval,omitempty"`

	// websocket upgrader
	upgrader *websocket.Upgrader

	// module related config
	ctx              caddy.Context
	logger           *zap.Logger
	subscriberLogger *zap.Logger
	websocketConfig  *websocketConfig

	// upstream
	upstream *upstream
}

type ChannelCategory struct {
	Match    string `json:"match,omitempty"`
	Category string `json:"category,omitempty"`

	re *regexp.Regexp
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
	for i, cc := range m.ChannelCategory {
		re, err := regexp.Compile(cc.Match)
		if err != nil {
			return fmt.Errorf("provisioning channel_category: %v", err)
		}
		m.ChannelCategory[i].re = re
	}

	m.ctx = ctx
	m.logger = ctx.Logger()
	m.subscriberLogger = m.logger.Named("subscriber")
	m.websocketConfig = &websocketConfig{
		writeWait:        writeWait,
		pongWait:         pongWait,
		pingInterval:     pingInterval,
		maxMessageSize:   maxMessageSize,
		chanSize:         chanSize,
		shortyResetCount: m.ShortyResetCount,
		shorty:           m.Compression == "shorty",
		metrics:          true,
		pingText:         m.PingType == "text",
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
	if m.WriteWait > m.PingInterval {
		return fmt.Errorf("write_wait > ping_interval: %v > %v", m.WriteWait, m.PingInterval)
	}
	if m.Compression != "permessage-deflate" && m.Compression != "shorty" && m.Compression != "off" && m.Compression != "" {
		return fmt.Errorf("bad compression value: %s", m.Compression)
	}
	if m.PingType != "control" && m.PingType != "text" && m.PingType != "" {
		return fmt.Errorf("bad ping type value: %s", m.PingType)
	}
	if m.SubscribeRetries > 0 && m.SubscribeTryInterval <= 0 {
		return fmt.Errorf("subscribe_retries > 0 while subscribe_try_interval not set: %d %v", m.SubscribeRetries, m.SubscribeTryInterval)
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

	// if search parameter have shorty=on, turn on shorty on this connection
	config := *m.websocketConfig
	config.logger = m.subscriberLogger.With(zap.String("remote_addr", conn.RemoteAddr().String()))
	config.shorty = len(r.URL.Query().Get("shorty")) > 0
	config.replacer = repl

	if c := config.logger.Check(zap.DebugLevel, "new subscriber connected"); c != nil {
		c.Write(
			zap.Duration("write_wait", config.writeWait),
			zap.Duration("pong_wait", config.pongWait),
			zap.Duration("ping_interval", config.pingInterval),
			zap.Int64("max_message_size", config.maxMessageSize),
			zap.Int("chan_size", config.chanSize),
			zap.Bool("shorty", config.shorty),
			zap.Bool("ping_text", config.pingText),
		)
	}

	// it will start read / write loop internally, and the message processor
	// take care of the messages, so no need to maintain any state
	_ = newWebSocket(conn, m.upstream.subscriberReqChan, config)
	return nil
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler. Syntax:
//
//	websocket_notifier [<matchers>] [<upstream>] {
//	  # configurations
//	  write_wait       		<interval>
//	  pong_wait        		<interval>
//	  ping_interval    		<interval>
//	  max_message_size 		<size>
//	  chan_size        		<num>
//	  recovery_wait    		<interval>
//	  compression 			<permessage-deflate | shorty | off>
//	  shorty_reset_count 	<num>
//	  ping_type				<control | text>
//
//	  header_up   [+|-]<field> [<value|regexp> [<replacement>]]
//	  header_down [+|-]<field> [<value|regexp> [<replacement>]]
//	  metadata				<key>	<replacement>
//	  channel_category		<regexp>	category
//	  keep_alive			<interval>
//	  subscribe_retries		<retries>
//	  subscribe_try_interval	<interval>
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

		case "ping_type":
			if !d.NextArg() {
				return d.ArgErr()
			}
			m.PingType = d.Val()

		case "metadata":
			if m.Metadata == nil {
				m.Metadata = make(map[string]string)
			}
			args := d.RemainingArgs()
			switch len(args) {
			case 2:
				if _, ok := m.Metadata[args[0]]; ok {
					return d.Errf("duplicated metadata key: %s", args[0])
				}
				m.Metadata[args[0]] = args[1]

			default:
				return d.ArgErr()
			}

		case "channel_category":
			if m.ChannelCategory == nil {
				m.ChannelCategory = make([]ChannelCategory, 0)
			}
			args := d.RemainingArgs()
			switch len(args) {
			case 2:
				m.ChannelCategory = append(m.ChannelCategory, ChannelCategory{
					Match:    args[0],
					Category: args[1],
				})

			default:
				return d.ArgErr()
			}

		case "keep_alive":
			if !d.NextArg() {
				return d.ArgErr()
			}
			dur, err := caddy.ParseDuration(d.Val())
			if err != nil {
				return d.Errf("bad duration value %s: %v", d.Val(), err)
			}
			m.KeepAlive = caddy.Duration(dur)

		case "max_event_buffer_size":
			if !d.NextArg() {
				return d.ArgErr()
			}
			size, err := strconv.Atoi(d.Val())
			if err != nil {
				return d.Errf("bad size value %s: %v", d.Val(), err)
			}
			m.MaxEventBufferSize = size

		case "subscribe_retries":
			if !d.NextArg() {
				return d.ArgErr()
			}
			size, err := strconv.Atoi(d.Val())
			if err != nil {
				return d.Errf("bad subscribe_retries value %s: %v", d.Val(), err)
			}
			m.SubscribeRetries = size

		case "subscribe_try_interval":
			if !d.NextArg() {
				return d.ArgErr()
			}
			dur, err := caddy.ParseDuration(d.Val())
			if err != nil {
				return d.Errf("bad duration value %s: %v", d.Val(), err)
			}
			m.SubscribeTryInterval = caddy.Duration(dur)

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

// Interface guards
var (
	_ caddy.Provisioner           = (*WebSocketNotifier)(nil)
	_ caddy.CleanerUpper          = (*WebSocketNotifier)(nil)
	_ caddy.Validator             = (*WebSocketNotifier)(nil)
	_ caddyhttp.MiddlewareHandler = (*WebSocketNotifier)(nil)
	_ caddyfile.Unmarshaler       = (*WebSocketNotifier)(nil)
)
