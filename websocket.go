// Package websocket implements a basic websocket server.
package websocket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Handler handles a single websocket message. If the returned message is
// non-nil, it will be sent to the client. If an error is returned, the
// connection will be closed.
type Handler func(ctx context.Context, msg *Message) (*Message, error)

// EchoHandler is a Handler that echoes each incoming message back to the
// client.
var EchoHandler Handler = func(_ context.Context, msg *Message) (*Message, error) {
	return msg, nil
}

// Default options.
const (
	DefaultMaxFragmentSize int = 1024 * 16  // 16KiB
	DefaultMaxMessageSize  int = 1024 * 256 // 256KiB
)

// Options define the limits imposed on a websocket connection.
type Options struct {
	Hooks           Hooks
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	MaxFragmentSize int
	MaxMessageSize  int
}

type deadliner interface {
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// Websocket is a websocket connection.
type Websocket struct {
	// connection state
	conn     io.ReadWriteCloser
	closedCh chan struct{}
	server   bool

	// observability
	clientKey ClientKey
	hooks     Hooks

	// limits
	readTimeout     time.Duration
	writeTimeout    time.Duration
	maxFragmentSize int
	maxMessageSize  int
}

// Accept handles the initial HTTP-based handshake and upgrades the TCP
// connection to a websocket connection.
func Accept(w http.ResponseWriter, r *http.Request, opts Options) (*Websocket, error) {
	clientKey, err := handshake(w, r)
	if err != nil {
		return nil, fmt.Errorf("websocket: accept: handshake failed: %w", err)
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		panic("websocket: accept: server does not support hijacking")
	}

	conn, _, err := hj.Hijack()
	if err != nil {
		panic(fmt.Errorf("websocket: accept: hijack failed: %s", err))
	}

	return New(conn, clientKey, opts), nil
}

// New manually creates a new websocket connection. Caller is responsible for
// completing initial handshake before creating a websocket connection.
//
// Prefer Accept() when possible.
func New(src io.ReadWriteCloser, clientKey ClientKey, opts Options) *Websocket {
	setDefaults(&opts)
	if opts.ReadTimeout != 0 || opts.WriteTimeout != 0 {
		if _, ok := src.(deadliner); !ok {
			panic("ReadTimeout and WriteTimeout may only be used when input source supports setting read/write deadlines")
		}
	}
	return &Websocket{
		conn:            src,
		closedCh:        make(chan struct{}),
		server:          true,
		clientKey:       clientKey,
		hooks:           opts.Hooks,
		readTimeout:     opts.ReadTimeout,
		writeTimeout:    opts.WriteTimeout,
		maxFragmentSize: opts.MaxFragmentSize,
		maxMessageSize:  opts.MaxMessageSize,
	}
}

// setDefaults sets the default values for any unset options.
func setDefaults(opts *Options) {
	if opts.MaxFragmentSize <= 0 {
		opts.MaxFragmentSize = DefaultMaxFragmentSize
	}
	if opts.MaxMessageSize <= 0 {
		opts.MaxMessageSize = DefaultMaxMessageSize
	}
	setupHooks(&opts.Hooks)
}

// handshake validates the request and performs the WebSocket handshake, after
// which only websocket frames should be written to the underlying connection.
func handshake(w http.ResponseWriter, r *http.Request) (ClientKey, error) {
	if strings.ToLower(r.Header.Get("Upgrade")) != "websocket" {
		return "", fmt.Errorf("missing required `Upgrade: websocket` header")
	}
	if v := r.Header.Get("Sec-Websocket-Version"); v != requiredVersion {
		return "", fmt.Errorf("only websocket version %q is supported, got %q", requiredVersion, v)
	}

	clientKey := r.Header.Get("Sec-Websocket-Key")
	if clientKey == "" {
		return "", fmt.Errorf("missing required `Sec-Websocket-Key` header")
	}

	w.Header().Set("Connection", "upgrade")
	w.Header().Set("Upgrade", "websocket")
	w.Header().Set("Sec-Websocket-Accept", acceptKey(clientKey))
	w.WriteHeader(http.StatusSwitchingProtocols)
	return ClientKey(clientKey), nil
}

// ReadMessage reads a single websocket message from the connection,
// handling fragments and control frames automatically. frames are handled
// automatically. The connection will be closed on any error.
func (ws *Websocket) ReadMessage(ctx context.Context) (*Message, error) {
	var msg *Message
	for {
		select {
		case <-ws.closedCh:
			return nil, io.EOF
		case <-ctx.Done():
			_ = ws.Close()
			return nil, ctx.Err()
		default:
			ws.resetReadDeadline()
		}

		frame, err := ReadFrame(ws.conn)
		if err != nil {
			code := StatusServerError
			if errors.Is(err, io.EOF) {
				code = StatusNormalClosure
			}
			return nil, ws.closeOnReadError(code, err)
		}
		if err := validateFrame(frame, ws.maxFragmentSize, ws.server); err != nil {
			return nil, ws.closeOnReadError(StatusProtocolError, err)
		}
		ws.hooks.OnReadFrame(ws.clientKey, frame)

		switch frame.Opcode {
		case OpcodeBinary, OpcodeText:
			if msg != nil {
				return nil, ws.closeOnReadError(StatusProtocolError, ErrContinuationExpected)
			}
			msg = &Message{
				Binary:  frame.Opcode == OpcodeBinary,
				Payload: frame.Payload,
			}
		case OpcodeContinuation:
			if msg == nil {
				return nil, ws.closeOnReadError(StatusProtocolError, ErrInvalidContinuation)
			}
			msg.Payload = append(msg.Payload, frame.Payload...)
			if len(msg.Payload) > ws.maxMessageSize {
				return nil, ws.closeOnReadError(StatusTooLarge, fmt.Errorf("message size %d exceeds maximum of %d bytes", len(msg.Payload), ws.maxMessageSize))
			}
		case OpcodeClose:
			_ = ws.Close()
			return nil, io.EOF
		case OpcodePing:
			frame.Opcode = OpcodePong
			ws.hooks.OnWriteFrame(ws.clientKey, frame)
			if err := WriteFrame(ws.conn, frame); err != nil {
				return nil, err
			}
			continue
		case OpcodePong:
			continue // no-op
		default:
			return nil, ws.closeOnReadError(StatusProtocolError, fmt.Errorf("unsupported opcode: %v", frame.Opcode))
		}

		if frame.Fin {
			ws.hooks.OnReadMessage(ws.clientKey, msg)
			if !msg.Binary && !utf8.Valid(msg.Payload) {
				return nil, ws.closeOnReadError(StatusUnsupportedPayload, ErrInvalidUT8)
			}
			return msg, nil
		}
	}
}

// WriteMessage writes a single websocket message to the connection, after
// splitting it into fragments (if necessary). The connection will be closed
// on any error.
func (ws *Websocket) WriteMessage(ctx context.Context, msg *Message) error {
	ws.hooks.OnWriteMessage(ws.clientKey, msg)
	for _, frame := range messageFrames(msg, ws.maxFragmentSize) {
		ws.hooks.OnWriteFrame(ws.clientKey, frame)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ws.closedCh:
			return io.EOF
		default:
			ws.resetWriteDeadline()
		}
		if err := WriteFrame(ws.conn, frame); err != nil {
			return ws.closeOnWriteError(StatusServerError, err)
		}
	}
	return nil
}

