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
	"unicode/utf8"

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
				_ = ws.Handle(r.Context(), websocket.EchoHandler)
			}))
			defer srv.Close()

			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
			for k, v := range tc.reqHeaders {
				req.Header.Set(k, v)
			}

			resp, err := http.DefaultClient.Do(req)
			assert.NilError(t, err)

			assert.Equal(t, resp.StatusCode, tc.wantStatus, "incorrect status code")
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
		// because the server has a read timeout and the client never sends a
		// message, the server's read loop will time out and it will start the
		// closing handshake.
		//
		// at that point, the client should be able to read the close frame
		// from the server, write its ACK, and then the connection should be
		// closed.
		maxDuration := 250 * time.Millisecond

		clientServerTest{
			// client never sends a message, just waits for the server to
			// timeout and then completes the closing handshake
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				start := time.Now()
				mustReadCloseFrame(t, conn, websocket.StatusAbnormalClose, errors.New("error reading frame header"))
				elapsed := time.Since(start)
				assert.Equal(t, elapsed >= maxDuration, true, "not enough time passed")
				mustWriteFrame(t, conn, true, websocket.NewCloseFrame(websocket.StatusNormalClosure, "server closed connection"))
				assertConnClosed(t, conn)
			},
			// server runs echo handler with read timeout, which should timeout
			// after maxDuration and start closing handshake
			serverOpts: websocket.Options{
				ReadTimeout:  maxDuration,
				WriteTimeout: maxDuration,
				Hooks:        newTestHooks(t),
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				start := time.Now()
				err := ws.Handle(t.Context(), websocket.EchoHandler)
				elapsed := time.Since(start)
				assert.Equal(t, elapsed >= maxDuration, true, "not enough time passed")
				assert.Error(t, err, os.ErrDeadlineExceeded)
			},
		}.Run(t)
	})

	t.Run("client closing connection", func(t *testing.T) {
		t.Parallel()

		serverTimeout := time.Hour // should never be reached

		clientServerTest{
			// client closes its end of the connection, which should interrupt
			// the server's blocking read and cause it to return.
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				assert.NilError(t, conn.Close())
			},
			// server tries to read, which should be interrupted by client
			// closing connection
			serverOpts: websocket.Options{
				ReadTimeout: serverTimeout,
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				start := time.Now()
				msg, err := ws.ReadMessage(t.Context())
				elapsed := time.Since(start)

				assert.Error(t, err, io.EOF)
				assert.Equal(t, msg, nil, "msg should be nil on error")
				assert.Equal(t, elapsed < serverTimeout, true, "server should not reach its own timeout")
			},
		}.Run(t)
	})
}

