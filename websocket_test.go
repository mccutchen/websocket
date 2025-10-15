package websocket_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mccutchen/websocket"
	"github.com/mccutchen/websocket/internal/testing/assert"
)

func TestHandshake(t *testing.T) {
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
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, websocket.Options{})
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				_ = ws.Serve(r.Context(), websocket.EchoHandler)
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

		// this test ensures that the server will close the connection if it
		// gets a timeout while reading a message.
		//
		// the server has a 250ms read timeout and the client never sends a
		// message, so the read loop will time out after ~250ms and the server
		// will start the closing handshake.
		//
		// at that point, the client should be able to read the close frame
		// from the server, write its ACK, and then the connection should be
		// closed.
		maxDuration := 250 * time.Millisecond
		conn := setupRawConn(t, websocket.Options{
			ReadTimeout:  maxDuration,
			WriteTimeout: maxDuration,
			Hooks:        newTestHooks(t),
		})
		start := time.Now()
		mustReadCloseFrame(t, conn, websocket.StatusAbnormalClose, errors.New("error reading frame header"))
		elapsed := time.Since(start)
		assert.Equal(t, elapsed > maxDuration, true, "not enough time passed")
		mustWriteFrame(t, conn, true, websocket.NewCloseFrame(websocket.StatusNormalClosure, "server closed connection"))
		assertConnClosed(t, conn)
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
			gotServerError error
		)

		wg.Add(1)
		conn := setupRawConnWithHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer wg.Done()
			start := time.Now()
			ws, err := websocket.Accept(w, r, websocket.Options{
				ReadTimeout:    serverTimeout,
				MaxFrameSize:   128,
				MaxMessageSize: 256,
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			gotServerError = ws.Serve(r.Context(), websocket.EchoHandler)
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
		_, clientReadErr := conn.Read(make([]byte, 1))
		elapsedClientTime = time.Since(start)

		// close client connection, which should interrupt the server's
		// blocking read call on the connection
		assert.NilError(t, conn.Close())
		assertConnClosed(t, conn)

		assert.Equal(t, os.IsTimeout(clientReadErr), true, "expected timeout error")
		assert.RoughlyEqual(t, elapsedClientTime, clientTimeout, 10*time.Millisecond)

		// wait for the server to finish handling the one request, then
		// make sure it took the expected amount of time to get a read
		// timeout and an appropriate error
		wg.Wait()
		assert.RoughlyEqual(t, elapsedServerTime, clientTimeout, 10*time.Millisecond)
		assert.Error(t, gotServerError, io.EOF)
	})
}

func mustDoCloseHandshake(t testing.TB, conn net.Conn, status websocket.StatusCode, reason string) {
	t.Helper()
	var wantErr error
	if reason != "" {
		wantErr = errors.New(reason)
	}
	if status == 0 {
		status = websocket.StatusNormalClosure
	}
	mustWriteFrame(t, conn, true, websocket.NewCloseFrame(status, reason))
	log.Printf("XXX wrote close frame")
	mustReadCloseFrame(t, conn, status, wantErr)
	log.Printf("XXX read close frame")
	assertConnClosed(t, conn)
}