// Serve is a high-level convienience method for request-response style
// websocket connections, where handler is called for each incoming message
// and its return value is sent back to the client.
func (ws *Websocket) Serve(ctx context.Context, handler Handler) {
	for {
		msg, err := ws.ReadMessage(ctx)
		if err != nil {
			// an error in Read() closes the connection
			return
		}

		resp, err := handler(ctx, msg)
		if err != nil {
			_ = ws.closeWithError(StatusServerError, err)
			return
		}
		if resp != nil {
			if err := ws.WriteMessage(ctx, resp); err != nil {
				// an error in Write() closes the connection
				return
			}
		}
	}
}

// Close closes a websocket connection.
func (ws *Websocket) Close() error {
	return ws.closeWithError(StatusNormalClosure, nil)
}

func (ws *Websocket) closeWithError(code StatusCode, err error) error {
	ws.hooks.OnClose(ws.clientKey, code, err)
	close(ws.closedCh)
	if err := writeCloseFrame(ws.conn, code, err); err != nil {
		return fmt.Errorf("websocket: failed to write close frame: %w", err)
	}
	if err := ws.conn.Close(); err != nil {
		return fmt.Errorf("websocket: failed to close connection: %s", err)
	}
	return nil
}

func (ws *Websocket) closeOnReadError(code StatusCode, err error) error {
	ws.hooks.OnReadError(ws.clientKey, err)
	_ = ws.closeWithError(code, err)
	return err
}

func (ws *Websocket) closeOnWriteError(code StatusCode, err error) error {
	ws.hooks.OnWriteError(ws.clientKey, err)
	_ = ws.closeWithError(code, err)
	return err
}

func (ws *Websocket) resetReadDeadline() {
	if ws.readTimeout <= 0 {
		return
	}
	if err := ws.conn.(deadliner).SetReadDeadline(time.Now().Add(ws.readTimeout)); err != nil {
		panic(fmt.Sprintf("websocket: failed to set read deadline: %s", err))
	}
}

func (ws *Websocket) resetWriteDeadline() {
	if ws.writeTimeout <= 0 {
		return
	}
	if err := ws.conn.(deadliner).SetWriteDeadline(time.Now().Add(ws.writeTimeout)); err != nil {
		panic(fmt.Sprintf("websocket: failed to set write deadline: %s", err))
	}
}
