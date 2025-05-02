package caddynotifier

import (
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"
)

type webSocket[T any, V any] struct {
	conn         *websocket.Conn
	outboundChan chan<- *T
	config       *websocketConfig
	err          error
	done         <-chan struct{}
	id           string
}

type inboundMessage[T any, V any] struct {
	// conn indicate the sender of the message
	conn *webSocket[T, V]
	// value contains the message content
	value *V
	// err contains error if read error happens, when receiving this message, means the conn is dead
	err error
}

type websocketConfig struct {
	writeWait      time.Duration
	pongWait       time.Duration
	pingInterval   time.Duration // must less than pongWait
	maxMessageSize int64
	chanSize       int
}

func newWebSocket[T any, V any](conn *websocket.Conn, inboundChan chan<- inboundMessage[T, V], conf *websocketConfig) *webSocket[T, V] {
	outC := make(chan *T, conf.chanSize)
	done := make(chan struct{})
	w := &webSocket[T, V]{
		conn:         conn,
		outboundChan: outC,
		config:       conf,
		done:         done,
		id:           conn.RemoteAddr().String(),
	}
	go w.readLoop(done, inboundChan)
	go w.writeLoop(done, outC)

	return w
}

func (w *webSocket[T, V]) Close() error {
	return w.conn.Close()
}

func (w *webSocket[T, V]) readLoop(done chan struct{}, inboundChan chan<- inboundMessage[T, V]) {
	defer func() {
		inboundChan <- inboundMessage[T, V]{
			conn: w,
			err:  w.err,
		}
		w.conn.Close()
		close(done)
	}()

	w.conn.SetReadLimit(w.config.maxMessageSize)
	w.conn.SetReadDeadline(time.Now().Add(w.config.pongWait))
	w.conn.SetPongHandler(func(string) error { w.conn.SetReadDeadline(time.Now().Add(w.config.pongWait)); return nil })

	for {
		_, r, err := w.conn.NextReader()
		if err != nil {
			w.err = err
			return
		}
		v := new(V)
		if err := json.NewDecoder(r).Decode(v); err != nil {
			w.err = err
			return
		}
		inboundChan <- inboundMessage[T, V]{
			conn:  w,
			value: v,
		}
	}
}

func (w *webSocket[T, V]) writeLoop(done chan struct{}, outboundChan chan *T) {
	ticker := time.NewTicker(w.config.pingInterval)
	defer func() {
		ticker.Stop()
		w.conn.Close()
	}()

	for {
		select {
		case v, ok := <-outboundChan:
			w.conn.SetWriteDeadline(time.Now().Add(w.config.writeWait))
			if !ok {
				w.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			wr, err := w.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			if err := json.NewEncoder(wr).Encode(v); err != nil {
				return
			}
			if err := wr.Close(); err != nil {
				return
			}

		case <-ticker.C:
			w.conn.SetWriteDeadline(time.Now().Add(w.config.writeWait))
			if err := w.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-done:
			return
		}
	}
}