func TestProtocolOkay(t *testing.T) {
	var (
		maxDuration    = 250 * time.Millisecond
		maxFrameSize   = 128
		maxMessageSize = 256
	)

	newOpts := func(t *testing.T) websocket.Options {
		return websocket.Options{
			CloseTimeout:   maxDuration,
			ReadTimeout:    maxDuration,
			WriteTimeout:   maxDuration,
			MaxFrameSize:   maxFrameSize,
			MaxMessageSize: maxMessageSize,
			Hooks:          newTestHooks(t),
		}
	}

	t.Run("echo okay", func(t *testing.T) {
		t.Parallel()

		clientFrame := websocket.NewFrame(websocket.OpcodeText, true, []byte("hello"))
		clientConn := clientServerTest(t, newOpts(t), func(t testing.TB, ws *websocket.Websocket, serverConn net.Conn) {
			// read server frame and ensure that it matches client frame
			serverFrame := mustReadFrame(t, serverConn, maxFrameSize)
			log.Printf("XXX read server frame: %s", serverFrame)
			assert.DeepEqual(t, serverFrame, clientFrame, "frames should match")
			// read next message to receive and process closing handshake
			// initiated by client
			_, err := ws.ReadMessage(t.Context())
			assert.Error(t, err, io.EOF)
		})
		mustWriteFrame(t, clientConn, true, clientFrame)
		log.Printf("XXX wrote client frame: %s", clientFrame)
		mustDoCloseHandshake(t, clientConn, 0, "")
	})

	t.Run("fragmented echo okay", func(t *testing.T) {
		t.Parallel()

		var (
			opts = websocket.Options{
				// msg size must be double frame size for this test case
				MaxFrameSize:   128,
				MaxMessageSize: 256,
				Hooks:          newTestHooks(t),
			}
		)

		msgPayload := make([]byte, 0, opts.MaxMessageSize)
		msgPayload = append(msgPayload, bytes.Repeat([]byte("0"), opts.MaxFrameSize)...)
		msgPayload = append(msgPayload, bytes.Repeat([]byte("1"), opts.MaxFrameSize)...)

		var wg sync.WaitGroup
		wg.Add(1)
		clientConn := clientServerTest(t, opts, func(t testing.TB, ws *websocket.Websocket, serverConn net.Conn) {
			defer wg.Done()
			msg := mustReadMessage(t, ws)
			assert.DeepEqual(t, msg.Payload, msgPayload, "incorrect messaage payload")
			assert.NilError(t, ws.Close())
		})

		mustWriteFrames(t, clientConn, true, []*websocket.Frame{
			websocket.NewFrame(websocket.OpcodeText, false, msgPayload[:opts.MaxFrameSize]),
			websocket.NewFrame(websocket.OpcodeContinuation, true, msgPayload[opts.MaxFrameSize:]),
		})
		mustReadCloseFrame(t, clientConn, websocket.StatusNormalClosure, nil)
		wg.Wait()
	})

	t.Run("utf8 handling okay", func(t *testing.T) {
		t.Parallel()
		var (
			opts = newOpts(t)
			conn = setupRawConn(t, opts)
			ws   = setupWebsocketClient(t, conn, opts)
		)

		// valid UTF-8 accepted and echoed back
		{
			frame := websocket.NewFrame(websocket.OpcodeText, true, []byte("Iñtërnâtiônàlizætiøn"))
			mustWriteFrame(t, conn, true, frame)
			msg := mustReadMessage(t, ws)
			assert.DeepEqual(t, msg.Payload, frame.Payload, "incorrect message payloady")
		}

		// valid UTF-8 fragmented on codepoint boundaries is okay
		{
			mustWriteFrames(t, conn, true, []*websocket.Frame{
				websocket.NewFrame(websocket.OpcodeText, false, []byte("Iñtër")),

				websocket.NewFrame(websocket.OpcodeContinuation, false, []byte("nâtiônàl")),

				websocket.NewFrame(websocket.OpcodeContinuation, true, []byte("izætiøn")),
			})
			msg := mustReadMessage(t, ws)
			assert.DeepEqual(t, msg.Payload, []byte("Iñtërnâtiônàlizætiøn"), "incorrect message payloady")
		}

		// valid UTF-8 fragmented in the middle of a codepoint is reassembled
		// into a valid message
		{
			mustWriteFrames(t, conn, true, []*websocket.Frame{
				websocket.NewFrame(websocket.OpcodeText, false, []byte("jalape\xc3")),

				websocket.NewFrame(websocket.OpcodeContinuation, true, []byte("\xb1o")),
			})
			msg := mustReadMessage(t, ws)
			assert.DeepEqual(t, msg.Payload, []byte("jalapeño"), "payload")
		}
	})

	t.Run("binary messages can be invalid UTF-8", func(t *testing.T) {
		t.Parallel()
		var (
			opts = newOpts(t)
			conn = setupRawConn(t, opts)
			ws   = setupWebsocketClient(t, conn, opts)
		)
		frame := websocket.NewFrame(websocket.OpcodeBinary, true, []byte{0xc3})
		mustWriteFrame(t, conn, true, frame)
		msg := mustReadMessage(t, ws)
		assert.Equal(t, msg.Binary, true, "binary message")
		assert.DeepEqual(t, msg.Payload, frame.Payload, "binary payload")
	})

	t.Run("jumbo frames okay", func(t *testing.T) {
		t.Parallel()

		// Ensure we're exercising extended payload length parsing, where
		// payloads of length 65536 (64 KiB) or more must be encoded as 8
		// bytes
		jumboSize := 64 << 10 // == 64 KiB == 65536
		conn := setupRawConn(t, websocket.Options{
			MaxFrameSize:   jumboSize,
			MaxMessageSize: jumboSize,
			Hooks:          newTestHooks(t),
		})
		clientFrame := websocket.NewFrame(websocket.OpcodeText, true, bytes.Repeat([]byte("*"), jumboSize))
		mustWriteFrame(t, conn, true, clientFrame)
		respFrame := mustReadFrame(t, conn, jumboSize)
		assert.DeepEqual(t, respFrame.Payload, clientFrame.Payload, "payload")
	})

	t.Run("ping frames handled between fragments", func(t *testing.T) {
		t.Parallel()
		var (
			opts = newOpts(t)
			conn = setupRawConn(t, opts)
			ws   = setupWebsocketClient(t, conn, opts)
		)
		// write two fragmented frames with a ping control frame in between
		mustWriteFrames(t, conn, true, []*websocket.Frame{
			websocket.NewFrame(websocket.OpcodeText, false, []byte("0")),
			websocket.NewFrame(websocket.OpcodePing, true, nil),
			websocket.NewFrame(websocket.OpcodeContinuation, true, []byte("1")),
		})

		// should get a pong control frame first, even though ping was sent
		// as second frame
		pongFrame := mustReadFrame(t, conn, 125)
		assert.Equal(t, pongFrame.Opcode(), websocket.OpcodePong, "opcode")

		// then should get the echo'd message from the two fragments
		msg := mustReadMessage(t, ws)
		assert.DeepEqual(t, msg.Payload, []byte("01"), "incorrect messaage payload")
	})

	t.Run("pong frames from client are ignored", func(t *testing.T) {
		t.Parallel()
		// write two frames, pong control frame followed by text frame.
		//
		// pong frame should be ignored, text frame should be echoed
		conn := setupRawConn(t, newOpts(t))
		mustWriteFrames(t, conn, true, []*websocket.Frame{
			websocket.NewFrame(websocket.OpcodePong, true, nil),
			websocket.NewFrame(websocket.OpcodeText, true, []byte("0")),
		})
		respFrame := mustReadFrame(t, conn, 10)
		assert.DeepEqual(t, respFrame.Payload, []byte("0"), "payload")
	})
}

