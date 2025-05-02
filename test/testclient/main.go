package main

import (
	"bytes"
	"encoding/json"
	"log"
	"strconv"
	"time"

	caddynotifier "github.com/criyle/caddy-notifier"
	"github.com/gorilla/websocket"
)

func runClient() {
	c, _, err := websocket.DefaultDialer.Dial("ws://localhost:6080/ws", nil)
	if err != nil {
		log.Println("dial: ", err)
		return
	}
	defer c.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			var resp caddynotifier.SubscriberResponse
			err := c.ReadJSON(&resp)
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
			err := c.WriteJSON(req)
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
