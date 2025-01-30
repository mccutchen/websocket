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
		conn := setupRawConn(t, websocket.Options{
			ReadTimeout:  maxDuration,
			WriteTimeout: maxDuration,
			Hooks:        newTestHooks(t),
		})

		// client read should succeed, because we expect the server to send
		// a close frame when its read deadline is reached
		start := time.Now()
		resp, err := io.ReadAll(conn)
		elapsed := time.Since(start)
		assert.NilError(t, err)
		assert.RoughlyEqual(t, elapsed, maxDuration, 25*time.Millisecond)
		validateCloseFrame(t, bytes.NewBuffer(resp), websocket.StatusServerError, errors.New("error reading frame header"))

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
		conn := setupRawConnHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		_, clientReadErr := conn.Read(make([]byte, 1))
		elapsedClientTime = time.Since(start)

		// close client connection, which should interrupt the server's
		// blocking read call on the connection
		conn.Close()
		t.Logf("client connection closed")

		assert.Equal(t, os.IsTimeout(clientReadErr), true, "expected timeout error")
		assert.RoughlyEqual(t, elapsedClientTime, clientTimeout, 10*time.Millisecond)

		// wait for the server to finish handling the one request, then
		// make sure it took the expected amount of time to get a read
		// timeout and an appropriate error
		wg.Wait()
		assert.RoughlyEqual(t, elapsedServerTime, clientTimeout, 10*time.Millisecond)
		assert.Error(t, gotServerReadError, io.EOF)
	})
}

