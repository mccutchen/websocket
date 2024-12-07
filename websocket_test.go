package websocket_test

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
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
			websocket.Accept(w, handshakeReq, websocket.Options{})
		})

		t.Run("hijack failed", func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("expected to catch panic on Serve before Handshake")
				}
				assert.Equal(t, fmt.Sprint(r), "websocket: accept: hijack failed: error hijacking connection", "incorrect panic message")
			}()
			websocket.Accept(&brokenHijackResponseWriter{}, handshakeReq, websocket.Options{})
		})
	}
}

func TestConnectionLimits(t *testing.T) {
	// XXX FIXME: need to rework this in terms of read/write deadlines!
	t.Run("maximum request duration is enforced", func(t *testing.T) {
		t.Parallel()

		maxDuration := 500 * time.Millisecond

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

		t.Logf("httptest server addr: %s", srv.Listener.Addr())
		log.Printf("XXX server addr:\n%s", srv.Listener.Addr())
		<-time.After(20 * time.Second)

		proc := startTCPDump(t, srv.Listener.Addr())
		defer func() {
			log.Println(proc)
			assert.NilError(t, proc.Signal(syscall.SIGTERM))
			_, err := proc.Wait()
			assert.NilError(t, err)
		}()

		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		assert.NilError(t, err)
		defer conn.Close()

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

		// first, we write the request line and headers, which should cause the
		// server to respond with a 101 Switching Protocols response.
		{
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

			t.Logf("elapsed time: %s", elapsed)
			t.Logf("XXX payload size:  %v", len(resp))
			t.Logf("XXX payload bytes: %v", resp)

			assert.NilError(t, err)
			assert.Equal(t, len(resp) > 0, true, "expected non-empty response")

			frame, err := websocket.ReadFrame(bytes.NewBuffer(resp))
			assert.NilError(t, err)
			assert.RoughlyEqual(t, elapsed, maxDuration, 25*time.Millisecond)
			t.Logf("got frame: %#v", frame)
			assert.Equal(t, frame.Opcode, websocket.OpcodeClose, "expected close frame")
			assert.Equal(t, len(frame.Payload) >= 2, true, "expected close frame payload to be at least 2 bytes, got %v", frame.Payload)
			statusCode := websocket.StatusCode(binary.BigEndian.Uint16(frame.Payload[:2]))
			assert.Equal(t, statusCode, websocket.StatusNormalClosure, "expected normal closure status code")
			// FIXME: compare status code, wherever we can find it?
			// assert.Equal(t, frame.???, websocket.StatusNormalClosure, "expected normal closure status code")

			// confirm connection is closed
			_, err = conn.Write([]byte("0"))
			assert.Error(t, err, net.ErrClosed)
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
		)

		wg.Add(1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer wg.Done()
			start := time.Now()
			ws, err := websocket.Accept(w, r, websocket.Options{
				ReadTimeout:     serverTimeout,
				MaxFragmentSize: 128,
				MaxMessageSize:  256,
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

		// first, we write the request line and headers, which should cause the
		// server to respond with a 101 Switching Protocols response.
		{
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

			// wait for the server to finish
			wg.Wait()
			assert.RoughlyEqual(t, elapsedServerTime, clientTimeout, 10*time.Millisecond)
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
			log.Printf("XXX hook client=%s OnClose code=%v err=%q", key, code, err)
		},
		OnReadError: func(key websocket.ClientKey, err error) {
			log.Printf("XXX hook client=%s OnReadError err=%q", key, err)
		},
		OnReadFrame: func(key websocket.ClientKey, frame *websocket.Frame) {
			log.Printf("XXX hook client=%s OnReadFrame frame=%#v", key, frame)
		},
		OnReadMessage: func(key websocket.ClientKey, msg *websocket.Message) {
			log.Printf("XXX hook client=%s OnReadMessage msg=%#v", key, msg)
		},
		OnWriteError: func(key websocket.ClientKey, err error) {
			log.Printf("XXX hook client=%s OnWriteError err=%q", key, err)
		},
		OnWriteFrame: func(key websocket.ClientKey, frame *websocket.Frame) {
			log.Printf("XXX hook client=%s OnWriteFrame frame=%#v", key, frame)
		},
		OnWriteMessage: func(key websocket.ClientKey, msg *websocket.Message) {
			log.Printf("XXX hook client=%s OnWriteMessage msg=%#v", key, msg)
		},
	}
}

func startTCPDump(t *testing.T, addr net.Addr) *os.Process {
	t.Helper()

	host, port, err := net.SplitHostPort(addr.String())
	assert.NilError(t, err)

	ouptputPath := "dump.pcap"

	cmd := exec.Command("tcpdump", "-w", ouptputPath, "-i", "lo0", "tcp", "port", port)
	fmt.Printf("XXX TCPDUMP CMD: %v\n", cmd.String())

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	t.Logf("running tcpdump to capture %s:%s traffic: %s", host, port, cmd.String())
	assert.NilError(t, cmd.Start())
	<-time.After(500 * time.Millisecond)
	waitForFile(t, ouptputPath)
	return cmd.Process
}

func waitForFile(t *testing.T, path string) {
	t.Helper()

	timeout := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			t.Fatalf("file %q did not appear in time", path)
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				return
			}
		}
	}
}