func TestProtocolErrors(t *testing.T) {
	var (
		maxDuration    = 250 * time.Millisecond
		maxFrameSize   = 128
		maxMessageSize = 256
	)

	newOpts := func(t *testing.T) websocket.Options {
		return websocket.Options{
			ReadTimeout:    maxDuration,
			WriteTimeout:   maxDuration,
			MaxFrameSize:   maxFrameSize,
			MaxMessageSize: maxMessageSize,
			Hooks:          newTestHooks(t),
		}
	}

	testCases := map[string]struct {
		frames          []*websocket.Frame
		wantCloseCode   websocket.StatusCode
		wantCloseReason error

		// further customize behavior
		opts     func(*testing.T) websocket.Options
		unmasked bool
	}{
		"unexpected continuation frame": {
			frames: []*websocket.Frame{
				websocket.NewFrame(websocket.OpcodeContinuation, true, []byte("0")),
			},
			wantCloseCode:   websocket.StatusProtocolError,
			wantCloseReason: websocket.ErrContinuationUnexpected,
		},
		"max frame size": {
			frames: []*websocket.Frame{
				websocket.NewFrame(websocket.OpcodeText, true, bytes.Repeat([]byte("0"), maxFrameSize+1)),
			},
			wantCloseCode:   websocket.StatusTooLarge,
			wantCloseReason: websocket.ErrFrameTooLarge,
		},
		"max message size": {
			// 3rd frame will exceed max message size
			frames: []*websocket.Frame{
				websocket.NewFrame(websocket.OpcodeText, false, bytes.Repeat([]byte("0"), maxFrameSize)),
				websocket.NewFrame(websocket.OpcodeContinuation, false, bytes.Repeat([]byte("1"), maxFrameSize)),
				websocket.NewFrame(websocket.OpcodeContinuation, true, []byte("2")),
			},
			wantCloseCode:   websocket.StatusTooLarge,
			wantCloseReason: websocket.ErrMessageTooLarge,
		},
		"server requires masked frames": {
			frames: []*websocket.Frame{
				websocket.NewFrame(websocket.OpcodeText, true, []byte("hello")),
			},
			unmasked:        true,
			wantCloseCode:   websocket.StatusProtocolError,
			wantCloseReason: websocket.ErrClientFrameUnmasked,
		},
		"server rejects RSV bits": {
			frames: []*websocket.Frame{
				websocket.NewFrame(websocket.OpcodeText, true, []byte("hello"), websocket.RSV1),
			},
			wantCloseCode:   websocket.StatusProtocolError,
			wantCloseReason: websocket.ErrRSVBitsUnsupported,
		},
		"control frames must not be fragmented": {
			frames: []*websocket.Frame{
				websocket.NewFrame(websocket.OpcodeClose, false, nil),
			},
			wantCloseCode:   websocket.StatusProtocolError,
			wantCloseReason: websocket.ErrControlFrameFragmented,
		},
		"control frames must be less than 125 bytes": {
			frames: []*websocket.Frame{
				websocket.NewFrame(websocket.OpcodeClose, true, bytes.Repeat([]byte("0"), 126)),
			},
			wantCloseCode:   websocket.StatusProtocolError,
			wantCloseReason: websocket.ErrControlFrameTooLarge,
		},
		"text messages must be valid utf8": {
			frames: []*websocket.Frame{
				websocket.NewFrame(websocket.OpcodeText, true, []byte{0xc3}),
			},
			wantCloseCode:   websocket.StatusInvalidFramePayload,
			wantCloseReason: websocket.ErrInvalidFramePayload,
		},
		"missing continuation frame": {
			frames: []*websocket.Frame{
				websocket.NewFrame(websocket.OpcodeText, false, bytes.Repeat([]byte("0"), maxFrameSize)),
				websocket.NewFrame(websocket.OpcodeText, true, bytes.Repeat([]byte("1"), maxFrameSize)),
			},
			wantCloseCode:   websocket.StatusProtocolError,
			wantCloseReason: websocket.ErrContinuationExpected,
		},
		"unknown opcode": {
			frames: []*websocket.Frame{
				websocket.NewFrame(websocket.Opcode(255), true, nil),
			},
			wantCloseCode:   websocket.StatusProtocolError,
			wantCloseReason: websocket.ErrOpcodeUnknown,
		},
	}
	for name, tc := range testCases {
		if tc.opts == nil {
			tc.opts = newOpts
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var opts websocket.Options
			if tc.opts != nil {
				opts = tc.opts(t)
			} else {
				opts = newOpts(t)
			}
			conn := setupRawConn(t, opts)

			// write erronous or invalid frames to the connection, which should
			// cause the server to start the closing handshake
			mustWriteFrames(t, conn, !tc.unmasked, tc.frames)

			// read expected close frame
			mustReadCloseFrame(t, conn, tc.wantCloseCode, tc.wantCloseReason)

			// server closes connection immediately on protocol errors,
			// without waiting for client to reply
			assertConnClosed(t, conn)
		})
	}
}

