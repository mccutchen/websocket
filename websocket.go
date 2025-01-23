// Package websocket implements a basic websocket server.
package websocket

import (
	"bufio"
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
	DefaultMaxFrameSize   int = 1024 * 16  // 16KiB
	DefaultMaxMessageSize int = 1024 * 256 // 256KiB
	DefaultBufferSize     int = 1024 * 4   // 4KiB
)

// Options define the limits imposed on a websocket connection.
type Options struct {
	Hooks          Hooks
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	MaxFrameSize   int
	MaxMessageSize int
	BufferSize     int
}

type deadliner interface {
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// Websocket is a websocket connection.
type Websocket struct {
	// in practice, conn will be a net.Conn but constraining it to io.Closer
	// lets us force all reads/writes through connBuf to minimize syscalls
	conn    io.Closer
	connBuf *bufio.ReadWriter

	// connection state
	closedCh chan struct{}
	server   bool

	// observability
	clientKey ClientKey
	hooks     Hooks

	// limits
	readTimeout    time.Duration
	writeTimeout   time.Duration
	maxFrameSize   int
	maxMessageSize int

	// pool for buffer reuse
	pool *bufferPool
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

	conn, connBuf, err := hj.Hijack()
	if err != nil {
		panic(fmt.Errorf("websocket: accept: hijack failed: %s", err))
	}

	return New(conn, connBuf, clientKey, opts), nil
}

// New manually creates a new websocket connection. Caller is responsible for
// completing initial handshake before creating a websocket connection.
//
// Prefer Accept() when possible.
func New(conn io.Closer, connBuf *bufio.ReadWriter, clientKey ClientKey, opts Options) *Websocket {
	setDefaults(&opts)
	if opts.ReadTimeout != 0 || opts.WriteTimeout != 0 {
		if _, ok := conn.(deadliner); !ok {
			panic("ReadTimeout and WriteTimeout may only be used when input source supports setting read/write deadlines")
		}
	}
	return &Websocket{
		conn:           conn,
		connBuf:        connBuf,
		closedCh:       make(chan struct{}),
		server:         true,
		clientKey:      clientKey,
		hooks:          opts.Hooks,
		readTimeout:    opts.ReadTimeout,
		writeTimeout:   opts.WriteTimeout,
		maxFrameSize:   opts.MaxFrameSize,
		maxMessageSize: opts.MaxMessageSize,
		pool:           newBufferPool(opts.BufferSize),
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
	if opts.BufferSize <= 0 {
		opts.BufferSize = DefaultBufferSize
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
	frameBuf := ws.pool.Get()
	defer ws.pool.Put(frameBuf)

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

		frame, err := ReadFrame(ws.connBuf, frameBuf, ws.maxFrameSize)
		if err != nil {
			code := StatusServerError
			if errors.Is(err, io.EOF) {
				code = StatusNormalClosure
			}
			return nil, ws.closeOnReadError(code, err)
		}
		if err := validateFrame(frame, ws.maxFrameSize, ws.server); err != nil {
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
				Payload: frame.Payload[:],
			}
		case OpcodeContinuation:
			if msg == nil {
				return nil, ws.closeOnReadError(StatusProtocolError, ErrInvalidContinuation)
			}
			if len(msg.Payload)+len(frame.Payload) > ws.maxMessageSize {
				return nil, ws.closeOnReadError(StatusTooLarge, fmt.Errorf("message size exceeds maximum of %d bytes", ws.maxMessageSize))
			}
			msg.Payload = append(msg.Payload, frame.Payload...)
		case OpcodeClose:
			if err := ws.Close(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		case OpcodePing:
			frame.Opcode = OpcodePong
			ws.hooks.OnWriteFrame(ws.clientKey, frame)
			if err := WriteFrame(ws.connBuf, frame); err != nil {
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
	for _, frame := range messageFrames(msg, ws.maxFrameSize) {
		ws.hooks.OnWriteFrame(ws.clientKey, frame)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ws.closedCh:
			return io.EOF
		default:
			ws.resetWriteDeadline()
		}
		if err := WriteFrame(ws.connBuf, frame); err != nil {
			return ws.closeOnWriteError(StatusServerError, err)
		}
	}
	if err := ws.connBuf.Flush(); err != nil {
		return ws.closeOnWriteError(StatusServerError, fmt.Errorf("websocket: failed to flush after write: %w", err))
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
	if err := WriteFrame(ws.connBuf, CloseFrame(code, err)); err != nil {
		return fmt.Errorf("websocket: failed to write close frame: %w", err)
	}
	if err := ws.connBuf.Flush(); err != nil {
		return fmt.Errorf("websocket: failed to flush buffer after writing close frame: %w", err)
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

// ClientKey returns the client key for a connection.
func (ws *Websocket) ClientKey() ClientKey {
	return ws.clientKey
}

type bufferPool struct {
	pool sync.Pool
}

func newBufferPool(size int) *bufferPool {
	return &bufferPool{
		pool: sync.Pool{
			New: func() any {
				return make([]byte, size)
			},
		},
	}
}

func (bp *bufferPool) Get() []byte {
	return bp.pool.Get().([]byte)
}

func (bp *bufferPool) Put(x []byte) {
	bp.pool.Put(x)
}
