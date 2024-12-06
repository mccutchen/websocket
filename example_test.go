package websocket_test

import (
	"net/http"
	"time"

	"github.com/mccutchen/websocket"
)

func ExampleEchoHandler() {
	http.ListenAndServe(":8080", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, websocket.Limits{
			MaxDuration:     10 * time.Second,
			MaxFragmentSize: 10 * 1024,
			MaxMessageSize:  512 * 1024,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ws.Serve(r.Context(), websocket.EchoHandler)
	}))
}
