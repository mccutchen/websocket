// Package websocket implements a basic websocket server.
package websocket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Default options.
const (
	DefaultMaxFrameSize   int = 16 << 10  // 16KiB
	DefaultMaxMessageSize int = 256 << 10 // 256KiB

	// DefaultCloseTimeout is set to a reasonable default to prevent
	// misbehaving/malicious clients from holding open connections for too
	// long.
	DefaultCloseTimeout = 1 * time.Second
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
	CloseTimeout   time.Duration
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

	// connection state, protected by mutex
	mu                   sync.Mutex
	state                ConnState
	startCloseOnce       sync.Once
	closeImmediatelyOnce sync.Once

	// observability
	clientKey ClientKey
	hooks     Hooks

	// limits
	closeTimeout   time.Duration
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
		closeTimeout:   opts.CloseTimeout,
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
	if opts.CloseTimeout <= 0 {
		opts.CloseTimeout = DefaultCloseTimeout
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
	ws.mu.Lock()
	defer ws.mu.Unlock()

	var msg *Message
	for {
		// check for cancelation on each iteration
		select {
		case <-ctx.Done():
			// If context is canceled due to deadline, treat it as a read error
			// and initiate server closure.
			//
			// Otherwise, assume the client closed the connection and there's
			// nothing for us to do besides close our end.
			//
			// TODO: hook for context cancellation/timeout/early closure?
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, ws.startCloseOnReadError(ctx.Err())
			}
			return nil, ws.closeImmediately(nil)
		default:
		}

		frame, err := ws.readFrame()
		if err != nil {
			return nil, ws.startCloseOnReadError(err)
		}

		opcode := frame.Opcode()
		switch opcode {
		case OpcodeBinary, OpcodeText:
			if msg != nil {
				return nil, ws.startCloseOnReadError(ErrContinuationExpected)
			}
			msg = &Message{
				Binary:  opcode == OpcodeBinary,
				Payload: frame.Payload,
			}
		case OpcodeContinuation:
			if msg == nil {
				return nil, ws.startCloseOnReadError(ErrContinuationUnexpected)
			}
			if len(msg.Payload)+len(frame.Payload) > ws.maxMessageSize {
				return nil, ws.startCloseOnReadError(ErrMessageTooLarge)
			}
			msg.Payload = append(msg.Payload, frame.Payload...)
		case OpcodeClose:
			log.Printf("XXX got close from client")
			code := closeStatusCode(frame)
			if code == StatusNoStatusRcvd {
				frame = NewCloseFrame(code, "")
			}
			ws.hooks.OnCloseHandshakeDone(ws.clientKey, code, nil)
			_ = ws.writeFrame(frame)
			ws.finishClose()
			return nil, nil
		case OpcodePing:
			frame = NewFrame(OpcodePong, true, frame.Payload)
			if err := ws.writeFrame(frame); err != nil {
				return nil, ws.startCloseOnWriteError(err)
			}
			continue
		case OpcodePong:
			continue // no-op
		default:
			return nil, ws.startCloseOnReadError(ErrOpcodeUnknown)
		}

		if frame.Fin() {
			if !msg.Binary && !utf8.Valid(msg.Payload) {
				return nil, ws.startCloseOnReadError(ErrInvalidFramePayload)
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
	ws.mu.Lock()
	defer ws.mu.Unlock()

	ws.hooks.OnWriteMessage(ws.clientKey, msg)
	for _, frame := range FrameMessage(msg, ws.maxFrameSize) {
		select {
		case <-ctx.Done():
			return ws.startCloseOnWriteError(fmt.Errorf("websocket: write: %w", ctx.Err()))
		default:
			if err := ws.writeFrame(frame); err != nil {
				return ws.startCloseOnWriteError(err)
			}
		}
	}
	return nil
}

func (ws *Websocket) readFrame() (*Frame, error) {
	ws.resetReadDeadline()
	frame, err := ReadFrame(ws.conn, ws.mode, ws.maxFrameSize)
	if err != nil {
		return nil, err
	}
	if err := validateFrame(frame); err != nil {
		return nil, err
	}
	ws.hooks.OnReadFrame(ws.clientKey, frame)
	return frame, nil
}

func (ws *Websocket) writeFrame(frame *Frame) error {
	ws.resetWriteDeadline()
	ws.hooks.OnWriteFrame(ws.clientKey, frame)
	return WriteFrame(ws.conn, ws.mask(), frame)
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
			ws.mu.Lock()
			defer ws.mu.Unlock()
			code, reason := statusCodeForError(err)
			frame := NewCloseFrame(code, reason)
			_ = ws.doCloseHandshake(frame, code, err)
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

// Close starts the closing handshake.
func (ws *Websocket) Close() error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	code := StatusNormalClosure
	return ws.doCloseHandshake(NewCloseFrame(code, ""), code, nil)
}

func (ws *Websocket) startCloseOnReadError(err error) error {
	ws.hooks.OnReadError(ws.clientKey, err)
	code, reason := statusCodeForError(err)
	closeFrame := NewCloseFrame(code, reason)
	return ws.doCloseHandshake(closeFrame, code, err)
}

func (ws *Websocket) startCloseOnWriteError(err error) error {
	ws.hooks.OnWriteError(ws.clientKey, err)
	code, reason := statusCodeForError(err)
	closeFrame := NewCloseFrame(code, reason)
	return ws.doCloseHandshake(closeFrame, code, err)
}

func (ws *Websocket) doCloseHandshake(closeFrame *Frame, code StatusCode, cause error) error {
	// read or write failed because the connection is already closed, nothing
	// we can do
	if errors.Is(cause, net.ErrClosed) {
		return cause
	}

	// use sync.Once to ensure that we only close the connection once whether
	// or not concurrent reads/writes attempt to close it simultaneously
	ws.startCloseOnce.Do(func() {
		ws.setState(ConnStateClosing)
		closeDeadline := time.Now().Add(ws.closeTimeout)
		// also ensure no one read or write can exceed our new close deadline,
		// since we may do multiple reads below while waiting for our close
		// frame to be ACK'd
		ws.writeTimeout = ws.closeTimeout
		ws.readTimeout = ws.closeTimeout

		ws.hooks.OnCloseHandshakeStart(ws.clientKey, code, cause)
		if err := ws.writeFrame(closeFrame); err != nil {
			cause = fmt.Errorf("websocket: close: failed to write close frame for error: %w: %w", cause, err)
			ws.finishClose()
		}

		// Once we've written the frame to start the closing handshake, we
		// need to read frames until a) we get an ack close frame in return or
		// b) the close deadline elapses. Any non-close frames read are
		// discarded.
		for {
			if time.Now().After(closeDeadline) {
				cause = fmt.Errorf("websocket: close: timeout waiting for ack for error: %w", cause)
				ws.finishClose()
				return
			}
			frame, err := ws.readFrame()
			if err != nil {
				log.Printf("XXX error reading ACK: %s", err)
				if errors.Is(err, io.EOF) {
					ws.finishClose()
				}
				cause = fmt.Errorf("websocket: close: read failed while waiting for ack for error: %w: %w", cause, err)
				ws.finishClose()
				return
			}
			if frame.Opcode() != OpcodeClose {
				// drop any non-close frames recevied after closing handshake
				// is started
				continue
			}
			log.Printf("XXX finished closing handshake!")
			ws.hooks.OnCloseHandshakeDone(ws.clientKey, StatusNormalClosure, nil)
			ws.finishClose()
			return
		}
	})
	return cause
}

func (ws *Websocket) closeImmediately(cause error) error {
	ws.closeImmediatelyOnce.Do(func() {
		code, reason := statusCodeForError(cause)
		frame := NewCloseFrame(code, reason)
		ws.hooks.OnCloseHandshakeStart(ws.clientKey, code, cause)
		ws.hooks.OnWriteFrame(ws.clientKey, frame)
		if err := ws.writeFrame(frame); err != nil {
			cause = fmt.Errorf("websocket: close: failed to write close frame for error: %w: %w", cause, err)
		}
		ws.finishClose()
	})
	return cause
}

func (ws *Websocket) finishClose() {
	ws.setState(ConnStateClosed)
	log.Printf("XXX closing connection")
	if err := ws.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		panic("XXX FIXME error closing connection: " + err.Error())
	}
}

func (ws *Websocket) resetReadDeadline() {
	if ws.readTimeout == 0 {
		return
	}
	err := ws.conn.(deadliner).SetReadDeadline(time.Now().Add(ws.readTimeout))
	if err != nil && !errors.Is(err, net.ErrClosed) {
		panic(fmt.Sprintf("websocket: failed to set read deadline: %s", err))
	}
}

func (ws *Websocket) resetWriteDeadline() {
	if ws.writeTimeout == 0 {
		return
	}
	err := ws.conn.(deadliner).SetWriteDeadline(time.Now().Add(ws.writeTimeout))
	if err != nil && !errors.Is(err, net.ErrClosed) {
		panic(fmt.Sprintf("websocket: failed to set write deadline: %s", err))
	}
}

var validTransitions = map[ConnState][]ConnState{
	ConnStateOpen: {
		// a well-behaved transition from open -> closing -> closed
		ConnStateClosing,
		// for some errors (e.g. network-level issues) we transition directly
		// from open -> closed without doing a full closing handshake
		ConnStateClosed,
	},
	ConnStateClosing: {ConnStateClosed},
	ConnStateClosed: {
		// closing a connection twice is a no-op
		ConnStateClosed,
	},
}

// setState updates the connection's state, ensuring that the transition is
// valid.
//
// Note: caller must hold the mutex.
func (ws *Websocket) setState(newState ConnState) {
	isValid := slices.Contains(validTransitions[ws.state], newState)
	if !isValid {
		panic(fmt.Errorf("websocket: setState: invalid transition from %q to %q", ws.state, newState))
	}
	ws.state = newState
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
	OnCloseHandshakeStart func(ClientKey, StatusCode, error)
	// OnCloseHandshakeDone is called when the close handshake is complete.
	OnCloseHandshakeDone func(ClientKey, StatusCode, error)
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
		hooks.OnCloseHandshakeStart = func(ClientKey, StatusCode, error) {}
	}
	if hooks.OnCloseHandshakeDone == nil {
		hooks.OnCloseHandshakeDone = func(ClientKey, StatusCode, error) {}
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
