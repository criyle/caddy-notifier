package caddynotifier

import (
	"bytes"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/criyle/caddy-notifier/shorty"
	"github.com/gorilla/websocket"
)

var (
	websocketInboundBytes    atomic.Int64
	websocketOutboundBytes   atomic.Int64
	websocketCompressedBytes atomic.Int64
)

type webSocket[T any] struct {
	conn         *websocket.Conn
	outboundChan chan<- *outboundMessage
	config       *websocketConfig
	err          error
	done         <-chan struct{}
	id           string
}

type inboundMessage[T any] struct {
	// conn indicate the sender of the message
	conn *webSocket[T]
	// value contains the message content
	value *T
	// err contains error if read error happens, when receiving this message, means the conn is dead
	err error
}

type outboundMessage struct {
	messageType     int
	data            []byte
	preparedMessage *websocket.PreparedMessage
}

type websocketConfig struct {
	writeWait        time.Duration
	pongWait         time.Duration
	pingInterval     time.Duration // must less than pongWait
	maxMessageSize   int64
	chanSize         int
	shortyResetCount int
	shorty           bool
	metrics          bool
	pingText         bool
}

func newWebSocket[T any](conn *websocket.Conn, inboundChan chan<- inboundMessage[T], conf *websocketConfig) *webSocket[T] {
	outC := make(chan *outboundMessage, conf.chanSize)
	done := make(chan struct{})
	w := &webSocket[T]{
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

func (w *webSocket[T]) Close() error {
	return w.conn.Close()
}

func (w *webSocket[T]) readLoop(done chan struct{}, inboundChan chan<- inboundMessage[T]) {
	defer func() {
		inboundChan <- inboundMessage[T]{
			conn: w,
			err:  w.err,
		}
		w.conn.Close()
		close(done)
	}()

	w.conn.SetReadLimit(w.config.maxMessageSize)
	w.conn.SetReadDeadline(time.Now().Add(w.config.pongWait))
	w.conn.SetPongHandler(func(string) error { w.conn.SetReadDeadline(time.Now().Add(w.config.pongWait)); return nil })

	var sh *shorty.Shorty

	for {
		_, data, err := w.conn.ReadMessage()
		if err != nil {
			w.err = err
			return
		}
		if w.config.metrics {
			websocketInboundBytes.Add(int64(len(data)))
		}
		// ignore ping
		if bytes.Equal(data, []byte("ping")) {
			w.conn.WriteMessage(websocket.TextMessage, []byte("pong"))
			continue
		}
		// update deadline with pong
		if bytes.Equal(data, []byte("pong")) {
			w.conn.SetReadDeadline(time.Now().Add(w.config.pongWait))
			continue
		}
		// support shorty
		if bytes.Equal(data, []byte("shorty")) {
			if sh == nil {
				sh = shorty.NewShorty(10)
			} else {
				sh.Reset(true)
			}
			continue
		}
		if sh != nil {
			data = sh.Inflate(data)
		}
		v := new(T)
		if err := json.NewDecoder(bytes.NewBuffer(data)).Decode(v); err != nil {
			w.err = err
			return
		}
		inboundChan <- inboundMessage[T]{
			conn:  w,
			value: v,
		}
	}
}

func (w *webSocket[T]) writeLoop(done chan struct{}, outboundChan chan *outboundMessage) {
	ticker := time.NewTicker(w.config.pingInterval)
	defer func() {
		ticker.Stop()
		w.conn.Close()
	}()

	var (
		sh    *shorty.Shorty
		count int
	)
	resetShorty := func() error {
		count = 0
		sh.Reset(true)
		return w.conn.WriteMessage(websocket.TextMessage, []byte("shorty"))
	}
	if w.config.shorty {
		sh = shorty.NewShorty(10)
		resetShorty()
	}

	for {
		select {
		case v, ok := <-outboundChan:
			w.conn.SetWriteDeadline(time.Now().Add(w.config.writeWait))
			if !ok {
				w.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if v.preparedMessage != nil {
				w.conn.WritePreparedMessage(v.preparedMessage)
			} else {
				t := v.messageType
				data := v.data
				if w.config.metrics {
					websocketOutboundBytes.Add(int64(len(data)))
				}
				if sh != nil {
					if w.config.shortyResetCount > 0 && count > w.config.shortyResetCount {
						if err := resetShorty(); err != nil {
							return
						}
					}
					data = sh.Deflate(v.data)
					t = websocket.BinaryMessage
					count++
					if w.config.metrics {
						websocketCompressedBytes.Add(int64(len(data)))
					}
				}
				if err := w.conn.WriteMessage(t, data); err != nil {
					return
				}
			}

		case <-ticker.C:
			w.conn.SetWriteDeadline(time.Now().Add(w.config.writeWait))
			if err := w.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
			if w.config.pingText {
				if err := w.conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
					return
				}
			}

		case <-done:
			return
		}
	}
}