func TestCloseFrames(t *testing.T) {
	// construct a special close frame with an invalid utf8 reason outside of
	// test table because it can't be defined inline in a struct literal
	frameWithInvalidUTF8Reason := func() *websocket.Frame {
		payload := make([]byte, 0, 3)
		payload = binary.BigEndian.AppendUint16(payload, uint16(websocket.StatusNormalClosure))
		payload = append(payload, 0xc3)
		return websocket.NewFrame(websocket.OpcodeClose, true, payload)
	}

	testCases := map[string]struct {
		frame      *websocket.Frame
		wantCode   websocket.StatusCode
		wantReason string
	}{
		"empty payload ok": {
			frame:    websocket.NewFrame(websocket.OpcodeClose, true, nil),
			wantCode: websocket.StatusNormalClosure,
		},
		"one byte payload is illegal": {
			frame:      websocket.NewFrame(websocket.OpcodeClose, true, []byte("X")),
			wantCode:   websocket.StatusProtocolError,
			wantReason: websocket.ErrClosePayloadInvalid.Error(),
		},
		"invalid close code (too low)": {
			frame:      websocket.NewCloseFrame(websocket.StatusCode(999), ""),
			wantCode:   websocket.StatusProtocolError,
			wantReason: websocket.ErrCloseStatusInvalid.Error(),
		},
		"invalid close code (too high))": {
			frame:      websocket.NewCloseFrame(websocket.StatusCode(5001), ""),
			wantCode:   websocket.StatusProtocolError,
			wantReason: websocket.ErrCloseStatusInvalid.Error(),
		},
		"reserved close code": {
			frame:      websocket.NewCloseFrame(websocket.StatusCode(1015), ""),
			wantCode:   websocket.StatusProtocolError,
			wantReason: websocket.ErrCloseStatusReserved.Error(),
		},
		"invalid utf8 in close reason": {
			frame:      frameWithInvalidUTF8Reason(),
			wantCode:   websocket.StatusInvalidFramePayload,
			wantReason: websocket.ErrInvalidFramePayload.Error(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			conn := setupRawConn(t, websocket.Options{})
			t.Logf("sending close frame %v", tc.frame)
			assert.NilError(t, websocket.WriteFrame(conn, websocket.NewMaskingKey(), tc.frame))
			var wantErr error
			if tc.wantReason != "" {
				wantErr = errors.New(tc.wantReason)
			}
			mustReadCloseFrame(t, conn, tc.wantCode, wantErr)
		})
	}
}

var defaultOpts = websocket.Options{}

