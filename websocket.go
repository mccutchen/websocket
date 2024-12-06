// Package websocket implements a basic websocket server.
package websocket

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
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

// Limits define the limits imposed on a websocket connection.
type Limits struct {
	MaxDuration     time.Duration
	MaxFragmentSize int
	MaxMessageSize  int
}

// Conn is a websocket connection.
type Conn struct {
	conn   net.Conn
	buf    *bufio.ReadWriter
	closed *atomic.Bool

	maxDuration     time.Duration
	maxFragmentSize int
	maxMessageSize  int
}

// Accept upgrades an HTTP connection to a websocket connection.
func Accept(w http.ResponseWriter, r *http.Request, limits Limits) (*Conn, error) {
	if err := handshake(w, r); err != nil {
		return nil, fmt.Errorf("websocket: accept: handshake failed: %w", err)
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		panic("websocket: accept: server does not support hijacking")
	}

	conn, buf, err := hj.Hijack()
	if err != nil {
		panic(fmt.Errorf("websocket: accept: hijack failed: %s", err))
	}

	// best effort attempt to ensure that our websocket conenctions do not
	// exceed the maximum request duration
	if limits.MaxDuration > 0 {
		if err := conn.SetDeadline(time.Now().Add(limits.MaxDuration)); err != nil {
			panic(fmt.Errorf("websocket: serve: failed to set deadline on connection: %s", err))
		}
	}

	return &Conn{
		conn:            conn,
		buf:             buf,
		closed:          &atomic.Bool{},
		maxDuration:     limits.MaxDuration,
		maxFragmentSize: limits.MaxFragmentSize,
		maxMessageSize:  limits.MaxMessageSize,
	}, nil
}

// handshake validates the request and performs the WebSocket handshake, after
// which only websocket frames should be written to the underlying connection.
func handshake(w http.ResponseWriter, r *http.Request) error {
	if strings.ToLower(r.Header.Get("Upgrade")) != "websocket" {
		return fmt.Errorf("missing required `Upgrade: websocket` header")
	}
	if v := r.Header.Get("Sec-Websocket-Version"); v != requiredVersion {
		return fmt.Errorf("only websocket version %q is supported, got %q", requiredVersion, v)
	}

	clientKey := r.Header.Get("Sec-Websocket-Key")
	if clientKey == "" {
		return fmt.Errorf("missing required `Sec-Websocket-Key` header")
	}

	w.Header().Set("Connection", "upgrade")
	w.Header().Set("Upgrade", "websocket")
	w.Header().Set("Sec-Websocket-Accept", acceptKey(clientKey))
	w.WriteHeader(http.StatusSwitchingProtocols)
	return nil
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
		}

		frame, err := readFrame(c.buf)
		if err != nil {
			return nil, c.closeWithErrorReturned(StatusServerError, err)
		}

		if err := validateFrame(frame, c.maxFragmentSize); err != nil {
			return nil, c.closeWithErrorReturned(StatusProtocolError, err)
		}

		switch frame.Opcode {
		case OpcodeBinary, OpcodeText:
			if msg != nil {
				return nil, c.closeWithErrorReturned(StatusProtocolError, ErrContinuationExpected)
			}
			if frame.Opcode == OpcodeText && !utf8.Valid(frame.Payload) {
				return nil, c.closeWithErrorReturned(StatusUnsupportedPayload, ErrInvalidUT8)
			}
			msg = &Message{
				Binary:  frame.Opcode == OpcodeBinary,
				Payload: frame.Payload,
			}
		case OpcodeContinuation:
			if msg == nil {
				return nil, c.closeWithErrorReturned(StatusProtocolError, ErrInvalidContinuation)
			}
			if !msg.Binary && !utf8.Valid(frame.Payload) {
				return nil, c.closeWithErrorReturned(StatusUnsupportedPayload, ErrInvalidUT8)
			}
			msg.Payload = append(msg.Payload, frame.Payload...)
			if len(msg.Payload) > c.maxMessageSize {
				return nil, c.closeWithErrorReturned(StatusTooLarge, fmt.Errorf("message size %d exceeds maximum of %d bytes", len(msg.Payload), c.maxMessageSize))
			}
		case OpcodeClose:
			_ = c.Close()
			return nil, io.EOF
		case OpcodePing:
			frame.Opcode = OpcodePong
			if err := writeFrame(c.buf, frame); err != nil {
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
			return msg, nil
		}
	}
}

func (c *Conn) Write(ctx context.Context, msg *Message) error {
	for _, frame := range messageFrames(msg, c.maxFragmentSize) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.closedCh:
			return io.EOF
		default:
		}
		if err := writeFrame(c.buf, frame); err != nil {
			return err
		}
	}
	return nil
}

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
				return
			}
		}
	}
}

func (c *Conn) Close() error {
	return c.closeWithError(StatusNormalClosure, nil)
}

func (c *Conn) closeWithError(code StatusCode, err error) error {
	close(c.closedCh)
	_ = writeCloseFrame(c.buf, code, err)
	return c.conn.Close()
}

func (c *Conn) closeWithErrorReturned(code StatusCode, err error) error {
	_ = c.closeWithError(code, err)
	return err
}