func TestProtocolOkay(t *testing.T) {
	t.Run("single frame round trip", func(t *testing.T) {
		t.Parallel()

		wantFrame := websocket.NewFrame(websocket.OpcodeText, true, []byte("hello"))

		clientServerTest{
			// client writes a single frame and ensures the server echoes the
			// same frame back
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustWriteFrame(t, conn, true, wantFrame)
				gotFrame := mustReadFrame(t, conn, len(wantFrame.Payload))
				assert.Equal(t, gotFrame, wantFrame, "frames should match")
			},
			// server reads a single frame, ensures it matches the frame
			// written by the client, and then echoes it back.
			serverTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				serverFrame := mustReadFrame(t, conn, len(wantFrame.Payload))
				assert.Equal(t, serverFrame, wantFrame, "frames should match")
				mustWriteFrame(t, conn, false, serverFrame)
			},
		}.Run(t)
	})

	t.Run("fragmented round trip", func(t *testing.T) {
		t.Parallel()

		var (
			// msg size must be double frame size for this test case
			maxFrameSize   = 128
			maxMessageSize = maxFrameSize * 2
			wantMessage    = &websocket.Message{
				Payload: bytes.Repeat([]byte("X"), maxMessageSize),
			}
		)

		clientServerTest{
			// client manually writes a fragmented message and reads reply
			// from server to ensure correct round trip
			clientOpts: websocket.Options{
				MaxFrameSize:   maxFrameSize,
				MaxMessageSize: maxMessageSize,
				Hooks:          newTestHooks(t),
			},
			clientTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
				// manually write fragmented message to ensure server reassembles
				// correctly
				mustWriteFrames(t, conn, true, websocket.FrameMessage(wantMessage, maxFrameSize))
				// read reply to verify round-trip
				assert.Equal(t, mustReadMessage(t, ws), wantMessage, "incorect message received in reply from server")
			},

			// server reads entire message and ensures that it is reassembled
			// correctly before echoing back to client
			serverOpts: websocket.Options{
				MaxFrameSize:   maxFrameSize,
				MaxMessageSize: maxMessageSize,
				Hooks:          newTestHooks(t),
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				msg := mustReadMessage(t, ws)
				assert.Equal(t, msg, wantMessage, "incorrect messaage received from client")
				assert.NilError(t, ws.WriteMessage(t.Context(), msg))
			},
		}.Run(t)
	})

	t.Run("utf8 handling okay", func(t *testing.T) {
		t.Parallel()
		clientServerTest{
			// client sends a variety of valid utf8-encoded messages and
			// ensures that the server echoes them correctly.
			clientTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
				// valid UTF-8 accepted and echoed back
				{
					frame := websocket.NewFrame(websocket.OpcodeText, true, []byte("Iñtërnâtiônàlizætiøn"))
					mustWriteFrame(t, conn, true, frame)
					msg := mustReadMessage(t, ws)
					assert.Equal(t, msg.Payload, frame.Payload, "incorrect message payloady")
				}

				// valid UTF-8 fragmented on codepoint boundaries is okay
				{
					mustWriteFrames(t, conn, true, []*websocket.Frame{
						websocket.NewFrame(websocket.OpcodeText, false, []byte("Iñtër")),
						websocket.NewFrame(websocket.OpcodeContinuation, false, []byte("nâtiônàl")),
						websocket.NewFrame(websocket.OpcodeContinuation, true, []byte("izætiøn")),
					})
					msg := mustReadMessage(t, ws)
					assert.Equal(t, msg.Payload, []byte("Iñtërnâtiônàlizætiøn"), "incorrect message payloady")
				}

				// valid UTF-8 fragmented in the middle of a codepoint is reassembled
				// into a valid message
				{
					mustWriteFrames(t, conn, true, []*websocket.Frame{
						websocket.NewFrame(websocket.OpcodeText, false, []byte("jalape\xc3")),
						websocket.NewFrame(websocket.OpcodeContinuation, true, []byte("\xb1o")),
					})
					msg := mustReadMessage(t, ws)
					assert.Equal(t, msg.Payload, []byte("jalapeño"), "payload")
				}

				assert.NilError(t, ws.Close())
			},

			// server just runs EchoHandler to reply to client
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				assert.NilError(t, ws.Handle(t.Context(), websocket.EchoHandler))
			},
		}.Run(t)
	})

	t.Run("binary messages can be invalid UTF-8", func(t *testing.T) {
		t.Parallel()

		wantMessage := &websocket.Message{Binary: true, Payload: []byte{0xc3}}
		assert.Equal(t, utf8.Valid(wantMessage.Payload), false, "test payload should not be valid utf8")

		clientServerTest{
			clientTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
				mustWriteFrames(t, conn, true, websocket.FrameMessage(wantMessage, len(wantMessage.Payload)))
				msg := mustReadMessage(t, ws)
				assert.Equal(t, msg, wantMessage, "client received incorrect message in reply")
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				msg := mustReadMessage(t, ws)
				assert.Equal(t, msg, wantMessage, "server received incorrect message")
				assert.NilError(t, ws.WriteMessage(t.Context(), msg))
			},
		}.Run(t)
	})

	t.Run("jumbo frames okay", func(t *testing.T) {
		t.Parallel()

		// Ensure we're exercising extended payload length parsing, where
		// payloads of length 65536 (64 KiB) or more must be encoded as 8
		// bytes
		jumboSize := 65536

		clientServerTest{
			clientOpts: websocket.Options{
				MaxFrameSize:   jumboSize,
				MaxMessageSize: jumboSize,
				Hooks:          newTestHooks(t),
			},
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				clientFrame := websocket.NewFrame(websocket.OpcodeText, true, bytes.Repeat([]byte("*"), jumboSize))
				mustWriteFrame(t, conn, true, clientFrame)
				respFrame := mustReadFrame(t, conn, jumboSize)
				assert.Equal(t, respFrame.Payload, clientFrame.Payload, "payload")
			},

			serverOpts: websocket.Options{
				MaxFrameSize:   jumboSize,
				MaxMessageSize: jumboSize,
				Hooks:          newTestHooks(t),
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				msg := mustReadMessage(t, ws)
				assert.NilError(t, ws.WriteMessage(t.Context(), msg))
			},
		}.Run(t)
	})

	t.Run("ping frames handled between fragments", func(t *testing.T) {
		t.Parallel()
		clientServerTest{
			// client writes a fragmented message with a ping control frame
			// between fragments and ensures that the server handles the ping
			// and then replies with the correct response.
			clientTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
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
				assert.Equal(t, msg.Payload, []byte("01"), "incorrect messaage payload")

				assert.NilError(t, ws.Close())
			},

			// server just echoes messages from the client
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				assert.NilError(t, ws.Handle(t.Context(), websocket.EchoHandler))
			},
		}.Run(t)
	})

	t.Run("pong frames from client are ignored", func(t *testing.T) {
		t.Parallel()
		clientServerTest{
			// client writes a pong control frame followed by a text frame and
			// ensures that the server ignores the pong frame and echoes the
			// text frame.
			clientTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
				wantPayload := []byte("hi")
				mustWriteFrames(t, conn, true, []*websocket.Frame{
					websocket.NewFrame(websocket.OpcodePong, true, nil),
					websocket.NewFrame(websocket.OpcodeText, true, wantPayload),
				})
				respFrame := mustReadFrame(t, conn, len(wantPayload))
				assert.Equal(t, respFrame.Payload, wantPayload, "payload")
				assert.NilError(t, ws.Close())
			},
			// server just echoes messages from the client
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				assert.NilError(t, ws.Handle(t.Context(), websocket.EchoHandler))
			},
		}.Run(t)
	})
}