func TestCloseHandshake(t *testing.T) {
	t.Run("normal server-initiated closing handshake", func(t *testing.T) {
		// this test ensures that calling Close() on the server will initiate
		// and complete the full 2 way closing handshake with well-behaved
		// client.
		t.Parallel()

		// server starts closing handshake, will get no error if the client
		// finishes the handshake as expected
		var wg sync.WaitGroup
		wg.Add(1)
		clientConn := clientServerTest(t, defaultOpts, func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
			defer wg.Done()
			assert.NilError(t, ws.Close())
		})

		// client finishes closing handshake
		mustReadCloseFrame(t, clientConn, websocket.StatusNormalClosure, nil)
		mustWriteFrame(t, clientConn, true, websocket.NewCloseFrame(websocket.StatusNormalClosure, ""))
		wg.Wait()
		assertConnClosed(t, clientConn)
	})

	t.Run("client-initiated closing handshake", func(t *testing.T) {
		// this test ensures that a server will complete the 2 way closing
		// handshake when it is initiated by a well-behaved client.
		t.Parallel()

		// server replies to close frame automatically during a ReadMessage
		// call, which returns io.EOF when the connection is closed.
		var wg sync.WaitGroup
		wg.Add(1)
		clientConn := clientServerTest(t, defaultOpts, func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
			defer wg.Done()
			msg, err := ws.ReadMessage(t.Context())
			assert.Error(t, err, io.EOF)
			assert.Equal(t, msg, nil, "expected nil message")
		})

		// client initiates closing handshake and expects an appropriate reply
		// from the server
		mustWriteFrame(t, clientConn, true, websocket.NewCloseFrame(websocket.StatusNormalClosure, ""))
		mustReadCloseFrame(t, clientConn, websocket.StatusNormalClosure, nil)
		assertConnClosed(t, clientConn)

		wg.Wait()
	})

	t.Run("timeout waiting for handshake reply", func(t *testing.T) {
		// this test ensure the serve enforces a timeout when waiting for a
		// misbehaving client to reply to a closing handshake.
		t.Parallel()

		closeTimeout := 200 * time.Millisecond

		// server starts closing handshake, which should end with a timeout
		// error when the client does not reply in time
		var wg sync.WaitGroup
		wg.Add(1)
		clientConn := setupRawConnWithHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer wg.Done()
			ws, err := websocket.Accept(w, r, websocket.Options{
				CloseTimeout: closeTimeout,
			})
			assert.NilError(t, err)

			start := time.Now()
			closeErr := ws.Close()
			elapsed := time.Since(start)
			assert.Error(t, closeErr, os.ErrDeadlineExceeded)
			assert.Equal(t, elapsed > closeTimeout, true, "close should have waited for timeout")
		}))

		// client gets the closing handshake message but ignores it
		mustReadCloseFrame(t, clientConn, websocket.StatusNormalClosure, nil)
		wg.Wait()
		assertConnClosed(t, clientConn)
	})

	t.Run("client sends additional non-close frames before completing handshake", func(t *testing.T) {
		// this test ensures that the server properly receives but ignores any
		// additional data frames sent by the client after the closing
		// handshake is initiated but before the client replies with its own
		// close frame.
		t.Parallel()

		closeTimeout := 200 * time.Millisecond

		// server initiates closing handshake, which should complete without
		// error despite additional data frames sent by the client.
		var wg sync.WaitGroup
		wg.Add(1)
		clientConn := setupRawConnWithHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer wg.Done()
			ws, err := websocket.Accept(w, r, websocket.Options{
				CloseTimeout: closeTimeout,
			})
			assert.NilError(t, err)
			assert.NilError(t, ws.Close())
		}))

		// client receives initial closing handshake but sends additional data
		// before closing its end.
		mustReadCloseFrame(t, clientConn, websocket.StatusNormalClosure, nil)
		mustWriteFrames(t, clientConn, true, []*websocket.Frame{
			websocket.NewFrame(websocket.OpcodePing, true, nil),
			websocket.NewFrame(websocket.OpcodeText, true, []byte("ignore me")),
			websocket.NewCloseFrame(websocket.StatusNormalClosure, ""),
		})
		assertConnClosed(t, clientConn)
		wg.Wait()
	})

	t.Run("client initiates close but server reply fails", func(t *testing.T) {
		// this tests an edge case where the server gets a closing handshake
		// from the client but cannot reply for whatever reason.
		t.Parallel()

		var wg sync.WaitGroup
		wg.Add(1)
		clientConn := setupRawConnWithHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer wg.Done()

			// Manually re-implement websocket.Accept in order to wrap the
			// actual conn with one that will let us inject errors
			clientKey, err := websocket.Handshake(w, r)
			assert.NilError(t, err)

			hj := w.(http.Hijacker)
			realConn, _, err := hj.Hijack()
			assert.NilError(t, err)

			// any write by the server will fail
			writeErr := errors.New("simulated write error")
			fakeConn := &wrappedConn{
				conn: realConn,
				write: func(b []byte) (int, error) {
					return 0, writeErr
				},
			}

			ws := websocket.New(fakeConn, clientKey, websocket.ServerMode, websocket.Options{
				Hooks: newTestHooks(t),
			})
			msg, err := ws.ReadMessage(r.Context())
			assert.Error(t, err, writeErr)
			assert.Equal(t, msg, nil, "msg should be nil on error")
		}))

		// client starts closing handshake
		mustWriteFrame(t, clientConn, true, websocket.NewCloseFrame(websocket.StatusNormalClosure, ""))
		wg.Wait()
		assertConnClosed(t, clientConn)
	})

	t.Run("server fails to initiate close", func(t *testing.T) {
		// this tests an edge case where the server cannot even begin the
		// closing handshake due to a write error.
		t.Parallel()

		var wg sync.WaitGroup
		wg.Add(1)
		clientConn := setupRawConnWithHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer wg.Done()

			// Manually re-implement websocket.Accept in order to wrap the
			// actual conn with one that will let us inject errors
			clientKey, err := websocket.Handshake(w, r)
			assert.NilError(t, err)

			hj := w.(http.Hijacker)
			realConn, _, err := hj.Hijack()
			assert.NilError(t, err)

			// any write by the server will fail
			writeErr := errors.New("simulated write error")
			fakeConn := &wrappedConn{
				conn: realConn,
				write: func(b []byte) (int, error) {
					return 0, writeErr
				},
			}

			ws := websocket.New(fakeConn, clientKey, websocket.ServerMode, websocket.Options{
				Hooks: newTestHooks(t),
			})
			assert.Error(t, ws.Close(), writeErr)
		}))
		wg.Wait()
		assertConnClosed(t, clientConn)
	})
}

func TestNetworkErrors(t *testing.T) {
	t.Run("a write error should cause the server to close the connection", func(t *testing.T) {
		// the error we will inject into the wrapped writer, which will be
		// verified in the close frame
		writeErr := errors.New("simulated write failure")

		conn := setupRawConnWithHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Manually re-implement websocket.Accept in order to wrap the
			// actual conn with one that will let us inject errors
			clientKey, err := websocket.Handshake(w, r)
			assert.NilError(t, err)

			hj := w.(http.Hijacker)
			realConn, _, err := hj.Hijack()
			assert.NilError(t, err)

			// Our wrapped conn will fail on the first write and succeed on
			// any subsequent writes, so echoing the incoming frame will fail
			// but writing the subsequent close frame will succeed.
			//
			// Note: I'm not sure it actually makes sense to try writing the
			// close frame after an initial write error, but that's what we
			// do for now!
			var writeCount atomic.Int64
			fakeConn := &wrappedConn{
				conn: realConn,
				write: func(b []byte) (int, error) {
					count := writeCount.Add(1)
					// return an error on first write
					if count == 1 {
						return 0, writeErr
					}
					// otherwise pass thru to underlying conn
					return realConn.Write(b)
				},
			}

			ws := websocket.New(fakeConn, clientKey, websocket.ServerMode, websocket.Options{})
			_ = ws.Serve(r.Context(), websocket.EchoHandler)
		}))

		// We write a frame, but the server's attempt to write the echo
		// response should fail via fakeConn above.
		//
		// The _second_ write, writing the close frame before closing the
		// underlying conn, should succceed.
		mustWriteFrame(t, conn, true, websocket.NewFrame(websocket.OpcodeText, true, []byte("hello")))
		mustReadCloseFrame(t, conn, websocket.StatusAbnormalClose, writeErr)
	})

	t.Run("handle context cancelation between reads of multi frame message", func(t *testing.T) {
		// In this test, the client writes a partial message (fin=false) and
		// then the context is canceled, so we ensure that the server properly
		// handles cancelation between reads of multi-frame messages
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var wg sync.WaitGroup
		wg.Add(1)
		conn := setupRawConnWithHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer wg.Done()
			ws, err := websocket.Accept(w, r, websocket.Options{})
			assert.NilError(t, err)
			msg, err := ws.ReadMessage(ctx) // use context from test setup, not from request
			assert.Error(t, err, context.Canceled)
			assert.Equal(t, msg, nil, "expected nil message")
		}))

		wg.Add(1)
		go func() {
			defer wg.Done()
			mustWriteFrame(t, conn, true, websocket.NewFrame(websocket.OpcodeText, false, []byte("frame 1")))
			cancel()
			mustReadCloseFrame(t, conn, websocket.StatusInternalError, context.Canceled)
		}()
		wg.Wait()
	})
}