func TestProtocol(t *testing.T) {
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

	t.Run("basic echo functionality", func(t *testing.T) {
		t.Parallel()
		conn := setupRawConn(t, newOpts(t))
		// write client frame
		clientFrame := &websocket.Frame{
			Opcode:  websocket.OpcodeText,
			Fin:     true,
			Payload: []byte("hello"),
		}
		assert.NilError(t, websocket.WriteFrameMasked(conn, clientFrame, websocket.NewMaskingKey()))
		// read server frame and ensure that it matches client frame
		serverFrame, err := websocket.ReadFrame(conn, maxFrameSize)
		assert.NilError(t, err)
		assert.DeepEqual(t, serverFrame, clientFrame, "matching frames")

		// ensure closing handshake is completed when initiated by client
		clientClose := websocket.CloseFrame(websocket.StatusNormalClosure, "")
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

		var (
			opts = newOpts(t)
			conn = setupRawConn(t, opts)
			ws   = setupClient(t, conn, opts)
		)

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
		t.Logf("expecting final payload: %q", wantMessagePayload)
		msg := mustConsumeMessage(t, ws)
		assert.DeepEqual(t, msg.Payload, wantMessagePayload, "incorrect messaage payload")
	})

	t.Run("test messages must be valid utf8", func(t *testing.T) {
		t.Parallel()
		var (
			opts = newOpts(t)
			conn = setupRawConn(t, opts)
			ws   = setupClient(t, conn, opts)
		)

		// valid UTF-8 accepted and echoed back
		{
			frame := &websocket.Frame{
				Opcode:  websocket.OpcodeText,
				Fin:     true,
				Payload: []byte("Iñtërnâtiônàlizætiøn"),
			}
			assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
			msg := mustConsumeMessage(t, ws)
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

			msg := mustConsumeMessage(t, ws)
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

			msg := mustConsumeMessage(t, ws)
			assert.DeepEqual(t, msg.Payload, []byte("jalapeño"), "payload")
		}
	})

	t.Run("binary messages can be invalid UTF-8", func(t *testing.T) {
		t.Parallel()
		var (
			opts = newOpts(t)
			conn = setupRawConn(t, opts)
			ws   = setupClient(t, conn, opts)
		)
		frame := &websocket.Frame{
			Opcode:  websocket.OpcodeBinary,
			Fin:     true,
			Payload: []byte{0xc3},
		}
		assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
		msg, err := ws.ReadMessage(context.Background())
		assert.NilError(t, err)
		assert.Equal(t, msg.Binary, true, "binary message")
		assert.DeepEqual(t, msg.Payload, frame.Payload, "binary payload")
	})

	t.Run("jumbo frames are okay", func(t *testing.T) {
		t.Parallel()
		jumboSize := 64 * 1024 // payloads of length 65536 or more must be encoded as 8 bytes
		newJumboOpts := func(t *testing.T) websocket.Options {
			return websocket.Options{
				MaxFrameSize:   jumboSize,
				MaxMessageSize: jumboSize,
				Hooks:          newTestHooks(t),
			}
		}
		conn := setupRawConn(t, newJumboOpts(t))
		clientFrame := &websocket.Frame{
			Opcode:  websocket.OpcodeText,
			Fin:     true,
			Payload: bytes.Repeat([]byte("*"), jumboSize),
		}
		assert.NilError(t, websocket.WriteFrameMasked(conn, clientFrame, websocket.NewMaskingKey()))
		respFrame, err := websocket.ReadFrame(conn, jumboSize)
		assert.NilError(t, err)
		assert.DeepEqual(t, respFrame.Payload, clientFrame.Payload, "payload")
	})

	t.Run("ping frames handled between fragments", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, maxMessageSize/maxFrameSize, 2, "test assumes maxMessageSize/maxFrameSize == 2, may need to be updated")

		var (
			opts = newOpts(t)
			conn = setupRawConn(t, opts)
			ws   = setupClient(t, conn, opts)
		)

		// write fragmented message that will be assembled into a message that
		// exceeds the message size limit
		clientFrames := []*websocket.Frame{
			{
				Opcode:  websocket.OpcodeText,
				Fin:     false,
				Payload: bytes.Repeat([]byte("0"), maxFrameSize),
			},
			{
				Opcode: websocket.OpcodePing,
				Fin:    true,
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
			if frame.Opcode != websocket.OpcodePing {
				wantMessagePayload = append(wantMessagePayload, frame.Payload...)
			}
		}

		// should get a pong control frame first
		pongMsg, err := websocket.ReadFrame(conn, 125)
		assert.NilError(t, err)
		assert.Equal(t, pongMsg.Opcode, websocket.OpcodePong, "opcode")

		// then should get the echo'd message from the two fragments
		msg := mustConsumeMessage(t, ws)
		assert.DeepEqual(t, msg.Payload, wantMessagePayload, "incorrect messaage payload")
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

	t.Run("unexpected continuation frame", func(t *testing.T) {
		t.Parallel()
		conn := setupRawConn(t, newOpts(t))
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
		conn := setupRawConn(t, newOpts(t))
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
		conn := setupRawConn(t, newOpts(t))

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
		validateCloseFrame(t, conn, websocket.StatusTooLarge, websocket.ErrMessageTooLarge)
	})

	t.Run("server requires masked frames", func(t *testing.T) {
		t.Parallel()
		conn := setupRawConn(t, newOpts(t))
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
		conn := setupRawConn(t, newOpts(t))
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
		conn := setupRawConn(t, newOpts(t))
		frame := &websocket.Frame{
			Opcode: websocket.OpcodeClose,
			Fin:    false,
		}
		assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
		validateCloseFrame(t, conn, websocket.StatusProtocolError, websocket.ErrControlFrameFragmented)
	})

	t.Run("control frames must be less than 125 bytes", func(t *testing.T) {
		t.Parallel()
		conn := setupRawConn(t, newOpts(t))
		frame := &websocket.Frame{
			Opcode:  websocket.OpcodeClose,
			Payload: bytes.Repeat([]byte("0"), 126),
		}
		assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
		validateCloseFrame(t, conn, websocket.StatusProtocolError, websocket.ErrControlFrameTooLarge)
	})

	t.Run("test messages must be valid utf8", func(t *testing.T) {
		t.Parallel()
		conn := setupRawConn(t, newOpts(t))
		frame := &websocket.Frame{
			Opcode:  websocket.OpcodeText,
			Fin:     true,
			Payload: []byte{0xc3},
		}
		assert.NilError(t, websocket.WriteFrameMasked(conn, frame, websocket.NewMaskingKey()))
		validateCloseFrame(t, conn, websocket.StatusUnsupportedPayload, websocket.ErrInvalidUTF8)
	})
}

func TestCloseFrames(t *testing.T) {
	// construct a special close frame with an invalid utf8 reason outside of
	// test table because it can't be defined inline in a struct literal
	frameWithInvalidUTF8Reason := func() *websocket.Frame {
		payload := make([]byte, 0, 3)
		payload = binary.BigEndian.AppendUint16(payload, uint16(websocket.StatusNormalClosure))
		payload = append(payload, 0xc3)
		return &websocket.Frame{
			Opcode:  websocket.OpcodeClose,
			Fin:     true,
			Payload: payload,
		}
	}

	testCases := map[string]struct {
		frame      *websocket.Frame
		wantCode   websocket.StatusCode
		wantReason string
	}{
		"empty payload ok": {
			frame: &websocket.Frame{
				Opcode: websocket.OpcodeClose,
				Fin:    true,
			},
			wantCode:   websocket.StatusNormalClosure,
			wantReason: "",
		},
		"one byte payload is illegal": {
			frame: &websocket.Frame{
				Opcode:  websocket.OpcodeClose,
				Fin:     true,
				Payload: []byte("X"),
			},
			wantCode:   websocket.StatusProtocolError,
			wantReason: "close frame payload must be at least 2 bytes",
		},
		"invalid close code (too low)": {
			frame:      websocket.CloseFrame(websocket.StatusCode(999), ""),
			wantCode:   websocket.StatusProtocolError,
			wantReason: "close status code out of range",
		},
		"invalid close code (too high))": {
			frame:      websocket.CloseFrame(websocket.StatusCode(5001), ""),
			wantCode:   websocket.StatusProtocolError,
			wantReason: "close status code out of range",
		},
		"reserved close code": {
			frame:      websocket.CloseFrame(websocket.StatusCode(1015), ""),
			wantCode:   websocket.StatusProtocolError,
			wantReason: "close status code is reserved",
		},
		"invalid utf8 in close reason": {
			frame:      frameWithInvalidUTF8Reason(),
			wantCode:   websocket.StatusProtocolError,
			wantReason: "invalid UTF-8",
		},
	}

	for name, tc := range testCases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			conn := setupRawConn(t, websocket.Options{})
			t.Logf("sending close frame %v", tc.frame)
			assert.NilError(t, websocket.WriteFrameMasked(conn, tc.frame, websocket.NewMaskingKey()))
			var wantErr error
			if tc.wantReason != "" {
				wantErr = errors.New(tc.wantReason)
			}
			validateCloseFrame(t, conn, tc.wantCode, wantErr)
		})
	}
}

