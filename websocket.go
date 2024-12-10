// Package websocket implements a basic websocket server.
package websocket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
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

func (m Mode) String() string {
	if m == ServerMode {
		return "server"
	}
	return "client"
}

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

type ConnState string

const (
	ConnStateOpen    ConnState = "open"
	ConnStateClosing ConnState = "closing"
	ConnStateClosed  ConnState = "closed"
)

// Websocket is a websocket connection.
type Websocket struct {
	conn io.ReadWriteCloser
	mode Mode

	// connection state
	state ConnState
	mu    sync.Mutex

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
		state:          ConnStateOpen,
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
		case <-ctx.Done():
			// If context is canceled due to deadline, treat it as a read error and
			// initiate server closure.
			//
			// Otherwise, assume the client closed the connection and there's nothing
			// for us to do besides close our end.
			//
			// TODO: hook for context cancellation/timeout/early closure?
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, ws.closeOnReadError(ctx.Err())
			}
			return nil, ws.Close()
		default:
			ws.resetReadDeadline(ctx)
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

		// if we're waiting for a close frame, ignore all other frames and keep
		// reading till we get one (or time out)
		//
		// FIXME: making a recursive call here is a hack, there's probably a
		// better way to structure this.
		state := ws.getState()
		if state == ConnStateClosing && opcode != OpcodeClose {
			return ws.ReadMessage(ctx)
		}

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
			switch state {
			case ConnStateOpen:
				ws.setState(ConnStateClosing)
				ws.hooks.OnCloseHandshakeStart(ws.clientKey, ClientMode, StatusNormalClosure, nil)
				// TODO: this responds with the same closing code that we
				// received. Should we either a) parse the incoming close frame
				// so that the hook gets the correct status code or b) always
				// close with a normal closure here instead of echoing the
				// incoming frame?
				if err := WriteFrame(ws.conn, ws.mask(), frame); err != nil {
					return nil, fmt.Errorf("websocket: read: failed to write close handshake reply: %w", err)
				}
				continue
			case ConnStateClosing:
				ws.hooks.OnCloseHandshakeDone(ws.clientKey, ClientMode, StatusNormalClosure, nil)
				return nil, ws.Close()
			default:
				return nil, ws.closeOnReadError(fmt.Errorf("websocket: read: unexpected close frame in state %q", state))
			}
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
			return ws.Close()
		default:
			ws.resetWriteDeadline(ctx)
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
	frame := NewCloseFrame(code, reason)
	ws.setState(ConnStateClosed)
	ws.hooks.OnCloseHandshakeStart(ws.clientKey, ws.mode, code, err)
	ws.hooks.OnWriteFrame(ws.clientKey, frame)
	if err := WriteFrame(ws.conn, ws.mask(), frame); err != nil {
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

// chooseDeadline returns an appropriate deadline by choosing the sooner of
// the context's deadline (if it has one) and the timeout.
func chooseDeadline(ctx context.Context, timeout time.Duration, nowSource func() time.Time) time.Time {
	timeoutDeadline := nowSource().Add(timeout)
	ctxDeadline, ok := ctx.Deadline()
	if ok && timeoutDeadline.After(ctxDeadline) {
		return ctxDeadline
	}
	return timeoutDeadline
}

func (ws *Websocket) resetReadDeadline(ctx context.Context) {
	deadline := chooseDeadline(ctx, ws.readTimeout, time.Now)
	if deadline.IsZero() {
		return
	}
	if err := ws.conn.(deadliner).SetReadDeadline(deadline); err != nil {
		panic(fmt.Sprintf("websocket: failed to set read deadline: %s", err))
	}
}

func (ws *Websocket) resetWriteDeadline(ctx context.Context) {
	deadline := chooseDeadline(ctx, ws.writeTimeout, time.Now)
	if deadline.IsZero() {
		return
	}
	if err := ws.conn.(deadliner).SetWriteDeadline(deadline); err != nil {
		panic(fmt.Sprintf("websocket: failed to set write deadline: %s", err))
	}
}

var validTransitions = map[ConnState][]ConnState{
	ConnStateOpen:    {ConnStateClosing},
	ConnStateClosing: {ConnStateClosed},
}

func (ws *Websocket) setState(newState ConnState) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	isValid := false
	for _, s := range validTransitions[ws.state] {
		if newState == s {
			isValid = true
			break
		}
	}
	if !isValid {
		panic(fmt.Errorf("websocket: setState: invalid transition from %q to %q", ws.state, newState))
	}
	ws.state = newState
}

func (ws *Websocket) getState() ConnState {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.state
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
	// OnCloseStart is called when the a close handshake is initiated.
	OnCloseHandshakeStart func(client ClientKey, initiatedBy Mode, code StatusCode, err error)
	// OnCloseHandshakeDone is called when the close handshake is complete.
	OnCloseHandshakeDone func(client ClientKey, initiatedBy Mode, code StatusCode, err error)
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
	if hooks.OnCloseHandshakeStart == nil {
		hooks.OnCloseHandshakeStart = func(ClientKey, Mode, StatusCode, error) {}
	}
	if hooks.OnCloseHandshakeDone == nil {
		hooks.OnCloseHandshakeDone = func(ClientKey, Mode, StatusCode, error) {}
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
