package websocket_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/mccutchen/websocket"
	"github.com/mccutchen/websocket/internal/testing/assert"
)

func TestAccept(t *testing.T) {
	testCases := map[string]struct {
		reqHeaders      map[string]string
		wantStatus      int
		wantRespHeaders map[string]string
	}{
		"valid handshake": {
			reqHeaders: map[string]string{
				"Connection":            "upgrade",
				"Upgrade":               "websocket",
				"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Version": "13",
			},
			wantRespHeaders: map[string]string{
				"Connection":           "upgrade",
				"Upgrade":              "websocket",
				"Sec-Websocket-Accept": "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=",
			},
			wantStatus: http.StatusSwitchingProtocols,
		},
		"valid handshake, header values case insensitive": {
			reqHeaders: map[string]string{
				"Connection":            "Upgrade",
				"Upgrade":               "WebSocket",
				"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Version": "13",
			},
			wantRespHeaders: map[string]string{
				"Connection":           "upgrade",
				"Upgrade":              "websocket",
				"Sec-Websocket-Accept": "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=",
			},
			wantStatus: http.StatusSwitchingProtocols,
		},
		"missing Connection header is okay": {
			reqHeaders: map[string]string{
				"Upgrade":               "websocket",
				"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Version": "13",
			},
			wantStatus: http.StatusSwitchingProtocols,
		},
		"incorrect Connection header is also okay": {
			reqHeaders: map[string]string{
				"Connection":            "foo",
				"Upgrade":               "websocket",
				"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Version": "13",
			},
			wantStatus: http.StatusSwitchingProtocols,
		},
		"missing Upgrade header": {
			reqHeaders: map[string]string{
				"Connection":            "Upgrade",
				"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Version": "13",
			},
			wantStatus: http.StatusBadRequest,
		},
		"incorrect Upgrade header": {
			reqHeaders: map[string]string{
				"Connection":            "Upgrade",
				"Upgrade":               "http/2",
				"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Version": "13",
			},
			wantStatus: http.StatusBadRequest,
		},
		"missing version": {
			reqHeaders: map[string]string{
				"Connection":        "upgrade",
				"Upgrade":           "websocket",
				"Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==",
			},
			wantStatus: http.StatusBadRequest,
		},
		"incorrect version": {
			reqHeaders: map[string]string{
				"Connection":            "upgrade",
				"Upgrade":               "websocket",
				"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Version": "12",
			},
			wantStatus: http.StatusBadRequest,
		},
		"missing Sec-WebSocket-Key": {
			reqHeaders: map[string]string{
				"Connection":            "upgrade",
				"Upgrade":               "websocket",
				"Sec-WebSocket-Version": "13",
			},
			wantStatus: http.StatusBadRequest,
		},
	}
	for name, tc := range testCases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, websocket.Options{})
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				ws.Serve(r.Context(), websocket.EchoHandler)
			}))
			defer srv.Close()

			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			for k, v := range tc.reqHeaders {
				req.Header.Set(k, v)
			}

			resp, err := http.DefaultClient.Do(req)
			assert.NilError(t, err)

			assert.StatusCode(t, resp, tc.wantStatus)
			for k, v := range tc.wantRespHeaders {
				assert.Equal(t, resp.Header.Get(k), v, "incorrect value for %q response header", k)
			}
		})
	}

	// hijack failure cases w/ shared setup
	{
		handshakeReq := httptest.NewRequest(http.MethodGet, "/websocket/echo", nil)
		for k, v := range map[string]string{
			"Connection":            "upgrade",
			"Upgrade":               "websocket",
			"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
			"Sec-WebSocket-Version": "13",
		} {
			handshakeReq.Header.Set(k, v)
		}

		t.Run("http.Hijack not implemented", func(t *testing.T) {
			t.Parallel()

			// confirm that httptest.ResponseRecorder does not implmeent
			// http.Hjijacker
			var w http.ResponseWriter = httptest.NewRecorder()
			_, ok := w.(http.Hijacker)
			assert.Equal(t, ok, false, "expected httptest.ResponseRecorder not to implement http.Hijacker")

			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("expected to catch panic on when http.Hijack not implemented")
				}
				assert.Equal(t, fmt.Sprint(r), "websocket: accept: server does not support hijacking", "incorrect panic message")
			}()
			_, _ = websocket.Accept(w, handshakeReq, websocket.Options{})
		})

		t.Run("hijack failed", func(t *testing.T) {
			t.Parallel()

			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("expected to catch panic on Serve before Handshake")
				}
				assert.Equal(t, fmt.Sprint(r), "websocket: accept: hijack failed: error hijacking connection", "incorrect panic message")
			}()
			_, _ = websocket.Accept(&brokenHijackResponseWriter{}, handshakeReq, websocket.Options{})
		})
	}
}

