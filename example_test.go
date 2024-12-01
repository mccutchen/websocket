package websocket_test

import (
	"net/http"
	"time"

	"github.com/mccutchen/websocket"
)

func ExampleEchoHandler() {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		ws := websocket.New(w, r, websocket.Limits{
			MaxDuration:     10 * time.Second,
			MaxFragmentSize: 10 * 1024,
			MaxMessageSize:  512 * 1024,
		})
		if err := ws.Handshake(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ws.Serve(websocket.EchoHandler)
	})
	http.ListenAndServe(":8080", mux)
}
