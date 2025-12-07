// A basic websocket echo server.
package main

import (
	"flag"
	"log"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	"github.com/mccutchen/websocket"
)

func main() {
	var (
		debug bool
		pprof bool
	)
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.BoolVar(&pprof, "pprof", false, "Enable pprof endpoints on port 6060")
	flag.Parse()

	logger := getLogger(debug)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, websocket.Options{
			Logger:       getLogger(debug),
			ReadTimeout:  60 * time.Second,
			WriteTimeout: 1 * time.Second,
			// Allow very large frames and messages to allow testing with
			// the autobahn websocket test suite.
			//
			// Prefer much lower limits when your application allows it.
			MaxFrameSize:   16 << 20, // 16 MiB
			MaxMessageSize: 16 << 20,
		})
		if err != nil {
			logger.ErrorContext(r.Context(), "websocket handshake failed", "error", err.Error())
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		logger.InfoContext(r.Context(), "websocket handshake completed, starting echo handler", "client-key", ws.ClientKey())
		ws.Handle(r.Context(), websocket.EchoHandler)
	})

	if pprof {
		logger.Info("pprof endponts enabled at /debug/pproff/")
		mux.Handle("/debug/pprof/", http.DefaultServeMux)
	}

	addr := getListenAddr()
	logger.Info("starting echoserver", "addr", "http://"+addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func getLogger(debug bool) *slog.Logger {
	if !debug {
		return nil
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}

func getListenAddr() string {
	if addr := os.Getenv("LISTEN_ADDR"); addr != "" {
		return addr
	}
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return "127.0.0.1:8080"
}
