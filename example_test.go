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
			MaxFragmentSize: 16 * 1024,   // 16KiB
			MaxMessageSize:  1024 * 1024, // 1MiB
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