func TestProtocolErrors(t *testing.T) {
	var (
		maxFrameSize   = 128
		maxMessageSize = maxFrameSize * 2
	)

	newOpts := func(t *testing.T) websocket.Options {
		return websocket.Options{
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
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			clientServerTest{
				// client writes erroneous our invalid frames, which should
				// cause the server to send a close frame.
				//
				// for these protocol-level errors, the server closes the
				// conection immediately after sending its close frame, not
				// waiting for the client to reply.
				clientTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
					mustWriteFrames(t, conn, !tc.unmasked, tc.frames)
					mustReadCloseFrame(t, conn, tc.wantCloseCode, tc.wantCloseReason)
					assertConnClosed(t, conn)
				},
				// server just runs echo handler, which should process all
				// protocol errors automatically
				serverOpts: newOpts(t),
				serverTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
					assert.Error(t, ws.Handle(t.Context(), websocket.EchoHandler), tc.wantCloseReason)
				},
			}.Run(t)
		})
	}
}

func TestCloseFrameValidation(t *testing.T) {
	// construct a special close frame with an invalid utf8 reason outside of
	// test table because it can't be defined inline in a struct literal
	frameWithInvalidUTF8Reason := func() *websocket.Frame {
		payload := make([]byte, 0, 3)
		payload = binary.BigEndian.AppendUint16(payload, uint16(websocket.StatusNormalClosure))
		payload = append(payload, 0xc3)
		return websocket.NewFrame(websocket.OpcodeClose, true, payload)
	}

	testCases := map[string]struct {
		frame    *websocket.Frame
		wantCode websocket.StatusCode
		wantErr  error
	}{
		"empty payload ok": {
			frame:    websocket.NewFrame(websocket.OpcodeClose, true, nil),
			wantCode: websocket.StatusNormalClosure,
		},
		"one byte payload is illegal": {
			frame:    websocket.NewFrame(websocket.OpcodeClose, true, []byte("X")),
			wantCode: websocket.StatusProtocolError,
			wantErr:  websocket.ErrClosePayloadInvalid,
		},
		"invalid close code (too low)": {
			frame:    websocket.NewCloseFrame(websocket.StatusCode(999), ""),
			wantCode: websocket.StatusProtocolError,
			wantErr:  websocket.ErrCloseStatusInvalid,
		},
		"invalid close code (too high))": {
			frame:    websocket.NewCloseFrame(websocket.StatusCode(5001), ""),
			wantCode: websocket.StatusProtocolError,
			wantErr:  websocket.ErrCloseStatusInvalid,
		},
		"reserved close code": {
			frame:    websocket.NewCloseFrame(websocket.StatusCode(1015), ""),
			wantCode: websocket.StatusProtocolError,
			wantErr:  websocket.ErrCloseStatusReserved,
		},
		"invalid utf8 in close reason": {
			frame:    frameWithInvalidUTF8Reason(),
			wantCode: websocket.StatusInvalidFramePayload,
			wantErr:  websocket.ErrInvalidFramePayload,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			clientServerTest{
				// client writes erroneous our invalid frames, which should
				// cause the server to send a close frame.
				//
				// for these protocol-level errors, the server closes the
				// conection immediately after sending its close frame, not
				// waiting for the client to reply.
				clientTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
					assert.NilError(t, websocket.WriteFrame(conn, websocket.NewMaskingKey(), tc.frame))
					mustReadCloseFrame(t, conn, tc.wantCode, tc.wantErr)
				},
				// server just runs echo handler, which should process all
				// protocol errors automatically
				serverTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
					assert.Error(t, ws.Handle(t.Context(), websocket.EchoHandler), tc.wantErr)
				},
			}.Run(t)
		})
	}
}