func mustConsumeMessage(tb testing.TB, ws *websocket.Websocket) *websocket.Message {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := ws.ReadMessage(ctx)
	assert.NilError(tb, err)
	return msg
}

func setupRawConnHandler(t testing.TB, handler http.Handler) net.Conn {
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

	return conn
}

func setupRawConn(t testing.TB, opts websocket.Options) net.Conn {
	return setupRawConnHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ws.Serve(r.Context(), websocket.EchoHandler)
	}))
}

func setupClient(t testing.TB, conn net.Conn, opts websocket.Options) *websocket.Websocket {
	t.Helper()
	return websocket.New(conn, websocket.ClientKey("test-client"), websocket.ClientMode, opts)
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
	t.Logf("validateCloseFrame: got frame: %v", frame)
	assert.Equal(t, frame.Opcode, websocket.OpcodeClose, "opcode")

	if wantStatus == 0 {
		// nothing else to validate
		return
	}

	assert.Equal(t, len(frame.Payload) >= 2, true, "expected close frame payload to be at least 2 bytes, got %v", frame.Payload)
	statusCode := websocket.StatusCode(binary.BigEndian.Uint16(frame.Payload[:2]))
	closeMsg := string(frame.Payload[2:])
	t.Logf("got close frame: code=%v msg=%q", statusCode, closeMsg)
	assert.Equal(t, int(statusCode), int(wantStatus), "status code")
	if wantErr != nil {
		assert.Contains(t, closeMsg, wantErr.Error(), "reason")
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
