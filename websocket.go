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

// Default options.
const (
	DefaultMaxFrameSize   int = 16 << 10  // 16KiB
	DefaultMaxMessageSize int = 256 << 10 // 256KiB
)

// Mode enalbes server or client behavior
type Mode bool

// Valid modes
const (
	ServerMode Mode = false
	ClientMode      = true
)

// Options define the limits imposed on a websocket connection.
type Options struct {
	Hooks          Hooks
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	MaxFrameSize   int
	MaxMessageSize int
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
	mode     Mode

	// observability
	clientKey ClientKey
	hooks     Hooks

	// limits
	readTimeout    time.Duration
	writeTimeout   time.Duration
	maxFrameSize   int
	maxMessageSize int
}

// Accept handles the initial HTTP-based handshake and upgrades the TCP
// connection to a websocket connection.
func Accept(w http.ResponseWriter, r *http.Request, opts Options) (*Websocket, error) {
	clientKey, err := Handshake(w, r)
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

	return New(conn, clientKey, ServerMode, opts), nil
}

// New is a low-level API that manually creates a new websocket connection.
// Caller is responsible for completing initial handshake before creating a
// websocket connection.
//
// Prefer the higher-level [Accept] API when possible. See also [Handshake] if
// using New directly.
func New(src io.ReadWriteCloser, clientKey ClientKey, mode Mode, opts Options) *Websocket {
	setDefaults(&opts)
	if opts.ReadTimeout != 0 || opts.WriteTimeout != 0 {
		if _, ok := src.(deadliner); !ok {
			panic("ReadTimeout and WriteTimeout may only be used when input source supports setting read/write deadlines")
		}
	}
	return &Websocket{
		conn:           src,
		closedCh:       make(chan struct{}),
		mode:           mode,
		clientKey:      clientKey,
		hooks:          opts.Hooks,
		readTimeout:    opts.ReadTimeout,
		writeTimeout:   opts.WriteTimeout,
		maxFrameSize:   opts.MaxFrameSize,
		maxMessageSize: opts.MaxMessageSize,
	}
}

// setDefaults sets the default values for any unset options.
func setDefaults(opts *Options) {
	if opts.MaxFrameSize <= 0 {
		opts.MaxFrameSize = DefaultMaxFrameSize
	}
	if opts.MaxMessageSize <= 0 {
		opts.MaxMessageSize = DefaultMaxMessageSize
	}
	setupHooks(&opts.Hooks)
}

// Handshake is a low-level helper that validates the request and performs
// the WebSocket Handshake, after which only websocket frames should be
// written to the underlying connection.
//
// Prefer the higher-level [Accept] API when possible.
func Handshake(w http.ResponseWriter, r *http.Request) (ClientKey, error) {
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

// ReadMessage reads a single [Message] from the connection, handling
// fragments and control frames automatically. The connection will be closed
// on any error.
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

		frame, err := ReadFrame(ws.conn, ws.mode, ws.maxFrameSize)
		if err != nil {
			return nil, ws.closeOnReadError(err)
		}
		if err := validateFrame(frame); err != nil {
			return nil, ws.closeOnReadError(err)
		}
		ws.hooks.OnReadFrame(ws.clientKey, frame)

		opcode := frame.Opcode()
		switch opcode {
		case OpcodeBinary, OpcodeText:
			if msg != nil {
				return nil, ws.closeOnReadError(ErrContinuationExpected)
			}
			msg = &Message{
				Binary:  opcode == OpcodeBinary,
				Payload: frame.Payload,
			}
		case OpcodeContinuation:
			if msg == nil {
				return nil, ws.closeOnReadError(ErrContinuationUnexpected)
			}
			if len(msg.Payload)+len(frame.Payload) > ws.maxMessageSize {
				return nil, ws.closeOnReadError(ErrMessageTooLarge)
			}
			msg.Payload = append(msg.Payload, frame.Payload...)
		case OpcodeClose:
			_ = ws.Close()
			return nil, io.EOF
		case OpcodePing:
			frame = NewFrame(OpcodePong, true, frame.Payload)
			ws.hooks.OnWriteFrame(ws.clientKey, frame)
			if err := WriteFrame(ws.conn, ws.mask(), frame); err != nil {
				return nil, err
			}
			continue
		case OpcodePong:
			continue // no-op
		default:
			return nil, ws.closeOnReadError(ErrOpcodeUnknown)
		}

		if frame.Fin() {
			if !msg.Binary && !utf8.Valid(msg.Payload) {
				return nil, ws.closeOnReadError(ErrInvalidFramePayload)
			}
			ws.hooks.OnReadMessage(ws.clientKey, msg)
			return msg, nil
		}
	}
}