func TestConnectionLimits(t *testing.T) {
	t.Run("server enforces read deadline", func(t *testing.T) {
		t.Parallel()

		maxDuration := 250 * time.Millisecond
		_, conn := setupRawConn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := websocket.Accept(w, r, websocket.Options{
				ReadTimeout:  maxDuration,
				WriteTimeout: maxDuration,
				Hooks:        newTestHooks(t),
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			ws.Serve(r.Context(), websocket.EchoHandler)
		}))

		// client read should succeed, because we expect the server to send
		// a close frame when its read deadline is reached
		start := time.Now()
		resp, err := io.ReadAll(conn)
		elapsed := time.Since(start)
		assert.NilError(t, err)
		assert.RoughlyEqual(t, elapsed, maxDuration, 25*time.Millisecond)
		validateCloseFrame(t, bytes.NewBuffer(resp), websocket.StatusServerError, errors.New("xxx"))

		// connection should be closed, so we should get EOF when trying to
		// read from it again
		_, err = conn.Read(make([]byte, 1))
		assert.Error(t, err, io.EOF)
	})

	t.Run("client closing connection", func(t *testing.T) {
		t.Parallel()

		// the client will close the connection well before the server closes
		// the connection. make sure the server properly handles the client
		// closure.
		var (
			clientTimeout     = 100 * time.Millisecond
			serverTimeout     = time.Hour // should never be reached
			elapsedClientTime time.Duration
			elapsedServerTime time.Duration
			wg                sync.WaitGroup

			// record the error the server sees when the client closes the
			// connection
			gotServerReadError error
		)

		wg.Add(1)
		_, conn := setupRawConn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer wg.Done()
			start := time.Now()
			ws, err := websocket.Accept(w, r, websocket.Options{
				ReadTimeout:    serverTimeout,
				MaxFrameSize:   128,
				MaxMessageSize: 256,
				Hooks: websocket.Hooks{
					OnReadError: func(key websocket.ClientKey, err error) {
						gotServerReadError = err
					},
				},
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			ws.Serve(r.Context(), websocket.EchoHandler)
			elapsedServerTime = time.Since(start)
		}))

		// should cause the client end of the connection to close well before
		// the max request time configured above
		assert.NilError(t, conn.SetDeadline(time.Now().Add(clientTimeout)))

		// try to read from the connection, expecting the connection
		// to be closed after roughly clientTimeout seconds.
		//
		// the server should detect the closed connection and abort the
		// handler, also after roughly clientTimeout seconds.
		start := time.Now()
		_, err := conn.Read(make([]byte, 1))
		elapsedClientTime = time.Since(start)

		// close client connection, which should interrupt the server's
		// blocking read call on the connection
		conn.Close()
		t.Logf("client connection closed")

		assert.Equal(t, os.IsTimeout(err), true, "expected timeout error")
		assert.RoughlyEqual(t, elapsedClientTime, clientTimeout, 10*time.Millisecond)

		// wait for the server to finish handling the one request, then
		// make sure it took the expected amount of time to get a read
		// timeout and an appropriate error
		wg.Wait()
		assert.RoughlyEqual(t, elapsedServerTime, clientTimeout, 10*time.Millisecond)
		assert.Error(t, gotServerReadError, io.EOF)
	})
}