func TestCloseHandshake(t *testing.T) {
	t.Run("normal server-initiated closing handshake", func(t *testing.T) {
		// this test ensures that calling Close() on the server will initiate
		// and complete the full 2 way closing handshake with well-behaved
		// client.
		t.Parallel()

		clientServerTest{
			// client manually finishes closing handshake
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustReadCloseFrame(t, conn, websocket.StatusNormalClosure, nil)
				mustWriteFrame(t, conn, true, websocket.NewCloseFrame(websocket.StatusNormalClosure, ""))
				assertConnClosed(t, conn)
			},
			// server starts closing handshake, will get no error if the client
			// finishes the handshake as expected
			serverOpts: websocket.Options{
				CloseTimeout: 1 * time.Second,
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
				assert.NilError(t, ws.Close())
				assertConnClosed(t, conn)
			},
		}.Run(t)
	})

	t.Run("normal client-initiated closing handshake", func(t *testing.T) {
		// this test ensures that a server will complete the 2 way closing
		// handshake when it is initiated by a well-behaved client.
		t.Parallel()

		closeStatus := websocket.StatusGoingAway

		clientServerTest{
			// client initiates closing handshake and expects an appropriate reply
			// from the server
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustWriteFrame(t, conn, true, websocket.NewCloseFrame(closeStatus, ""))
				mustReadCloseFrame(t, conn, closeStatus, nil)
				assertConnClosed(t, conn)
			},
			// server replies to close frame automatically during a ReadMessage
			// call, which returns io.EOF when the connection is closed.
			serverTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
				msg, err := ws.ReadMessage(t.Context())
				assert.Error(t, err, io.EOF)
				assert.Equal(t, msg, nil, "expected nil message")
				assertConnClosed(t, conn)
			},
		}.Run(t)
	})

	t.Run("timeout waiting for handshake reply", func(t *testing.T) {
		// this test ensures the server enforces a timeout when waiting for a
		// misbehaving client to reply to a closing handshake.
		t.Parallel()

		closeTimeout := 250 * time.Millisecond

		clientServerTest{
			// client gets the closing handshake message but does not reply,
			// causing the server to time out while waiting
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustReadCloseFrame(t, conn, websocket.StatusNormalClosure, nil)
			},
			// server starts closing handshake, which should end with a timeout
			// error when the client does not reply in time
			serverOpts: websocket.Options{
				CloseTimeout: closeTimeout,
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				start := time.Now()
				closeErr := ws.Close()
				elapsed := time.Since(start)
				assert.Error(t, closeErr, os.ErrDeadlineExceeded)
				assert.Equal(t, elapsed > closeTimeout, true, "close should have waited for timeout")
			},
		}.Run(t)
	})

	t.Run("server initiates close but client closes conn without replying", func(t *testing.T) {
		// this tests the case where a client closes the connection without
		// replying when it receives the server's intitial closing handshake
		t.Parallel()
		clientServerTest{
			// server closes connection, expects io.EOF error when reading
			// reply from client, which is not treated as an error.
			serverTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
				assert.NilError(t, ws.Close())
			},
			// client gets intitial closing frame from server but closes its
			// end immediately
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustReadCloseFrame(t, conn, websocket.StatusNormalClosure, nil)
				assert.NilError(t, conn.Close())
			},
		}.Run(t)
	})

	t.Run("client sends additional non-close frames before completing handshake", func(t *testing.T) {
		// this test ensures that the server properly receives but ignores any
		// additional data frames sent by the client after the closing
		// handshake is initiated but before the client replies with its own
		// close frame.
		t.Parallel()

		closeTimeout := 1 * time.Second

		clientServerTest{
			// client receives initial closing handshake but sends additional data
			// before closing its end.
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustReadCloseFrame(t, conn, websocket.StatusNormalClosure, nil)
				mustWriteFrames(t, conn, true, []*websocket.Frame{
					websocket.NewFrame(websocket.OpcodePing, true, nil),
					websocket.NewFrame(websocket.OpcodeText, true, []byte("ignore me")),
					websocket.NewCloseFrame(websocket.StatusNormalClosure, ""),
				})
				assertConnClosed(t, conn)
			},
			// server initiates closing handshake, which should complete without
			// error despite additional data frames sent by the client.
			serverOpts: websocket.Options{
				CloseTimeout: closeTimeout,
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				assert.NilError(t, ws.Close())
			},
		}.Run(t)
	})

	t.Run("client initiates close but server reply fails", func(t *testing.T) {
		// this tests an edge case where the server gets a closing handshake
		// from the client but cannot reply for whatever reason.
		t.Parallel()

		// Define the error we'll inject when the server tries to write
		writeErr := errors.New("simulated write error")

		clientServerTest{
			// client starts closing handshake
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustWriteFrame(t, conn, true, websocket.NewCloseFrame(websocket.StatusNormalClosure, ""))
			},
			// server conn has write failures injected, which will prevent
			// the server from replying to the client's closing handshake.
			serverConn: func(conn net.Conn) net.Conn {
				return &wrappedConn{
					conn: conn,
					write: func(b []byte) (int, error) {
						return 0, writeErr
					},
				}
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
				msg, err := ws.ReadMessage(t.Context())
				assert.Error(t, err, writeErr)
				assert.Equal(t, msg, nil, "msg should be nil on error")
			},
		}.Run(t)
	})

	t.Run("server fails to initiate close", func(t *testing.T) {
		// this tests an edge case where the server cannot even begin the
		// closing handshake due to a write error.
		t.Parallel()

		// Define the error we'll inject when the server tries to write
		writeErr := errors.New("simulated write error")

		clientServerTest{
			// client should get an error on any action it takes because the
			// server will close the connection when its write fails
			clientTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				msg, err := ws.ReadMessage(t.Context())
				assert.Error(t, err, io.EOF)
				assert.Equal(t, msg, nil, "expected nil message on error")
			},
			// server wraps the connection to inject write errors, then tries
			// to close which should fail immediately
			serverConn: func(conn net.Conn) net.Conn {
				return &wrappedConn{
					conn: conn,
					write: func(b []byte) (int, error) {
						return 0, writeErr
					},
				}
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				assert.Error(t, ws.Close(), writeErr)
			},
		}.Run(t)
	})
}

