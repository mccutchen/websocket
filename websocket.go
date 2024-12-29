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

type deadliner interface {
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// Conn is a websocket connection.
type Conn struct {
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

// Accept upgrades an HTTP connection to a websocket connection.
func Accept(w http.ResponseWriter, r *http.Request, opts Options) (*Conn, error) {
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

	return NewConn(conn, clientKey, opts), nil
}

// NewConn creates a new websocket connection.
func NewConn(src io.ReadWriteCloser, clientKey ClientKey, opts Options) *Conn {
	setDefaults(&opts)
	if opts.ReadTimeout != 0 || opts.WriteTimeout != 0 {
		if _, ok := src.(deadliner); !ok {
			panic(fmt.Sprintf("ReadTimeout and WriteTimeout may only be used when input source supports setting read/write deadlines"))
		}
	}
	return &Conn{
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

// Read reads a single websocket message from the connection. Ping/pong frames
// are handled automatically. The connection will be closed on any error.
func (c *Conn) Read(ctx context.Context) (*Message, error) {
	var msg *Message
	for {
		select {
		case <-c.closedCh:
			return nil, io.EOF
		case <-ctx.Done():
			_ = c.Close()
			return nil, ctx.Err()
		default:
			c.resetReadDeadline()
		}

		frame, err := ReadFrame(c.conn)
		if err != nil {
			code := StatusServerError
			if errors.Is(err, io.EOF) {
				code = StatusNormalClosure
			}
			return nil, c.closeOnReadError(code, err)
		}
		if err := validateFrame(frame, c.maxFragmentSize, c.server); err != nil {
			return nil, c.closeOnReadError(StatusProtocolError, err)
		}
		c.hooks.OnReadFrame(c.clientKey, frame)

		switch frame.Opcode {
		case OpcodeBinary, OpcodeText:
			if msg != nil {
				return nil, c.closeOnReadError(StatusProtocolError, ErrContinuationExpected)
			}
			msg = &Message{
				Binary:  frame.Opcode == OpcodeBinary,
				Payload: frame.Payload,
			}
		case OpcodeContinuation:
			if msg == nil {
				return nil, c.closeOnReadError(StatusProtocolError, ErrInvalidContinuation)
			}
			msg.Payload = append(msg.Payload, frame.Payload...)
			if len(msg.Payload) > c.maxMessageSize {
				return nil, c.closeOnReadError(StatusTooLarge, fmt.Errorf("message size %d exceeds maximum of %d bytes", len(msg.Payload), c.maxMessageSize))
			}
		case OpcodeClose:
			_ = c.Close()
			return nil, io.EOF
		case OpcodePing:
			frame.Opcode = OpcodePong
			c.hooks.OnWriteFrame(c.clientKey, frame)
			if err := WriteFrame(c.conn, frame); err != nil {
				return nil, err
			}
			continue
		case OpcodePong:
			continue // no-op
		default:
			return nil, c.closeOnReadError(StatusProtocolError, fmt.Errorf("unsupported opcode: %v", frame.Opcode))
		}

		if frame.Fin {
			c.hooks.OnReadMessage(c.clientKey, msg)
			if !msg.Binary && !utf8.Valid(msg.Payload) {
				return nil, c.closeOnReadError(StatusUnsupportedPayload, ErrInvalidUT8)
			}
			return msg, nil
		}
	}
}

// Write writes a single websocket message to the connection, after splitting
// it into fragments (if necessary). The connection will be closed on any
// error.
func (c *Conn) Write(ctx context.Context, msg *Message) error {
	c.hooks.OnWriteMessage(c.clientKey, msg)
	for _, frame := range messageFrames(msg, c.maxFragmentSize) {
		c.hooks.OnWriteFrame(c.clientKey, frame)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.closedCh:
			return io.EOF
		default:
			c.resetWriteDeadline()
		}
		if err := WriteFrame(c.conn, frame); err != nil {
			return c.closeOnWriteError(StatusServerError, err)
		}
	}
	return nil
}

// Serve is a basic high-level handler for a request-response style websocket
// connection, where handler is called for each incoming message and its return
// value is sent back to the client.
func (c *Conn) Serve(ctx context.Context, handler Handler) {
	for {
		msg, err := c.Read(ctx)
		if err != nil {
			// an error in Read() closes the connection
			return
		}

		resp, err := handler(ctx, msg)
		if err != nil {
			_ = c.closeWithError(StatusServerError, err)
			return
		}

		if resp != nil {
			if err := c.Write(ctx, resp); err != nil {
				// an error in Write() closes the connection
				return
			}
		}
	}
}

// Close closes a websocket connection.
func (c *Conn) Close() error {
	return c.closeWithError(StatusNormalClosure, nil)
}

func (c *Conn) closeWithError(code StatusCode, err error) error {
	c.hooks.OnClose(c.clientKey, code, err)
	close(c.closedCh)
	if err := writeCloseFrame(c.conn, code, err); err != nil {
		return fmt.Errorf("websocket: failed to write close frame: %w", err)
	}
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("websocket: failed to close connection: %s", err)
	}
	return nil
}

func (c *Conn) closeOnReadError(code StatusCode, err error) error {
	c.hooks.OnReadError(c.clientKey, err)
	_ = c.closeWithError(code, err)
	return err
}

func (c *Conn) closeOnWriteError(code StatusCode, err error) error {
	c.hooks.OnWriteError(c.clientKey, err)
	_ = c.closeWithError(code, err)
	return err
}

func (c *Conn) resetReadDeadline() {
	if c.readTimeout <= 0 {
		return
	}
	if err := c.conn.(deadliner).SetReadDeadline(time.Now().Add(c.readTimeout)); err != nil {
		panic(fmt.Sprintf("websocket: failed to set read deadline: %s", err))
	}
}

func (c *Conn) resetWriteDeadline() {
	if c.writeTimeout <= 0 {
		return
	}
	if err := c.conn.(deadliner).SetWriteDeadline(time.Now().Add(c.writeTimeout)); err != nil {
		panic(fmt.Sprintf("websocket: failed to set write deadline: %s", err))
	}
}
