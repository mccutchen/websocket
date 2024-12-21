package websocket_test

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	// TODO: need to rework these tests in terms of read/write deadlines rather
	// than total request time.
	t.Run("server enforces read deadline", func(t *testing.T) {
		t.Parallel()

		maxDuration := 250 * time.Millisecond

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := websocket.Accept(w, r, websocket.Options{
				ReadTimeout:  maxDuration,
				WriteTimeout: maxDuration,
				// TODO: test these limits as well
				MaxFragmentSize: 128,
				MaxMessageSize:  256,
				Hooks:           newTestHooks(t),
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			ws.Serve(r.Context(), websocket.EchoHandler)
		}))
		defer srv.Close()

		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		assert.NilError(t, err)
		defer conn.Close()

		// first, we write the request line and headers, which should cause the
		// server to respond with a 101 Switching Protocols response.
		{
			reqParts := []string{
				"GET /websocket/echo HTTP/1.1",
				"Host: test",
				"Connection: upgrade",
				"Upgrade: websocket",
				"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Version: 13",
			}
			reqBytes := []byte(strings.Join(reqParts, "\r\n") + "\r\n\r\n")
			t.Logf("raw request:\n%q", reqBytes)

			n, err := conn.Write(reqBytes)
			assert.NilError(t, err)
			assert.Equal(t, n, len(reqBytes), "incorrect number of bytes written")

			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			assert.NilError(t, err)
			assert.StatusCode(t, resp, http.StatusSwitchingProtocols)
		}

		// next, we try to read from the connection, expecting the connection
		// to be closed after roughly maxDuration seconds
		{
			start := time.Now()
			resp, err := io.ReadAll(conn)
			elapsed := time.Since(start)

			// client read should succeed, because we expect the server to send
			// a close frame when its read deadline is reached
			assert.NilError(t, err)
			assert.RoughlyEqual(t, elapsed, maxDuration, 25*time.Millisecond)

			buf := make([]byte, 4096)
			frame, err := websocket.ReadFrame(bytes.NewBuffer(resp), buf)
			assert.NilError(t, err)
			assert.Equal(t, frame.Opcode, websocket.OpcodeClose, "expected close frame")

			// extract close status code and message from payload
			assert.Equal(t, len(frame.Payload) >= 2, true, "expected close frame payload to be at least 2 bytes, got %v", frame.Payload)
			statusCode := websocket.StatusCode(binary.BigEndian.Uint16(frame.Payload[:2]))
			closeMsg := string(frame.Payload[2:])
			t.Logf("got close frame: code=%v msg=%q", statusCode, closeMsg)
			assert.Equal(t, statusCode, websocket.StatusServerError, "expected server error status code")
			assert.Contains(t, closeMsg, "timeout", "expected timeout close message")

			// connection should be closed, so we should get EOF when trying to
			// read from it again
			_, err = conn.Read(make([]byte, 1))
			assert.Error(t, err, io.EOF)
		}
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
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		defer srv.Close()

		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		assert.NilError(t, err)
		defer conn.Close()

		// should cause the client end of the connection to close well before
		// the max request time configured above
		assert.NilError(t, conn.SetDeadline(time.Now().Add(clientTimeout)))

		// first, we write the request line and headers, which should cause the
		// server to respond with a 101 Switching Protocols response.
		{
			reqParts := []string{
				"GET /websocket/echo HTTP/1.1",
				"Host: test",
				"Connection: upgrade",
				"Upgrade: websocket",
				"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==",
				"Sec-WebSocket-Version: 13",
			}
			reqBytes := []byte(strings.Join(reqParts, "\r\n") + "\r\n\r\n")
			t.Logf("raw request:\n%q", reqBytes)

			n, err := conn.Write(reqBytes)
			assert.NilError(t, err)
			assert.Equal(t, n, len(reqBytes), "incorrect number of bytes written")

			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			assert.NilError(t, err)
			assert.StatusCode(t, resp, http.StatusSwitchingProtocols)
			t.Logf("handshake completed")
		}

		// next, we try to read from the connection, expecting the connection
		// to be closed after roughly clientTimeout seconds.
		//
		// the server should detect the closed connection and abort the
		// handler, also after roughly clientTimeout seconds.
		{
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
		}
	})
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