func TestErrorHandling(t *testing.T) {
	t.Run("a write error should cause the server to close the connection", func(t *testing.T) {
		t.Parallel()

		// the error we will inject into the wrapped writer, which will be
		// verified in the close frame
		writeErr := errors.New("simulated write failure")

		clientServerTest{
			// client writes a frame, but the server's attempt to write the echo
			// response should fail. The server should then write a close frame
			// (which succeeds). Client reads the close frame and then closes
			// the connection to unblock the server.
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustWriteFrame(t, conn, true, websocket.NewFrame(websocket.OpcodeText, true, []byte("hello")))
				mustReadCloseFrame(t, conn, websocket.StatusAbnormalClose, writeErr)
				// reply to the close frame to complete the closing handshake
				mustWriteFrame(t, conn, true, websocket.NewCloseFrame(websocket.StatusNormalClosure, ""))
				assertConnClosed(t, conn)
			},
			// server conn has write failures injected: first write fails,
			// subsequent writes succeed. This allows the echo handler to fail
			// on the first write but succeed when writing the close frame.
			serverConn: func(conn net.Conn) net.Conn {
				var writeCount atomic.Int64
				return &wrappedConn{
					conn: conn,
					write: func(b []byte) (int, error) {
						count := writeCount.Add(1)
						// return an error on first write
						if count == 1 {
							return 0, writeErr
						}
						// otherwise pass thru to underlying conn
						return conn.Write(b)
					},
				}
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				err := ws.Handle(t.Context(), websocket.EchoHandler)
				assert.Error(t, err, writeErr)
			},
		}.Run(t)
	})

	t.Run("handle context cancelation between reads of multi frame message", func(t *testing.T) {
		// In this test, the client writes a partial message (fin=false) and
		// then the context is canceled, so we ensure that the server properly
		// handles cancelation between reads of multi-frame messages
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		clientServerTest{
			// client writes partial message, cancels context, then reads close frame
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustWriteFrame(t, conn, true, websocket.NewFrame(websocket.OpcodeText, false, []byte("frame 1")))
				cancel()
				mustReadCloseFrame(t, conn, websocket.StatusInternalError, context.Canceled)
			},
			// server tries to read message with the cancelable context, which
			// should be canceled while waiting for the continuation frame.
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				assert.Error(t, ws.Handle(ctx, websocket.EchoHandler), context.Canceled)
			},
		}.Run(t)
	})

	t.Run("handle context cancelation between writes of multi frame message", func(t *testing.T) {
		// In this test, the client writes a partial message (fin=false) and
		// then the context is canceled, so we ensure that the server properly
		// handles cancelation between reads of multi-frame messages
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		maxFrameSize := 16
		var payload []byte
		payload = append(payload, bytes.Repeat([]byte("0"), maxFrameSize)...)
		payload = append(payload, bytes.Repeat([]byte("1"), maxFrameSize)...)

		clientServerTest{
			// client reads first frame from multi-frame message, but then
			// the connection is closed after server fails to write second
			// frame
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				frame := mustReadFrame(t, conn, maxFrameSize*2)
				assert.Equal(t, frame.Payload, payload[:maxFrameSize], "incorrect payload")
				// complete closing handshake
				mustReadCloseFrame(t, conn, websocket.StatusNormalClosure, nil)
				mustWriteFrame(t, conn, true, websocket.NewCloseFrame(websocket.StatusNormalClosure, ""))
			},
			// server writes a multi-frame message, but the context is
			// canceled after the first frame is written.
			serverConn: func(conn net.Conn) net.Conn {
				var writeCount atomic.Int64
				return &wrappedConn{
					conn: conn,
					write: func(b []byte) (int, error) {
						n, err := conn.Write(b)
						// after first write, sleep until context is canceled
						// to ensure the write loop in WriteMessage sees the
						// context cancelation between frames
						if writeCount.Add(1) == 1 {
							<-ctx.Done()
						}
						return n, err
					},
				}
			},
			serverOpts: websocket.Options{
				MaxFrameSize: maxFrameSize,
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				go func() {
					time.Sleep(100 * time.Millisecond)
					cancel()
				}()
				msg := &websocket.Message{Payload: payload}
				assert.Error(t, ws.WriteMessage(ctx, msg), context.Canceled)
				assert.NilError(t, ws.Close())
			},
		}.Run(t)
	})

	t.Run("write error handling ping frame", func(t *testing.T) {
		// test behavior when a ping frame is received but writing the pong
		// reply fails
		t.Parallel()

		writeErr := errors.New("fake write error")

		clientServerTest{
			// client reads a message from the server
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustWriteFrame(t, conn, true, websocket.NewFrame(websocket.OpcodePing, true, nil))
			},
			// server calls ReadMessage twice, first getting an error from the
			// invalid frame and then getting the expected valid payload,
			// which is echoed back to the client.
			serverConn: func(conn net.Conn) net.Conn {
				return &wrappedConn{
					conn: conn,
					write: func(b []byte) (int, error) {
						return 0, writeErr
					},
				}
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				_, err := ws.ReadMessage(t.Context())
				assert.Error(t, err, writeErr)
			},
		}.Run(t)
	})

	t.Run("ReadMessage does not close connection on error", func(t *testing.T) {
		// This ensures that ReadMessage does not automatically close the
		// connection on a read error.
		t.Parallel()

		wantPayload := []byte("valid payload")

		clientServerTest{
			// client writes an invalid frame (which will cause a read error
			// on the server) and then a valid frame, server should echo the
			// valid frame.
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustWriteFrames(t, conn, true, []*websocket.Frame{
					websocket.NewFrame(websocket.OpcodeContinuation, false, []byte("invalid continuation frame")),
					websocket.NewFrame(websocket.OpcodeText, true, wantPayload),
				})
				mustReadFrame(t, conn, 128)
			},
			// server calls ReadMessage twice, first getting an error from the
			// invalid frame and then getting the expected valid payload,
			// which is echoed back to the client.
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				_, err := ws.ReadMessage(t.Context())
				assert.Error(t, err, websocket.ErrContinuationUnexpected)

				msg, err := ws.ReadMessage(t.Context())
				assert.NilError(t, err)
				assert.Equal(t, msg.Payload, wantPayload, "incorrect payload")
				assert.NilError(t, ws.WriteMessage(t.Context(), msg))
			},
		}.Run(t)
	})

	t.Run("WriteMessage does not close connection on error", func(t *testing.T) {
		// This ensures that WriteMessage does not automatically close the
		// connection on a write error.
		t.Parallel()

		var (
			writeErr    = errors.New("fake write error")
			wantPayload = []byte("valid payload")
		)

		clientServerTest{
			// client reads a message from the server
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				frame := mustReadFrame(t, conn, 128)
				assert.Equal(t, frame.Payload, wantPayload, "incorrect payload")
			},
			// server calls ReadMessage twice, first getting an error from the
			// invalid frame and then getting the expected valid payload,
			// which is echoed back to the client.
			serverConn: func(conn net.Conn) net.Conn {
				var writeCount atomic.Int64
				return &wrappedConn{
					conn: conn,
					write: func(b []byte) (int, error) {
						count := writeCount.Add(1)
						// return an error on first write
						if count == 1 {
							return 0, writeErr
						}
						// otherwise pass thru to underlying conn
						return conn.Write(b)
					},
				}
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				msg1 := &websocket.Message{Payload: []byte("failed write")}
				assert.Error(t, ws.WriteMessage(t.Context(), msg1), writeErr)

				msg2 := &websocket.Message{Payload: wantPayload}
				assert.NilError(t, ws.WriteMessage(t.Context(), msg2))
			},
		}.Run(t)
	})
}

