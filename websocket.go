// Package websocket implements a basic websocket server.
package websocket

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	CloseTimeout   time.Duration // defaults to ReadTimeout if set
	MaxFrameSize   int
	MaxMessageSize int
}

type deadliner interface {
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// Connections can be in one of three states.
type connState string

const (
	connStateOpen    connState = "open"
	connStateClosing connState = "closing"
	connStateClosed  connState = "closed"
)

// ErrConnectionClosed is returned on any attempt to read from or write to
// a closed or closing connection.
var ErrConnectionClosed = errors.New("websocket: connection not open for reading or writing")

// Websocket is a websocket connection.
type Websocket struct {
	conn net.Conn
	mode Mode

	// connection state, protected by mutex
	mu    sync.Mutex
	state connState

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
func New(conn net.Conn, clientKey ClientKey, mode Mode, opts Options) *Websocket {
	setDefaults(&opts)
	return &Websocket{
		conn:           conn,
		state:          connStateOpen,
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
	if opts.CloseTimeout == 0 && opts.ReadTimeout != 0 {
		opts.CloseTimeout = opts.ReadTimeout
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

	if ws.state != connStateOpen {
		return nil, ErrConnectionClosed
	}

	var msg *Message
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("websocket: read: %w", ctx.Err())
		default:
		}

		frame, err := ws.readFrame()
		if err != nil {
			// If we failed to read a frame for any reason (generally a read
			// timeout or a validation error), send a close frame to inform
			// the peer of the reason and close connection immediately without
			// waiting for a reply.
			//
			// This approach seems a) expected by the Autobahn test suite and
			// b) supported by the RFC:
			// https://datatracker.ietf.org/doc/html/rfc6455#section-7.1.7
			//
			// (Search for "Fail the" to see all of the scenarios where
			// immediately closing the connection is the correct behavior.)
			return nil, err
		}

		opcode := frame.Opcode()
		switch opcode {
		case OpcodeBinary, OpcodeText:
			if msg != nil {
				return nil, ErrContinuationExpected
			}
			msg = &Message{
				Binary:  opcode == OpcodeBinary,
				Payload: frame.Payload,
			}
		case OpcodeContinuation:
			if msg == nil {
				return nil, ErrContinuationUnexpected
			}
			if len(msg.Payload)+len(frame.Payload) > ws.maxMessageSize {
				return nil, ErrMessageTooLarge
			}
			msg.Payload = append(msg.Payload, frame.Payload...)
		case OpcodeClose:
			if err := ws.doCloseHandshakeReply(closeReplyFrame(frame)); err != nil {
				return nil, err
			}
			return nil, io.EOF
		case OpcodePing:
			frame = NewFrame(OpcodePong, true, frame.Payload)
			if err := ws.writeFrame(frame); err != nil {
				return nil, err
			}
			continue
		case OpcodePong:
			continue // no-op, assume reply to a ping
		default:
			return nil, ErrOpcodeUnknown
		}

		if frame.Fin() {
			if !msg.Binary && !utf8.Valid(msg.Payload) {
				return nil, ErrInvalidFramePayload
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

	if ws.state != connStateOpen {
		return ErrConnectionClosed
	}

	ws.hooks.OnWriteMessage(ws.clientKey, msg)
	for _, frame := range FrameMessage(msg, ws.maxFrameSize) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("websocket: write: %w", ctx.Err())
		default:
			if err := ws.writeFrame(frame); err != nil {
				return fmt.Errorf("websocket: write: %w", err)
			}
		}
	}
	return nil
}

func (ws *Websocket) readFrame() (*Frame, error) {
	ws.resetReadDeadline()
	frame, err := ReadFrame(ws.conn, ws.mode, ws.maxFrameSize)
	if err != nil {
		ws.hooks.OnReadError(ws.clientKey, err)
		return nil, fmt.Errorf("websocket: read: %w", err)
	}
	if err := validateFrame(frame); err != nil {
		ws.hooks.OnReadError(ws.clientKey, err)
		return nil, fmt.Errorf("websocket: read: %w", err)
	}
	ws.hooks.OnReadFrame(ws.clientKey, frame)
	return frame, nil
}

func (ws *Websocket) writeFrame(frame *Frame) error {
	ws.resetWriteDeadline()
	ws.hooks.OnWriteFrame(ws.clientKey, frame)
	if err := WriteFrame(ws.conn, ws.mask(), frame); err != nil {
		ws.hooks.OnWriteError(ws.clientKey, err)
		return err
	}
	return nil
}

// Serve is a high-level convienience method for request-response style
// websocket connections, where the given [Handler] is called for each
// incoming message read from the peer.
//
// If the handler returns a non-nil message, that message is written back
// to the peer. Nil messages indicate that there is no reply and are skipped.
//
// If the handler returns a non-nil error, its message will be sent to the
// client as the start of the two-way closing handshake with a status of
// internal error. NOTE: This means errors returned by the handler should not
// contain sensitive information that should not be exposed to peers.
//
// If there is an error reading from or writing to the underlying connection,
// the connection is assumed to be unrecoverable and is closed immediately,
// without waiting for the full two-way handshake. For more control over
// error handling and closing behavior, use [ReadMessage], [WriteMessage], and
// [Close] directly.
//
// See also [EchoHandler], a minimal handler that echoes each incoming message
// verbatim.
func (ws *Websocket) Serve(ctx context.Context, handler Handler) error {
	for {
		msg, err := ws.ReadMessage(ctx)
		if err != nil {
			// Special case: connection is closed, there's no need to write
			// closing handshake. Consider this a normal, non-error end to the
			// connection.
			if errors.Is(err, io.EOF) {
				return nil
			}
			// If we failed to read a message for any other reason (generally
			// a read timeout or a validation error), send a close frame to
			// inform the peer of the reason and close connection immediately
			// without waiting for a reply.
			//
			// This approach seems a) expected by the Autobahn test suite and
			// b) supported by the RFC:
			// https://datatracker.ietf.org/doc/html/rfc6455#section-7.1.7
			//
			// (Search for "Fail the" to see all of the scenarios where
			// immediately closing the connection is the correct behavior.)
			return ws.closeImmediately(err)
		}

		resp, err := handler(ctx, msg)
		if err != nil {
			// on error from handler function, start full closing handshake
			// so that clients may be properly informed of application-level
			// errors.
			if closeErr := ws.CloseWithStatus(statusCodeForError(err)); closeErr != nil {
				return fmt.Errorf("websocket: serve: error closing connection after application error: %w: %w", err, closeErr)
			}
			return err
		}
		if resp != nil {
			if err := ws.WriteMessage(ctx, resp); err != nil {
				// As with reads above, an error writing a message for any
				// reason is considered fatal, so we try writing a close frame
				// (which will probably also fail) and close the connection
				// immediately instead of waiting for full two way handshake.
				return ws.closeImmediately(err)
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

// Close starts a normal closing handshake. See [CloseWithStatus] for control
// over status code and reason.
func (ws *Websocket) Close() error {
	return ws.CloseWithStatus(StatusNormalClosure, "")
}

// CloseWithStatus starts the closing handshake with the given status code and
// optional reason.
func (ws *Websocket) CloseWithStatus(status StatusCode, reason string) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.doCloseHandshake(NewCloseFrame(status, reason), nil)
}

// doCloseHandshake initiates the closing handshake and blocks until the
// handshake is completed by the peer or until the configured close timeout
// elapses.
//
// Note: caller must hold the mutex.
func (ws *Websocket) doCloseHandshake(closeFrame *Frame, cause error) error {
	// enter closing state and ensure no one read or write can exceed our
	// close timeout, since we may do multiple reads below while waiting for a
	// reply to our close frame
	ws.setState(connStateClosing)
	ws.readTimeout = ws.closeTimeout
	ws.writeTimeout = ws.closeTimeout

	ws.hooks.OnCloseHandshakeStart(ws.clientKey, 0, cause)
	if err := ws.writeFrame(closeFrame); err != nil {
		ws.finishClose()
		return fmt.Errorf("websocket: close: failed to write close frame %w", err)
	}

	// Once we've written the frame to start the closing handshake, we
	// need to read frames until a) we get an ack close frame in return or
	// b) the close deadline elapses. Any non-close frames read are
	// discarded.
	for {
		frame, err := ws.readFrame()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				cause = fmt.Errorf("websocket: close: read failed while waiting for reply: %w", err)
			}
			ws.finishClose()
			return cause
		}
		if frame.Opcode() != OpcodeClose {
			// drop any non-close frames recevied after closing handshake
			// is started
			continue
		}
		ws.hooks.OnCloseHandshakeDone(ws.clientKey, 0, nil)
		ws.finishClose()
		return cause
	}
}

// doCloseHandshakeReply sends a close frame in response to a closing
// handshake initiated by the other end and then closes our end of the
// connection.
func (ws *Websocket) doCloseHandshakeReply(closeFrame *Frame) error {
	err := ws.writeFrame(closeFrame)
	ws.hooks.OnCloseHandshakeDone(ws.clientKey, 0, err)
	ws.finishClose()
	return err
}

func (ws *Websocket) closeImmediately(cause error) error {
	code, reason := statusCodeForError(cause)
	frame := NewCloseFrame(code, reason)
	_ = ws.writeFrame(frame) // connection is closed on our end, error not actionable
	ws.hooks.OnCloseHandshakeDone(ws.clientKey, code, cause)
	ws.finishClose()
	return cause
}

func (ws *Websocket) finishClose() {
	ws.setState(connStateClosed)
	_ = ws.conn.Close() // nothing we can do about an error here
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

var validTransitions = map[connState][]connState{
	connStateOpen: {
		// proper closing handshake has started, waiting on reply from peer
		connStateClosing,
		// for some errors (e.g. network-level issues) we transition directly
		// from open -> closed without doing a full closing handshake
		connStateClosed,
	},
	connStateClosing: {connStateClosed},
	connStateClosed:  {connStateClosed}, // we treat closing a connection twice as a no-op
}

// setState updates the connection's state, ensuring that the transition is
// valid.
//
// Note: caller must hold the mutex.
func (ws *Websocket) setState(newState connState) {
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
	if err == nil || errors.Is(err, io.EOF) {
		return StatusNormalClosure, ""
	}
	var protoErr *Error
	if errors.As(err, &protoErr) {
		return protoErr.code, protoErr.Error()
	}
	return StatusInternalError, err.Error()
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
