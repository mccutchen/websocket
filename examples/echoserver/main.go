// A basic websocket echo server.
package main

import (
	"context"
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if pprof {
		go func() {
			logger.Info("starting pprof server", "addr", "http://127.0.0.1:6060")
			log.Fatal(http.ListenAndServe("127.0.0.1:6060", nil))
		}()
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var hooks websocket.Hooks
		if debug {
			hooks = newDebugHooks(r.Context(), logger)
		}
		ws, err := websocket.Accept(w, r, websocket.Options{
			Hooks:        hooks,
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
		ws.Serve(r.Context(), websocket.EchoHandler)
	})
	addr := getListenAddr()
	logger.Info("starting echoserver", "addr", "http://"+addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}

func newDebugHooks(ctx context.Context, logger *slog.Logger) websocket.Hooks {
	levelForErr := func(err error) slog.Level {
		if err != nil {
			return slog.LevelError
		}
		return slog.LevelInfo
	}
	return websocket.Hooks{
		OnClose: func(key websocket.ClientKey, code websocket.StatusCode, err error) {
			logger.Log(ctx, levelForErr(err), "OnClose", "client", key, "code", code, "err", err)
		},
		OnReadError: func(key websocket.ClientKey, err error) {
			logger.ErrorContext(ctx, "OnReadError", "client", key, "err", err)
		},
		OnReadFrame: func(key websocket.ClientKey, frame *websocket.Frame) {
			logger.InfoContext(ctx, "OnReadFrame", "client", key, "frame", frame)
		},
		OnReadMessage: func(key websocket.ClientKey, msg *websocket.Message) {
			logger.InfoContext(ctx, "OnReadMessage", "client", key, "msg", msg)
		},
		OnWriteError: func(key websocket.ClientKey, err error) {
			logger.ErrorContext(ctx, "OnWriteError", "client", key, "err", err)
		},
		OnWriteFrame: func(key websocket.ClientKey, frame *websocket.Frame) {
			logger.InfoContext(ctx, "OnWriteFrame", "client", key, "frame", frame)
		},
		OnWriteMessage: func(key websocket.ClientKey, msg *websocket.Message) {
			logger.InfoContext(ctx, "OnWriteMessage", "client", key, "msg", msg)
		},
	}
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
