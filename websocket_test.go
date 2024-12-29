package websocket_test

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
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
		validateCloseFrame(t, bytes.NewBuffer(resp), websocket.StatusServerError, "timeout")

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
				ReadTimeout:     serverTimeout,
				MaxFragmentSize: 128,
				MaxMessageSize:  256,
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

// TODO: flesh out basic protocol test cases
// - successful echo
// - successful echo across multiple frames
// - frame size limits
// - message size limits
// - utf8 validation
// - unexpected continuation frames
func TestProtocolBasics(t *testing.T) {
	var (
		maxDuration     = 250 * time.Millisecond
		maxFragmentSize = 16
		maxMessageSize  = 32
	)

	echoHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, websocket.Options{
			ReadTimeout:     maxDuration,
			WriteTimeout:    maxDuration,
			MaxFragmentSize: maxFragmentSize,
			MaxMessageSize:  maxMessageSize,
			Hooks:           newTestHooks(t),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ws.Serve(r.Context(), websocket.EchoHandler)
	})

	t.Run("basic echo functionality", func(t *testing.T) {
		t.Parallel()
		_, conn := setupRawConn(t, echoHandler)
		// write client frame
		clientFrame := &websocket.Frame{
			Opcode:  websocket.OpcodeText,
			Fin:     true,
			Payload: []byte("hello"),
		}
		mask := [4]byte{1, 2, 3, 4}
		assert.NilError(t, websocket.WriteFrameMasked(conn, clientFrame, mask))
		// read server frame
		serverFrame, err := websocket.ReadFrame(conn)
		assert.NilError(t, err)
		// ensure we get back the same frame
		assert.Equal(t, serverFrame.Fin, clientFrame.Fin, "expected matching FIN bits")
		assert.Equal(t, serverFrame.Opcode, clientFrame.Opcode, "expected matching opcodes")
		assert.Equal(t, string(serverFrame.Payload), string(clientFrame.Payload), "expected matching payloads")
	})

	t.Run("server requires masked frames", func(t *testing.T) {
		t.Parallel()
		_, conn := setupRawConn(t, echoHandler)
		frame := &websocket.Frame{
			Opcode:  websocket.OpcodeText,
			Fin:     true,
			Payload: []byte("hello"),
		}
		assert.NilError(t, websocket.WriteFrame(conn, frame))
		validateCloseFrame(t, conn, websocket.StatusProtocolError, "received unmasked client frame")
	})

}

func TestFraming(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		t.Parallel()

		// write masked "client" frame to buffer
		clientFrame := &websocket.Frame{
			Opcode:  websocket.OpcodeText,
			Fin:     true,
			Payload: []byte("hello"),
		}
		mask := [4]byte{1, 2, 3, 4}
		buf := &bytes.Buffer{}
		assert.NilError(t, websocket.WriteFrameMasked(buf, clientFrame, mask))

		// read "server" frame from buffer
		serverFrame, err := websocket.ReadFrame(buf)
		assert.NilError(t, err)

		// ensure client and server frame match
		assert.Equal(t, serverFrame.Fin, clientFrame.Fin, "expected matching FIN bits")
		assert.Equal(t, serverFrame.Opcode, clientFrame.Opcode, "expected matching opcodes")
		assert.Equal(t, string(serverFrame.Payload), string(clientFrame.Payload), "expected matching payloads")
	})
}

// setupRawConn is a test helpers that runs a test server and does the
// initial websocket handshake. The returned connection is ready for use to
// sent/receive websocket messages.
func setupRawConn(t *testing.T, handler http.Handler) (*httptest.Server, net.Conn) {
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
		"Sec-WebSocket-Key":     makeClientKey(),
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

func makeClientKey() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		panic(fmt.Sprintf("failed to read random bytes: %s", err))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// validateCloseFrame ensures that we can read a close frame from the given
// reader and optionally ensures that the close frame includes a specific
// status code and message.
func validateCloseFrame(t *testing.T, r io.Reader, wantStatus websocket.StatusCode, wantMsg string) {
	t.Helper()

	frame, err := websocket.ReadFrame(r)
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
	assert.Contains(t, closeMsg, wantMsg, "got incorrect close message")
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

func newTestHooks(t *testing.T) websocket.Hooks {
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