func TestServeLoop(t *testing.T) {
	t.Run("serve should close the connection when handler returns an error", func(t *testing.T) {
		t.Parallel()

		// the error our websocket.Handler will return in response to a
		// specially crafted "fail" frame
		wantErr := errors.New("fail")

		clientServerTest{
			// client writes two frames: one that should be echoed, and one
			// that should cause the handler to return an error
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustWriteFrames(t, conn, true, []*websocket.Frame{
					websocket.NewFrame(websocket.OpcodeText, true, []byte("ok")),
					websocket.NewFrame(websocket.OpcodeText, true, []byte("fail")),
				})

				// first frame should be echoed as expected
				frame := mustReadFrame(t, conn, 128)
				assert.Equal(t, frame.Payload, []byte("ok"), "incorrect payload")

				// second frame should cause the handler to return an error,
				// which should cause the server to close the connection
				mustReadCloseFrame(t, conn, websocket.StatusInternalError, wantErr)
				// reply to the close frame to complete the closing handshake
				mustWriteFrame(t, conn, true, websocket.NewCloseFrame(websocket.StatusNormalClosure, ""))
				assertConnClosed(t, conn)
			},
			// server runs Serve with a custom handler that returns an error
			// when it receives a "fail" message
			serverTest: func(t testing.TB, ws *websocket.Websocket, _ net.Conn) {
				err := ws.Handle(t.Context(), func(ctx context.Context, msg *websocket.Message) (*websocket.Message, error) {
					if bytes.Equal(msg.Payload, []byte("fail")) {
						return nil, wantErr
					}
					return msg, nil
				})
				assert.Error(t, err, wantErr)
			},
		}.Run(t)
	})

	t.Run("error occures when closing connection after application error", func(t *testing.T) {
		t.Parallel()

		var (
			appErr   = errors.New("fake application error")
			writeErr = errors.New("fake write error")
		)

		clientServerTest{
			// client writes some frame to trigger handler
			clientTest: func(t testing.TB, _ *websocket.Websocket, conn net.Conn) {
				mustWriteFrame(t, conn, true, websocket.NewFrame(websocket.OpcodeText, true, []byte("hi")))
			},
			// server runs a handler that always returns an application error,
			// which should cause server to start closing handshake. closing
			// handshake should fail due to write error.
			serverConn: func(conn net.Conn) net.Conn {
				return &wrappedConn{
					conn: conn,
					write: func(b []byte) (int, error) {
						return 0, writeErr
					},
				}
			},
			serverTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
				err := ws.Handle(t.Context(), func(ctx context.Context, msg *websocket.Message) (*websocket.Message, error) {
					return nil, appErr
				})
				// the error returned wraps both the application-level error
				// that triggered the close ...
				assert.Error(t, err, appErr)
				// ... and the write error that prevented the closing
				// handshake from actually completing
				assert.Error(t, err, writeErr)
				assertConnClosed(t, conn)
			},
		}.Run(t)
	})
}