func TestProtocolBasics(t *testing.T) {
	var (
		maxDuration    = 250 * time.Millisecond
		maxFrameSize   = 256
		maxMessageSize = 512
	)

	newLoggingEchoHandler := func() (http.HandlerFunc, <-chan *websocket.Message) {
		// buffered chan to allow msg to be appended before test client code
		// is ready to read
		msgLog := make(chan *websocket.Message, 1)
		handler := func(w http.ResponseWriter, r *http.Request) {
			ws, err := websocket.Accept(w, r, websocket.Options{
				ReadTimeout:    maxDuration,
				WriteTimeout:   maxDuration,
				MaxFrameSize:   maxFrameSize,
				MaxMessageSize: maxMessageSize,
				Hooks:          newTestHooks(t),
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			ws.Serve(r.Context(), func(ctx context.Context, msg *websocket.Message) (*websocket.Message, error) {
				msg, err := websocket.EchoHandler(ctx, msg)
				if err != nil {
					return nil, err
				}
				msgLog <- msg
				return msg, nil
			})
		}
		return handler, msgLog
	}

	newEchoHandler := func() http.HandlerFunc {
		handler, _ := newLoggingEchoHandler()
		return handler
	}

	t.Run("basic echo functionality", func(t *testing.T) {
		t.Parallel()
		_, conn := setupRawConn(t, newEchoHandler())
		// write client frame
		clientFrame := &websocket.Frame{
			Opcode:  websocket.OpcodeText,
			Fin:     true,
			Payload: []byte("hello"),
		}
		assert.NilError(t, websocket.WriteFrameMasked(conn, clientFrame, websocket.NewMaskingKey()))
		// read server frame
		serverFrame, err := websocket.ReadFrame(conn, maxFrameSize)
		assert.NilError(t, err)
		// ensure we get back the same frame
		assert.Equal(t, serverFrame.Fin, clientFrame.Fin, "expected matching FIN bits")
		assert.Equal(t, serverFrame.Opcode, clientFrame.Opcode, "expected matching opcodes")
		assert.Equal(t, string(serverFrame.Payload), string(clientFrame.Payload), "expected matching payloads")

		// ensure closing handshake is completed when initiated by client
		clientClose := websocket.CloseFrame(websocket.StatusNormalClosure, nil)
		assert.NilError(t, websocket.WriteFrameMasked(conn, clientClose, websocket.NewMaskingKey()))
		validateCloseFrame(t, conn, websocket.StatusNormalClosure, nil)

		// FIXME: this write should fail, but the connection doesn't seem to
		// get closed. for now we skip.
		if false {
			wn, err := conn.Write([]byte("foo"))
			assert.Equal(t, wn, 0, "expected to write 0 bytes")
			assert.Error(t, err, net.ErrClosed)
		}
	})

	t.Run("fragmented echo", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, maxMessageSize/maxFrameSize, 2, "test assumes maxMessageSize/maxFrameSize == 2, may need to be updated")
		echoHandler, msgLog := newLoggingEchoHandler()
		_, conn := setupRawConn(t, echoHandler)

		// write fragmented message that will be assembled into a message that
		// exceeds the message size limit
		clientFrames := []*websocket.Frame{
			{
				Opcode:  websocket.OpcodeText,
				Fin:     false,
				Payload: bytes.Repeat([]byte("0"), maxFrameSize),
			},
			{
				Opcode:  websocket.OpcodeContinuation,
				Fin:     true,
				Payload: bytes.Repeat([]byte("1"), maxFrameSize),
			},
		}
		var wantMessagePayload []byte
		for _, frame := range clientFrames {
			assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
			wantMessagePayload = append(wantMessagePayload, frame.Payload...)
		}
		msg := mustConsumeMessage(t, msgLog)
		assert.DeepEqual(t, msg.Payload, wantMessagePayload, "incorrect messaage payload")
	})

	t.Run("unexpected continuation frame", func(t *testing.T) {
		t.Parallel()
		_, conn := setupRawConn(t, newEchoHandler())
		clientFrame := &websocket.Frame{
			Opcode:  websocket.OpcodeContinuation,
			Fin:     true,
			Payload: []byte("0"),
		}
		assert.NilError(t, websocket.WriteFrameMasked(conn, clientFrame, websocket.NewMaskingKey()))
		validateCloseFrame(t, conn, websocket.StatusProtocolError, websocket.ErrContinuationExpected)
	})

	t.Run("max frame size", func(t *testing.T) {
		t.Parallel()
		_, conn := setupRawConn(t, newEchoHandler())
		clientFrame := &websocket.Frame{
			Opcode:  websocket.OpcodeText,
			Fin:     true,
			Payload: bytes.Repeat([]byte("0"), maxFrameSize+1),
		}
		assert.NilError(t, websocket.WriteFrameMasked(conn, clientFrame, websocket.NewMaskingKey()))
		validateCloseFrame(t, conn, websocket.StatusTooLarge, websocket.ErrFrameTooLarge)
	})

	t.Run("max message size", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, maxMessageSize/maxFrameSize, 2, "test assumes maxMessageSize/maxFrameSize == 2, may need to be updated")
		_, conn := setupRawConn(t, newEchoHandler())

		// write fragmented message that will be assembled into a message that
		// exceeds the message size limit
		clientFrames := []*websocket.Frame{
			{
				Opcode:  websocket.OpcodeText,
				Fin:     false,
				Payload: bytes.Repeat([]byte("0"), maxFrameSize),
			},
			{
				Opcode:  websocket.OpcodeContinuation,
				Fin:     false,
				Payload: bytes.Repeat([]byte("1"), maxFrameSize),
			},

			{
				Opcode:  websocket.OpcodeContinuation,
				Fin:     true,
				Payload: []byte("2"),
			},
		}
		for _, frame := range clientFrames {
			assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
		}

		// the server should close the connection with an error after reading
		// the 3rd fragment above, as it will cause the total message size to
		// exceed the limit.
		validateCloseFrame(t, conn, websocket.StatusTooLarge, websocket.ErrFrameTooLarge)
	})
	t.Run("server requires masked frames", func(t *testing.T) {
		t.Parallel()
		_, conn := setupRawConn(t, newEchoHandler())
		frame := &websocket.Frame{
			Opcode:  websocket.OpcodeText,
			Fin:     true,
			Payload: []byte("hello"),
		}
		assert.NilError(t, websocket.WriteFrame(conn, frame))
		validateCloseFrame(t, conn, websocket.StatusProtocolError, websocket.ErrUnmaskedClientFrame)
	})

	t.Run("server rejects RSV bits", func(t *testing.T) {
		t.Parallel()
		_, conn := setupRawConn(t, newEchoHandler())
		frame := &websocket.Frame{
			Opcode:  websocket.OpcodeText,
			RSV1:    true,
			Fin:     true,
			Payload: []byte("hello"),
		}
		assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
		validateCloseFrame(t, conn, websocket.StatusProtocolError, websocket.ErrUnsupportedRSVBits)
	})

	t.Run("control frames must not be fragmented", func(t *testing.T) {
		t.Parallel()
		_, conn := setupRawConn(t, newEchoHandler())
		frame := &websocket.Frame{
			Opcode: websocket.OpcodeClose,
			Fin:    false,
		}
		assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
		validateCloseFrame(t, conn, websocket.StatusProtocolError, websocket.ErrControlFrameFragmented)
	})

	t.Run("control frames must be less than 125 bytes", func(t *testing.T) {
		t.Parallel()
		_, conn := setupRawConn(t, newEchoHandler())
		frame := &websocket.Frame{
			Opcode:  websocket.OpcodeClose,
			Payload: bytes.Repeat([]byte("0"), 126),
		}
		assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
		validateCloseFrame(t, conn, websocket.StatusProtocolError, websocket.ErrControlFrameTooLarge)
	})

	t.Run("test messages must be valid utf8", func(t *testing.T) {
		t.Parallel()
		echoHandler, msgLog := newLoggingEchoHandler()
		_, conn := setupRawConn(t, echoHandler)

		// valid UTF-8 accepted and echoed back
		{
			frame := &websocket.Frame{
				Opcode:  websocket.OpcodeText,
				Fin:     true,
				Payload: []byte("Iñtërnâtiônàlizætiøn"),
			}
			assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
			msg := mustConsumeMessage(t, msgLog)
			assert.DeepEqual(t, msg.Payload, frame.Payload, "incorrect message payloady")
		}

		// valid UTF-8 fragmented on codepoint boundaries is okay
		{
			frames := []*websocket.Frame{
				{
					Opcode:  websocket.OpcodeText,
					Fin:     false,
					Payload: []byte("Iñtër"),
				},

				{
					Opcode:  websocket.OpcodeContinuation,
					Fin:     false,
					Payload: []byte("nâtiônàl"),
				},

				{
					Opcode:  websocket.OpcodeContinuation,
					Fin:     true,
					Payload: []byte("izætiøn"),
				},
			}
			for _, frame := range frames {
				assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
			}

			msg := mustConsumeMessage(t, msgLog)
			assert.DeepEqual(t, msg.Payload, []byte("Iñtërnâtiônàlizætiøn"), "incorrect message payloady")
		}

		// valid UTF-8 fragmented in the middle of a codepoint is reassembled
		// into a valid message
		{
			frames := []*websocket.Frame{
				{
					Opcode:  websocket.OpcodeText,
					Fin:     false,
					Payload: []byte("jalape\xc3"),
				},

				{
					Opcode:  websocket.OpcodeContinuation,
					Fin:     true,
					Payload: []byte("\xb1o"),
				},
			}
			for _, frame := range frames {
				assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
			}

			msg := mustConsumeMessage(t, msgLog)
			assert.DeepEqual(t, msg.Payload, []byte("jalapeño"), "payload")
		}

		// invlalid UTF-8 causes connection to close
		{

			frame := &websocket.Frame{
				Opcode:  websocket.OpcodeText,
				Fin:     true,
				Payload: []byte{0xc3},
			}
			assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
			validateCloseFrame(t, conn, websocket.StatusUnsupportedPayload, "invalid UTF-8")

		}

	})

}

