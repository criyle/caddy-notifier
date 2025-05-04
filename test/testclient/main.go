package main

import (
	"bytes"
	"encoding/json"
	"log"
	"strconv"
	"time"

	caddynotifier "github.com/criyle/caddy-notifier"
	"github.com/criyle/caddy-notifier/shorty"
	"github.com/gorilla/websocket"
)

func runClient() {
	c, _, err := websocket.DefaultDialer.Dial("ws://localhost:6080/ws", nil)
	if err != nil {
		log.Println("dial: ", err)
		return
	}
	defer c.Close()

	sh := shorty.NewShorty(10)
	var rsh *shorty.Shorty

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				log.Println("read: ", err)
				return
			}
			if bytes.Equal(data, []byte("shorty")) {
				if rsh == nil {
					rsh = shorty.NewShorty(10)
				} else {
					rsh.Reset(true)
				}
				continue
			}
			if rsh != nil {
				data = rsh.Inflate(data)
			}

			var resp caddynotifier.SubscriberResponse
			err = json.NewDecoder(bytes.NewBuffer(data)).Decode(&resp)
			if err != nil {
				log.Println("read json: ", err)
				return
			}
			switch resp.Operation {
			case "subscribe":
				log.Println("subscribe: ", resp.Channels)
			case "event":
				v := make(map[string]any)
				err := json.NewDecoder(bytes.NewReader(resp.Payload)).Decode(&v)
				if err != nil {
					log.Println("json decode: ", err)
				} else {
					log.Println("event: ", v)
				}
			}
		}
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	if err := c.WriteMessage(websocket.TextMessage, []byte("shorty")); err != nil {
		log.Println("shorty")
		return
	}

	for {
		select {
		case <-done:
			return
		case t := <-ticker.C:
			req := &caddynotifier.SubscriberRequest{
				Operation:  "subscribe",
				Credential: "cred",
				Channels:   []string{"all", strconv.Itoa(int(t.Unix()))},
			}
			buf := new(bytes.Buffer)
			if err := json.NewEncoder(buf).Encode(req); err != nil {
				log.Println("json: ", err)
				return
			}
			err := c.WriteMessage(websocket.BinaryMessage, sh.Deflate(buf.Bytes()))
			if err != nil {
				log.Println("write json: ", err)
				return
			}
			log.Println("send: ", req)
		}
	}
}

func main() {
	for range [10]struct{}{} {
		go runClient()
	}
	select {}
}
