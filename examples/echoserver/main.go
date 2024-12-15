// A basic websocket echo server.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/mccutchen/websocket"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, websocket.Options{
			ReadTimeout:     60 * time.Second,
			WriteTimeout:    1 * time.Second,
			MaxFragmentSize: 1024 * 1024,
			MaxMessageSize:  1024 * 1024,
			Hooks:           newDebugHooks(r.Context(), logger),
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("websocket handshake failed: %s", err), http.StatusBadRequest)
			return
		}
		conn.Serve(r.Context(), websocket.EchoHandler)
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