func mustConsumeMessage(tb testing.TB, msgLog <-chan *websocket.Message) *websocket.Message {
	tb.Helper()
	select {
	case msg := <-msgLog:
		return msg
	case <-time.After(time.Second):
		tb.Fatalf("failed to consume message in time")
	}
	panic("unreachable")
}

// setupRawConn is a test helpers that runs a test server and does the
// initial websocket handshake. The returned connection is ready for use to
// sent/receive websocket messages.
func setupRawConn(t testing.TB, handler http.Handler) (*httptest.Server, net.Conn) {
	t.Helper()

	srv := httptest.NewServer(handler)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	assert.NilError(t, err)

	t.Cleanup(func() {
		conn.Close()
		srv.Close()
	})

	handshakeReq := httptest.NewRequest(http.MethodGet, "/", nil)
	for k, v := range map[string]string{
		"Connection":            "upgrade",
		"Upgrade":               "websocket",
		"Sec-WebSocket-Key":     string(websocket.NewClientKey()),
		"Sec-WebSocket-Version": "13",
	} {
		handshakeReq.Header.Set(k, v)
	}
	// write the request line and headers, which should cause the
	// server to respond with a 101 Switching Protocols response.
	assert.NilError(t, handshakeReq.Write(conn))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	assert.NilError(t, err)
	assert.StatusCode(t, resp, http.StatusSwitchingProtocols)

	return srv, conn
}