func TestServeLoop(t *testing.T) {
	t.Run("serve should close the connection when handler returns an error", func(t *testing.T) {
		// the error our websocket.Handler will return in response to a
		// specially crafted "fail" frame
		wantErr := errors.New("fail")

		conn := setupRawConnWithHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := websocket.Accept(w, r, websocket.Options{})
			assert.NilError(t, err)
			_ = ws.Serve(r.Context(), func(ctx context.Context, msg *websocket.Message) (*websocket.Message, error) {
				if bytes.Equal(msg.Payload, []byte("fail")) {
					return nil, wantErr
				}
				return msg, nil
			})
		}))

		mustWriteFrames(t, conn, true, []*websocket.Frame{
			websocket.NewFrame(websocket.OpcodeText, true, []byte("ok")),
			websocket.NewFrame(websocket.OpcodeText, true, []byte("fail")),
		})

		// first frame should be echoed as expected
		frame := mustReadFrame(t, conn, 128)
		assert.DeepEqual(t, frame.Payload, []byte("ok"), "incorrect payload")

		// second frame should cause the websocket.Handler used by the server
		// to return an error, which should cause the server to close the
		// connection
		mustReadCloseFrame(t, conn, websocket.StatusInternalError, wantErr)
	})
}

func TestClose(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup
	wg.Add(1)
	conn := setupRawConnWithHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer wg.Done()
		ws, err := websocket.Accept(w, r, websocket.Options{})
		assert.NilError(t, err)

		// server initiates and successfully completes closing handshake
		assert.NilError(t, ws.Close())

		// make sure any reads or writes after closing the connection are rejected
		{
			msg, err := ws.ReadMessage(t.Context())
			assert.Equal(t, msg, nil, "msg should be nil")
			assert.Error(t, err, websocket.ErrConnectionClosed)
		}
		{
			err := ws.WriteMessage(t.Context(), &websocket.Message{})
			assert.Error(t, err, websocket.ErrConnectionClosed)
		}
	}))

	mustReadCloseFrame(t, conn, websocket.StatusNormalClosure, nil)
	mustWriteFrame(t, conn, true, websocket.NewCloseFrame(websocket.StatusNormalClosure, ""))
	wg.Wait()
}

func mustReadFrame(t testing.TB, src io.Reader, maxPayloadLen int) *websocket.Frame {
	t.Helper()
	frame, err := websocket.ReadFrame(src, websocket.ClientMode, maxPayloadLen)
	assert.NilError(t, err)
	return frame
}

func mustReadMessage(t testing.TB, ws *websocket.Websocket) *websocket.Message {
	t.Helper()
	msg, err := ws.ReadMessage(t.Context())
	assert.NilError(t, err)
	return msg
}

func mustWriteFrame(t testing.TB, dst io.Writer, masked bool, frame *websocket.Frame) {
	t.Helper()
	mask := websocket.Unmasked
	if masked {
		mask = websocket.NewMaskingKey()
	}
	assert.NilError(t, websocket.WriteFrame(dst, mask, frame))
}

func mustWriteFrames(t testing.TB, dst io.Writer, masked bool, frames []*websocket.Frame) {
	t.Helper()
	for _, frame := range frames {
		mustWriteFrame(t, dst, masked, frame)
	}
}

// mustReadCloseFrame ensures that we can read a close frame from the given
// reader and optionally ensures that the close frame includes a specific
// status code and message.
func mustReadCloseFrame(t testing.TB, r io.Reader, wantCode websocket.StatusCode, wantErr error) {
	t.Helper()

	// All control frames MUST have a payload length of 125 bytes or less
	// and MUST NOT be fragmented.
	// https://datatracker.ietf.org/doc/html/rfc6455#section-5.5
	//
	// This is already enforced in validateFrame, called by ReadFrame, but we
	// must pass in a max payload size here.
	frame := mustReadFrame(t, r, 125)
	assert.Equal(t, frame.Opcode(), websocket.OpcodeClose, "opcode")

	// nothing else to validate
	if wantCode == 0 {
		return
	}

	assert.Equal(t, len(frame.Payload) >= 2, true, "expected close frame payload to be at least 2 bytes, got %v", frame.Payload)
	gotCode := websocket.StatusCode(binary.BigEndian.Uint16(frame.Payload[:2]))
	gotReason := string(frame.Payload[2:])
	assert.Equal(t, int(gotCode), int(wantCode), "incorrect close status code")
	if wantErr != nil {
		assert.Contains(t, gotReason, wantErr.Error(), "reason")
	}
}

