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
				Operation:    "accept",
				ConnectionId: req.ConnectionId,
				Channels:     req.Channels,
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
			// notify
			notify := &caddynotifier.NotifierResponse{
				Operation: "notify",
				Channels:  req.Channels,
				Payload:   json.RawMessage(buf.Bytes()),
			}
			err = c.WriteJSON(notify)
			if err != nil {
				log.Println("writejson2: ", err)
				return
			}
		}
	}
}

func main() {
	http.HandleFunc("/ws", ws)
	log.Println(http.ListenAndServe(":6081", nil))
}