// WriteMessage writes a single [Message] to the connection, after splitting
// it into fragments (if necessary). The connection will be closed on any
// error.
func (ws *Websocket) WriteMessage(ctx context.Context, msg *Message) error {
	ws.hooks.OnWriteMessage(ws.clientKey, msg)
	for _, frame := range FrameMessage(msg, ws.maxFrameSize) {
		ws.hooks.OnWriteFrame(ws.clientKey, frame)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ws.closedCh:
			return io.EOF
		default:
			ws.resetWriteDeadline()
		}
		if err := WriteFrame(ws.conn, ws.mask(), frame); err != nil {
			return ws.closeOnWriteError(err)
		}
	}
	return nil
}

// Serve is a high-level convienience method for request-response style
// websocket connections, where the given [Handler] is called for each
// incoming message and its return value is sent back to the client.
//
// See also [EchoHandler].
func (ws *Websocket) Serve(ctx context.Context, handler Handler) {
	for {
		msg, err := ws.ReadMessage(ctx)
		if err != nil {
			// an error in Read() closes the connection
			return
		}

		resp, err := handler(ctx, msg)
		if err != nil {
			_ = ws.closeWithError(err)
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

// mask returns an appropriate masking key for use when writing a message's
// frames.
func (ws *Websocket) mask() MaskingKey {
	if ws.mode == ServerMode {
		return Unmasked
	}
	return NewMaskingKey()
}

// Close closes a websocket connection.
func (ws *Websocket) Close() error {
	return ws.closeWithError(nil)
}

func (ws *Websocket) closeWithError(err error) error {
	code, reason := statusCodeForError(err)
	ws.hooks.OnClose(ws.clientKey, code, err)
	close(ws.closedCh)
	if err := WriteFrame(ws.conn, ws.mask(), NewCloseFrame(code, reason)); err != nil {
		return fmt.Errorf("websocket: failed to write close frame: %w", err)
	}
	if err := ws.conn.Close(); err != nil {
		return fmt.Errorf("websocket: failed to close connection: %s", err)
	}
	return nil
}

func (ws *Websocket) closeOnReadError(err error) error {
	ws.hooks.OnReadError(ws.clientKey, err)
	_ = ws.closeWithError(err)
	return err
}

func (ws *Websocket) closeOnWriteError(err error) error {
	ws.hooks.OnWriteError(ws.clientKey, err)
	_ = ws.closeWithError(err)
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

// ClientKey returns the [ClientKey] for a connection.
func (ws *Websocket) ClientKey() ClientKey {
	return ws.clientKey
}

// Handler handles a single websocket [Message] as part of the high level
// [Serve] request-response API.
//
// If the returned message is non-nil, it will be sent to the client. If an
// error is returned, the connection will be closed.
type Handler func(ctx context.Context, msg *Message) (*Message, error)

// EchoHandler is a [Handler] that echoes each incoming [Message] back to the
// client.
var EchoHandler Handler = func(_ context.Context, msg *Message) (*Message, error) {
	return msg, nil
}

// statusCodeForError returns an appropriate close frame status code and reason
// for the given error. If error is nil, returns a normal closure status code.
func statusCodeForError(err error) (StatusCode, string) {
	if err == nil {
		return StatusNormalClosure, ""
	}
	var protoErr *Error
	if errors.As(err, &protoErr) {
		return protoErr.code, protoErr.Error()
	}
	var (
		code   = StatusInternalError
		reason = err.Error()
	)
	switch {
	case errors.Is(err, io.EOF):
		code = StatusNormalClosure
	}
	return code, reason
}

// Hooks define the callbacks that are called during the lifecycle of a
// websocket connection.
type Hooks struct {
	// OnClose is called when the connection is closed.
	OnClose func(ClientKey, StatusCode, error)
	// OnReadError is called when a read error occurs.
	OnReadError func(ClientKey, error)
	// OnReadFrame is called when a frame is read.
	OnReadFrame func(ClientKey, *Frame)
	// OnReadMessage is called when a complete message is read.
	OnReadMessage func(ClientKey, *Message)
	// OnWriteError is called when a write error occurs.
	OnWriteError func(ClientKey, error)
	// OnWriteFrame is called when a frame is written.
	OnWriteFrame func(ClientKey, *Frame)
	// OnWriteMessage is called when a complete message is written.
	OnWriteMessage func(ClientKey, *Message)
}

// setupHooks ensures that all hooks have a default no-op function if unset.
func setupHooks(hooks *Hooks) {
	if hooks.OnClose == nil {
		hooks.OnClose = func(ClientKey, StatusCode, error) {}
	}
	if hooks.OnReadError == nil {
		hooks.OnReadError = func(ClientKey, error) {}
	}
	if hooks.OnReadFrame == nil {
		hooks.OnReadFrame = func(ClientKey, *Frame) {}
	}
	if hooks.OnReadMessage == nil {
		hooks.OnReadMessage = func(ClientKey, *Message) {}
	}
	if hooks.OnWriteError == nil {
		hooks.OnWriteError = func(ClientKey, error) {}
	}
	if hooks.OnWriteFrame == nil {
		hooks.OnWriteFrame = func(ClientKey, *Frame) {}
	}
	if hooks.OnWriteMessage == nil {
		hooks.OnWriteMessage = func(ClientKey, *Message) {}
	}
}