// assertConnClosed ensures that the given connection is closed by attempting a
// read and ensuring that it returns an appropriate error.
func assertConnClosed(t testing.TB, conn net.Conn) {
	t.Helper()
	_, err := conn.Read(make([]byte, 1))
	// testing this is super annoying, because we get at least four kinds of
	// errors at various points in our test suite:
	//
	// - a *net.OpError with a non-deterministic strings containing random IP
	//   addresses and the substring "connection reset by peer"
	// - a concrete io.EOF value
	// - a concrete net.ErrClosed value
	//
	// The latter two are easy to test, the first one requires this ugly
	// substring check.
	assert.Equal(t, err != nil, true, "expected non-nil error when reading from closed connection")

	// first check ugly substring matches
	for _, substring := range []string{
		"connection reset by peer",
	} {
		if strings.Contains(err.Error(), substring) {
			return
		}
	}

	// finally check for concrete errors
	assert.Error(t, err, io.EOF, net.ErrClosed)
}

// setupRawConn starts an websocket echo server with the given options, does
// the client handshake, and returns the underlying TCP connection ready for
// sending/receiving websocket messages.
func setupRawConn(t testing.TB, opts websocket.Options) net.Conn {
	return setupRawConnWithHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// unset any hooks to avoid reliable but flaky data race somewhere in
		// the bowels of t.Logf that only triggers when newTestHooks are
		// passed to both a server and a client.
		//
		// basic attempts like manually locking around t.Logf calls were
		// unsuccessful.
		//
		// FIXME: for now, we just disable server side hooks until we can
		// find a fix.
		opts.Hooks = websocket.Hooks{}

		ws, err := websocket.Accept(w, r, opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = ws.Serve(r.Context(), websocket.EchoHandler)
	}))
}

// setupRawConnWithHandler starts a server with the given handler (which
// must be a websocket echo server), does the client handshake, and returns
// the underlying TCP connection ready for sending/receiving.
//
// Prefer setupRawConn for simple use cases, use this only when a test
// requires custom behavior (e.g. coordination via waitgroups).
func setupRawConnWithHandler(t testing.TB, handler http.Handler) net.Conn {
	t.Helper()

	srv := httptest.NewServer(handler)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	assert.NilError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Close()
	})

	// tie our conn to a bufio.Reader because we need to pass the latter into
	// http.ReadResponse below, which may end up reading some websocket frame
	// data in tests where the server writes to the conn immediately after the
	// handshake (e.g. where the server immediately closes the connection).
	//
	// this wrapper helps ensure that any data read into the buffer is still
	// available on subsequent reads directly from the conn.
	conn, br := newConnWithBufferedReader(conn)

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
	resp, err := http.ReadResponse(br, nil)
	assert.NilError(t, err)
	assert.StatusCode(t, resp, http.StatusSwitchingProtocols)

	return conn
}

type serverTestFunc func(testing.TB, *websocket.Websocket, net.Conn)

func clientServerTest(t testing.TB, opts websocket.Options, f serverTestFunc) net.Conn {
	t.Helper()

	var wg sync.WaitGroup
	wg.Add(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer wg.Done()

		// Manually re-implement websocket.Accept in order to capture the
		// underlying net.Conn
		clientKey, err := websocket.Handshake(w, r)
		assert.NilError(t, err)

		conn, _, err := w.(http.Hijacker).Hijack()
		assert.NilError(t, err)

		ws := websocket.New(conn, clientKey, websocket.ServerMode, opts)
		f(t, ws, conn)
	}))
	t.Cleanup(func() {
		// TODO: require all tests to cleanly close the connection?
		// assertConnClosed(t, conn)
		srv.Close()
	})

	clientConn, err := net.Dial("tcp", srv.Listener.Addr().String())
	assert.NilError(t, err)

	// tie our conn to a bufio.Reader because we need to pass the latter into
	// http.ReadResponse below, which may end up reading some websocket frame
	// data in tests where the server writes to the conn immediately after the
	// handshake (e.g. where the server immediately closes the connection).
	//
	// this wrapper helps ensure that any data read into the buffer is still
	// available on subsequent reads directly from the conn.
	clientConn, br := newConnWithBufferedReader(clientConn)

	handshakeReq := httptest.NewRequest(http.MethodGet, "/", nil)
	for k, v := range map[string]string{
		"Connection": "upgrade",
		"Upgrade":    "websocket",
		// "Sec-WebSocket-Key":     string(websocket.NewClientKey()),
		"Sec-WebSocket-Key":     fmt.Sprintf("client-%03d", clientCount.Add(1)),
		"Sec-WebSocket-Version": "13",
	} {
		handshakeReq.Header.Set(k, v)
	}
	// write the request line and headers, which should cause the
	// server to respond with a 101 Switching Protocols response.
	assert.NilError(t, handshakeReq.Write(clientConn))
	resp, err := http.ReadResponse(br, nil)
	assert.NilError(t, err)
	assert.StatusCode(t, resp, http.StatusSwitchingProtocols)

	return clientConn
}
func newConnWithBufferedReader(conn net.Conn) (*connWithBufferedReader, *bufio.Reader) {
	reader := bufio.NewReader(conn)
	return &connWithBufferedReader{
		Conn:   conn,
		reader: reader,
	}, reader
}

