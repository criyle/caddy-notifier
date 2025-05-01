package caddynotifier

import (
	"net/http"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/gorilla/websocket"
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
	Upstream string `json:"upstream,omitempty"`

	upgrader *websocket.Upgrader
}

// CaddyModule returns the Caddy module information.
func (WebSocketNotifier) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.websocket_notifier",
		New: func() caddy.Module { return new(WebSocketNotifier) },
	}
}

// Provision implements caddy.Provisioner.
func (m *WebSocketNotifier) Provision(ctx caddy.Context) error {
	// TODO
	m.upgrader = &websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		//EnableCompression: true,
	}
	return nil
}

// Validate implements caddy.Validator.
func (m *WebSocketNotifier) Validate() error {
	// TODO
	return nil
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (m *WebSocketNotifier) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// TODO
	conn, err := m.upgrader.Upgrade(w, r, w.Header())
	if err != nil {
		return err
	}
	_ = conn
	return nil
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler.
func (m *WebSocketNotifier) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume directive name

	// require an argument
	if !d.NextArg() {
		return d.ArgErr()
	}

	// store the argument
	// TODO
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
	_ caddy.Validator             = (*WebSocketNotifier)(nil)
	_ caddyhttp.MiddlewareHandler = (*WebSocketNotifier)(nil)
	_ caddyfile.Unmarshaler       = (*WebSocketNotifier)(nil)
)