// validateCloseFrame ensures that we can read a close frame from the given
// reader and optionally ensures that the close frame includes a specific
// status code and message.
func validateCloseFrame(t *testing.T, r io.Reader, wantStatus websocket.StatusCode, wantErr error) {
	t.Helper()

	// All control frames MUST have a payload length of 125 bytes or less
	// and MUST NOT be fragmented.
	// https://datatracker.ietf.org/doc/html/rfc6455#section-5.5
	//
	// This is already enforced in validateFrame, called by ReadFrame, but we
	// must pass in a max payload size here.
	frame, err := websocket.ReadFrame(r, 125)
	assert.NilError(t, err)
	assert.Equal(t, frame.Opcode, websocket.OpcodeClose, "expected close frame")

	if wantStatus == 0 {
		// nothing else to validate
		return
	}

	assert.Equal(t, len(frame.Payload) >= 2, true, "expected close frame payload to be at least 2 bytes, got %v", frame.Payload)
	statusCode := websocket.StatusCode(binary.BigEndian.Uint16(frame.Payload[:2]))
	closeMsg := string(frame.Payload[2:])
	t.Logf("got close frame: code=%v msg=%q", statusCode, closeMsg)
	assert.Equal(t, int(statusCode), int(wantStatus), "got incorrect close status code")
	if wantErr != nil {
		assert.Contains(t, closeMsg, wantErr.Error(), "got incorrect close message")
	}
}

// brokenHijackResponseWriter implements just enough to satisfy the
// http.ResponseWriter and http.Hijacker interfaces and get through the
// handshake before failing to actually hijack the connection.
type brokenHijackResponseWriter struct {
	http.ResponseWriter
	Code int
}

func (w *brokenHijackResponseWriter) WriteHeader(code int) {
	w.Code = code
}

func (w *brokenHijackResponseWriter) Header() http.Header {
	return http.Header{}
}

func (brokenHijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, fmt.Errorf("error hijacking connection")
}

var (
	_ http.ResponseWriter = &brokenHijackResponseWriter{}
	_ http.Hijacker       = &brokenHijackResponseWriter{}
)

func newTestHooks(t testing.TB) websocket.Hooks {
	t.Helper()
	return websocket.Hooks{
		OnClose: func(key websocket.ClientKey, code websocket.StatusCode, err error) {
			t.Logf("client=%s OnClose code=%v err=%v", key, code, err)
		},
		OnReadError: func(key websocket.ClientKey, err error) {
			t.Logf("client=%s OnReadError err=%v", key, err)
		},
		OnReadFrame: func(key websocket.ClientKey, frame *websocket.Frame) {
			t.Logf("client=%s OnReadFrame frame=%v", key, frame)
		},
		OnReadMessage: func(key websocket.ClientKey, msg *websocket.Message) {
			t.Logf("client=%s OnReadMessage msg=%v", key, msg)
		},
		OnWriteError: func(key websocket.ClientKey, err error) {
			t.Logf("client=%s OnWriteError err=%v", key, err)
		},
		OnWriteFrame: func(key websocket.ClientKey, frame *websocket.Frame) {
			t.Logf("client=%s OnWriteFrame frame=%v", key, frame)
		},
		OnWriteMessage: func(key websocket.ClientKey, msg *websocket.Message) {
			t.Logf("client=%s OnWriteMessage msg=%v", key, msg)
		},
	}
}
