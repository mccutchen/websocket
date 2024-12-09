package websocket_test

import (
	"log"
	"net/http"
	"time"

	"github.com/mccutchen/websocket"
)

func ExampleEchoHandler() {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, websocket.Options{
			ReadTimeout:     500 * time.Millisecond,
			WriteTimeout:    500 * time.Millisecond,
			MaxFragmentSize: 10 * 1024,
			MaxMessageSize:  512 * 1024,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ws.Serve(r.Context(), websocket.EchoHandler)
	})
	if err := http.ListenAndServe(":8080", handler); err != nil {
		log.Fatalf("error starting server: %v", err)
	}
}
