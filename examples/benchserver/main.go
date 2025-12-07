// A websocket server that implements a simple benchmarking protocol.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/goccy/go-json"
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if pprof {
		go func() {
			logger.Info("starting pprof server", "addr", "http://127.0.0.1:6060")
			log.Fatal(http.ListenAndServe("127.0.0.1:6060", nil))
		}()
	}

	mux := http.NewServeMux()

	// 1. Connection speed test
	//
	// The client connects to the server, gets a 101 status response, and then
	// closes the connection.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = websocket.Accept(w, r, websocket.Options{})
	})

	// 2. Text message test
	//
	// The client sends {randomText}, and the server responds with
	// {randomText}.
	mux.HandleFunc("/plain", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, websocket.Options{
			Hooks: hooks,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("websocket handshake failed: %s", err), http.StatusBadRequest)
			logger.ErrorContext(r.Context(), "websocket handshake failed", "err", err)
			return
		}
		ws.Handle(r.Context(), websocket.EchoHandler)
	})

	// 3. JSON message test
	//
	// The client sends {"number": {randomNumber}} and the server responds as
	// follows:
	//
	// If the number is odd, the server sends {"number": {randomNumber + 1}}.
	//
	// If the number is even, the server sends {"number": {randomNumber + 2}}.
	type jsonMessage struct {
		Number int `json:"number"`
	}
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, websocket.Options{})
		if err != nil {
			http.Error(w, fmt.Sprintf("websocket handshake failed: %s", err), http.StatusBadRequest)
			logger.ErrorContext(r.Context(), "websocket handshake failed", "err", err)
			return
		}
		ws.Handle(r.Context(), func(_ context.Context, msg *websocket.Message) (*websocket.Message, error) {
			var m jsonMessage
			if err := json.Unmarshal(msg.Payload, &m); err != nil {
				return nil, err
			}
			if m.Number%2 == 0 {
				m.Number += 2
			} else {
				m.Number++
			}
			msg.Payload, _ = json.MarshalNoEscape(&m)
			return msg, nil
		})
	})

	// 4. Binary tessage test
	//
	// The client sends a number in an Int32Array message.
	//
	// If the number is odd, the server responds with number+1.
	//
	// If the number is even, the server responds with number+2.
	mux.HandleFunc("/binary", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, websocket.Options{})
		if err != nil {
			http.Error(w, fmt.Sprintf("websocket handshake failed: %s", err), http.StatusBadRequest)
			logger.ErrorContext(r.Context(), "websocket handshake failed", "err", err)
			return
		}
		ws.Handle(r.Context(), func(_ context.Context, msg *websocket.Message) (*websocket.Message, error) {
			if len(msg.Payload) != 4 {
				return nil, fmt.Errorf("invalid payload length: %d", len(msg.Payload))
			}
			val := binary.LittleEndian.Uint32(msg.Payload)
			if val%2 == 0 {
				val += 2
			} else {
				val++
			}
			binary.LittleEndian.PutUint32(msg.Payload[0:], val)
			return msg, nil
		})
	})

	addr := getListenAddr()
	logger.Info("starting benchserver", "addr", "http://"+addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func getListenAddr() string {
	if addr := os.Getenv("LISTEN_ADDR"); addr != "" {
		return addr
	}
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return "127.0.0.1:9001"
}

func getLogger(debug bool) *slog.Logger {
	if !debug {
		return nil
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}