func TestClose(t *testing.T) {
	t.Parallel()

	clientServerTest{
		// server initiates and successfully completes closing handshake and
		// ensures that any subsequent use of the websocket is rejected.
		serverTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
			assert.NilError(t, ws.Close())
			{
				msg, err := ws.ReadMessage(t.Context())
				assert.Equal(t, msg, nil, "msg should be nil")
				assert.Error(t, err, websocket.ErrConnectionClosed)
			}
			{
				err := ws.WriteMessage(t.Context(), &websocket.Message{})
				assert.Error(t, err, websocket.ErrConnectionClosed)
			}
		},
		// client receives and finishes closing handshake
		clientTest: func(t testing.TB, ws *websocket.Websocket, conn net.Conn) {
			mustReadCloseFrame(t, conn, websocket.StatusNormalClosure, nil)
			mustWriteFrame(t, conn, true, websocket.NewCloseFrame(websocket.StatusNormalClosure, ""))
		},
	}.Run(t)
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
		assert.True(t, strings.Contains(gotReason, wantErr.Error()), "incorrect close reason")
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
	switch {
	case errors.Is(err, io.EOF):
	case errors.Is(err, net.ErrClosed):
		// okay, we wanted one of these errors
	default:
		t.Errorf("unexpected error: %s", err)
	}
}

// clientServerTestFunc is a callback invoked by [clientServerTest.Run]to
// execute either the client or server portion of a test.
//
// Each test func will be called in a separate goroutine. The [Websocket]
// will be configured with clientOpts or serverOpts and the [net.Conn] will be
// configured with clientConn or serverConn (if given).
type clientServerTestFunc func(testing.TB, *websocket.Websocket, net.Conn)

// clientServerTest encapsulates coordinates a unit test involving a websocket
// client and server communicating with each other. See [clientServerTest.Run]
// for details.
type clientServerTest struct {
	clientTest clientServerTestFunc
	clientOpts websocket.Options
	clientKey  websocket.ClientKey
	clientConn func(net.Conn) net.Conn

	serverTest clientServerTestFunc
	serverOpts websocket.Options
	serverConn func(net.Conn) net.Conn
}

