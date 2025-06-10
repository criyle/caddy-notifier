package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"

	caddynotifier "github.com/criyle/caddy-notifier"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func ws(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade: ", err)
	}
	defer c.Close()
	log.Println("connect: ", c.RemoteAddr())
	i := 0

	for {
		var req caddynotifier.NotifierRequest
		err := c.ReadJSON(&req)
		if err != nil {
			log.Println("readjson: ", err)
			return
		}
		switch req.Operation {
		case "subscribe":
			res := &caddynotifier.NotifierResponse{
				Operation:    "verify",
				ConnectionId: req.ConnectionId,
				Credential:   req.Credential,
				Accept:       req.Channels,
			}
			err := c.WriteJSON(res)
			if err != nil {
				log.Println("writejson: ", err)
				return
			}

			var buf bytes.Buffer
			err = json.NewEncoder(&buf).Encode(map[string]any{
				"connection_id": res.ConnectionId,
			})
			if err != nil {
				log.Println("json encoder: ", err)
				return
			}
			// event
			event := &caddynotifier.NotifierResponse{
				Operation: "event",
				Channels:  req.Channels,
				Payload:   json.RawMessage(buf.Bytes()),
			}
			err = c.WriteJSON(event)
			if err != nil {
				log.Println("writejson2: ", err)
				return
			}
			if i%50 == 0 {
				res = &caddynotifier.NotifierResponse{
					Operation:  "deauthorize",
					Credential: req.Credential,
				}
				err = c.WriteJSON(res)
				if err != nil {
					log.Println("writejson2: ", err)
					return
				}
				log.Println("deauthorize")
			}
		case "resume":
			log.Println("resume", req.Channels)
		}
		i++
	}
}

func main() {
	http.HandleFunc("/ws", ws)
	log.Println(http.ListenAndServe(":6081", nil))
}