// connWithbufferedReader wraps a net.Conn with a bufio.Reader to ensure that
// any partial data left in the buffer is available to subsequent reads from
// the conn. See usage in setupRawConnWithHandler above for more explanation.
type connWithBufferedReader struct {
	net.Conn
	reader *bufio.Reader
}

func (c *connWithBufferedReader) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

var _ net.Conn = &connWithBufferedReader{}

// setupWebsocketClient wraps the given conn in a websocket client. The handshake and
// upgrade process must already be complete (see setupRawConn).
func setupWebsocketClient(t testing.TB, conn net.Conn, opts websocket.Options) *websocket.Websocket {
	t.Helper()
	return websocket.New(conn, websocket.ClientKey("test-client"), websocket.ClientMode, opts)
}

func newTestHooks(t testing.TB) websocket.Hooks {
	t.Helper()
	return websocket.Hooks{
		OnCloseHandshakeStart: func(key websocket.ClientKey, code websocket.StatusCode, err error) {
			// t.Logf("HOOK: client=%s OnCloseHandshakeStart code=%v err=%q", key, code, err)
			log.Printf("HOOK: client=%s OnCloseHandshakeStart code=%v err=%q", key, code, err)
		},
		OnCloseHandshakeDone: func(key websocket.ClientKey, code websocket.StatusCode, err error) {
			// t.Logf("HOOK: client=%s OnCloseHandshakeDone code=%v err=%q", key, code, err)
			log.Printf("HOOK: client=%s OnCloseHandshakeDone code=%v err=%q", key, code, err)
		},
		OnReadError: func(key websocket.ClientKey, err error) {
			// t.Logf("HOOK: client=%s OnReadError err=%v", key, err)
			log.Printf("HOOK: client=%s OnReadError err=%v", key, err)
		},
		OnReadFrame: func(key websocket.ClientKey, frame *websocket.Frame) {
			// t.Logf("HOOK: client=%s OnReadFrame frame=%v", key, frame)
			log.Printf("HOOK: client=%s OnReadFrame frame=%v", key, frame)
		},
		OnReadMessage: func(key websocket.ClientKey, msg *websocket.Message) {
			// t.Logf("HOOK: client=%s OnReadMessage msg=%v", key, msg)
			log.Printf("HOOK: client=%s OnReadMessage msg=%v", key, msg)
		},
		OnWriteError: func(key websocket.ClientKey, err error) {
			// t.Logf("HOOK: client=%s OnWriteError err=%v", key, err)
			log.Printf("HOOK: client=%s OnWriteError err=%v", key, err)
		},
		OnWriteFrame: func(key websocket.ClientKey, frame *websocket.Frame) {
			// t.Logf("HOOK: client=%s OnWriteFrame frame=%v", key, frame)
			log.Printf("HOOK: client=%s OnWriteFrame frame=%v", key, frame)
		},
		OnWriteMessage: func(key websocket.ClientKey, msg *websocket.Message) {
			// t.Logf("HOOK: client=%s OnWriteMessage msg=%v", key, msg)
			log.Printf("HOOK: client=%s OnWriteMessage msg=%v", key, msg)
		},
	}
}

// wrappedConn is a minimal wrapper around a net.Conn that allows tests to
// inject faults during Read, Write, and/or Close. Unless overridden, each
// method is proxied directly to the wrapped conn.
type wrappedConn struct {
	conn             net.Conn
	read             func([]byte) (int, error)
	write            func([]byte) (int, error)
	close            func() error
	setDeadline      func(time.Time) error
	setReadDeadline  func(time.Time) error
	setWriteDeadline func(time.Time) error
}

func (c *wrappedConn) Read(b []byte) (int, error) {
	if c.read != nil {
		return c.read(b)
	}
	return c.conn.Read(b)
}

func (c *wrappedConn) Write(b []byte) (int, error) {
	if c.write != nil {
		return c.write(b)
	}
	return c.conn.Write(b)
}

func (c *wrappedConn) Close() error {
	if c.close != nil {
		return c.close()
	}
	return c.conn.Close()
}

func (c *wrappedConn) SetDeadline(t time.Time) error {
	if c.setDeadline != nil {
		return c.setDeadline(t)
	}
	return c.conn.SetDeadline(t)
}

func (c *wrappedConn) SetReadDeadline(t time.Time) error {
	if c.setReadDeadline != nil {
		return c.setReadDeadline(t)
	}
	return c.conn.SetReadDeadline(t)
}

func (c *wrappedConn) SetWriteDeadline(t time.Time) error {
	if c.setWriteDeadline != nil {
		return c.setWriteDeadline(t)
	}
	return c.conn.SetWriteDeadline(t)
}

func (c *wrappedConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}
func (c *wrappedConn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

var (
	_ io.ReadWriteCloser = &wrappedConn{}
	_ net.Conn           = &wrappedConn{}
)

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

// ============================================================================
// Examples
// ============================================================================
func ExampleServe() {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, websocket.Options{
			ReadTimeout:    500 * time.Millisecond,
			WriteTimeout:   500 * time.Millisecond,
			MaxFrameSize:   16 << 10, // 16KiB
			MaxMessageSize: 1 << 20,  // 1MiB
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := ws.Serve(r.Context(), websocket.EchoHandler); err != nil {
			// an error returned by Serve is strictly informational; the
			// connection will already be closed at this point.
			log.Printf("error serving websocket connection: %s", err)
		}
	})
	if err := http.ListenAndServe(":8080", handler); err != nil {
		log.Fatalf("error starting server: %v", err)
	}
}