// Run runs a client-server test by a) running a real, ephemeral websocket
// server using httptest b) setting up a websocket client c) calling the
// given clientTest and serverTest callbacks and d) ensuring that both
// callbacks complete before returning.
//
// The client and server [websocket.Websocket] instances can be configured via
// [clientOpts] and [serverOpts]. The underlying [net.Conn] can be configured
// (e.g. wrapped, having deadlines set, etc) via [clientConn] and [serverConn].
func (cst clientServerTest) Run(t testing.TB) {
	t.Helper()

	if cst.clientKey == "" {
		cst.clientKey = websocket.NewClientKey()
	}

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

		// optionally wrap conn before handing it off to test function
		if cst.serverConn != nil {
			conn = cst.serverConn(conn)
		}

		ws := websocket.New(conn, clientKey, websocket.ServerMode, cst.serverOpts)
		cst.serverTest(t, ws, conn)
	}))
	t.Cleanup(func() {
		// TODO: require all tests to cleanly close the connection?
		// assertConnClosed(t, conn)
		srv.Close()
	})

	wg.Add(1)
	go func() {
		defer wg.Done()

		clientConn, err := net.Dial("tcp", srv.Listener.Addr().String())
		assert.NilError(t, err)
		t.Cleanup(func() {
			_ = clientConn.Close()
		})

		// optionally wrap client conn before handing it off to test function
		if cst.clientConn != nil {
			clientConn = cst.clientConn(clientConn)
		}

		// tie our client conn to a bufio.Reader because we need to pass the latter into
		// http.ReadResponse below, which may end up reading some websocket frame
		// data in tests where the server writes to the conn immediately after the
		// handshake (e.g. where the server immediately closes the connection).
		//
		// this wrapper helps ensure that any data read into the buffer is still
		// available on subsequent reads directly from the conn.
		clientConn, br := newConnWithBufferedReader(clientConn)

		handshakeReq := httptest.NewRequest(http.MethodGet, "/", nil)
		for k, v := range map[string]string{
			"Connection":            "upgrade",
			"Upgrade":               "websocket",
			"Sec-WebSocket-Key":     string(cst.clientKey),
			"Sec-WebSocket-Version": "13",
		} {
			handshakeReq.Header.Set(k, v)
		}
		// write the request line and headers, which should cause the
		// server to respond with a 101 Switching Protocols response.
		assert.NilError(t, handshakeReq.Write(clientConn))
		resp, err := http.ReadResponse(br, nil)
		assert.NilError(t, err)
		assert.Equal(t, resp.StatusCode, http.StatusSwitchingProtocols, "incorrect status code")

		clientSock := websocket.New(clientConn, cst.clientKey, websocket.ClientMode, cst.clientOpts)
		cst.clientTest(t, clientSock, clientConn)
	}()

	wg.Wait()
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

func newTestHooks(t testing.TB) websocket.Hooks {
	t.Helper()
	return websocket.Hooks{
		OnCloseHandshakeStart: func(key websocket.ClientKey, code websocket.StatusCode, err error) {
			t.Logf("HOOK: client=%s OnCloseHandshakeStart code=%v err=%q", key, code, err)
		},
		OnCloseHandshakeDone: func(key websocket.ClientKey, code websocket.StatusCode, err error) {
			t.Logf("HOOK: client=%s OnCloseHandshakeDone code=%v err=%q", key, code, err)
		},
		OnReadError: func(key websocket.ClientKey, err error) {
			t.Logf("HOOK: client=%s OnReadError err=%v", key, err)
		},
		OnReadFrame: func(key websocket.ClientKey, frame *websocket.Frame) {
			t.Logf("HOOK: client=%s OnReadFrame frame=%v", key, frame)
		},
		OnReadMessage: func(key websocket.ClientKey, msg *websocket.Message) {
			t.Logf("HOOK: client=%s OnReadMessage msg=%v", key, msg)
		},
		OnWriteError: func(key websocket.ClientKey, err error) {
			t.Logf("HOOK: client=%s OnWriteError err=%v", key, err)
		},
		OnWriteFrame: func(key websocket.ClientKey, frame *websocket.Frame) {
			t.Logf("HOOK: client=%s OnWriteFrame frame=%v", key, frame)
		},
		OnWriteMessage: func(key websocket.ClientKey, msg *websocket.Message) {
			t.Logf("HOOK: client=%s OnWriteMessage msg=%v", key, msg)
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
func ExampleHandle() {
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
		if err := ws.Handle(r.Context(), websocket.EchoHandler); err != nil {
			// an error returned by Handle is strictly informational; the
			// connection will already be closed at this point.
			log.Printf("error handling websocket connection: %s", err)
		}
	})
	if err := http.ListenAndServe(":8080", handler); err != nil {
		log.Fatalf("error starting server: %v", err)
	}
}
