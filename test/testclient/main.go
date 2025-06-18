package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	caddynotifier "github.com/criyle/caddy-notifier"
	"github.com/criyle/caddy-notifier/shorty"
	"github.com/gorilla/websocket"
)

func runClient(token string, seq uint64) {
	h := make(http.Header)
	h.Add("User-Agent", "testclient")
	c, _, err := websocket.DefaultDialer.Dial("ws://localhost:6080/ws?shorty=on", h)
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
				log.Println("shorty")
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
			case "verify":
				log.Println("verify: ", resp.Accept, resp.ResumeToken)
				token = resp.ResumeToken

			case "resume_success":
				log.Println("resume_success: ", resp.Channels)

			case "event":
				v := make(map[string]any)
				err := json.NewDecoder(bytes.NewReader(resp.Payload)).Decode(&v)
				if err != nil {
					log.Println("json decode: ", err)
				} else {
					log.Println("event: ", resp.Seq, v)
					seq = resp.Seq
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

	if token != "" {
		req := &caddynotifier.SubscriberRequest{
			Operation:   "resume",
			ResumeToken: token,
			Seq:         seq,
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
	i := 0

	for {
		select {
		case <-done:
			log.Println("close")
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
			i++
			if i > 3 {
				go runClient(token, seq)
				return
			}
		}
	}
}

func main() {
	for range [1]struct{}{} {
		go runClient("", 0)
	}
	select {}
}
