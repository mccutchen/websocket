package websocket_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/mccutchen/websocket"
	"github.com/mccutchen/websocket/internal/testing/assert"
)

func TestFrameRoundTrip(t *testing.T) {
	// Basic test to ensure that we can read back the same frame that we
	// write.
	t.Parallel()

	// write masked "client" frame to buffer
	clientFrame := websocket.NewFrame(websocket.OpcodeText, true, []byte("hello"))
	buf := &bytes.Buffer{}
	assert.NilError(t, websocket.WriteFrame(buf, websocket.NewMaskingKey(), clientFrame))

	// read "server" frame from buffer.
	serverFrame, err := websocket.ReadFrame(buf, websocket.ServerMode, len(clientFrame.Payload))
	assert.NilError(t, err)

	// ensure client and server frame match
	assert.DeepEqual(t, serverFrame, clientFrame, "server and client frame mismatch")
}

func TestMaxFrameSize(t *testing.T) {
	// Basic test to ensure that we can read back the same frame that we
	// write.
	t.Parallel()

	// write masked "client" frame to buffer
	clientFrame := websocket.NewFrame(websocket.OpcodeText, true, []byte("hello"))
	buf := &bytes.Buffer{}
	assert.NilError(t, websocket.WriteFrame(buf, websocket.NewMaskingKey(), clientFrame))

	// read "server" frame from buffer.
	serverFrame, err := websocket.ReadFrame(buf, websocket.ServerMode, len(clientFrame.Payload)-1)
	assert.Error(t, err, websocket.ErrFrameTooLarge)
	assert.Equal(t, serverFrame, nil, "expected nil frame on error")
}

func TestRSV(t *testing.T) {
	// We don't currently support any extensions, so RSV bits are not allowed.
	// But we still need to be able properly parse and marshal them.
	marshalledFrame := func(rsvBits ...websocket.RSVBit) []byte {
		frame := websocket.NewFrame(websocket.OpcodeText, true, nil, rsvBits...)
		return websocket.MarshalFrame(frame, websocket.Unmasked)
	}

	testCases := map[string]struct {
		rawBytes []byte
		wantRSV1 bool
		wantRSV2 bool
		wantRSV3 bool
	}{
		"no RSV bits set": {
			rawBytes: marshalledFrame(),
		},
		"RSV1 set": {
			rawBytes: marshalledFrame(websocket.RSV1),
			wantRSV1: true,
		},
		"RSV2 set": {
			rawBytes: marshalledFrame(websocket.RSV2),
			wantRSV2: true,
		},
		"RSV3 set": {
			rawBytes: marshalledFrame(websocket.RSV3),
			wantRSV3: true,
		},
		"all RSV bits set": {
			rawBytes: marshalledFrame(websocket.RSV1, websocket.RSV2, websocket.RSV3),
			wantRSV1: true,
			wantRSV2: true,
			wantRSV3: true,
		},
	}
	for name, tc := range testCases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			buf := bytes.NewReader(tc.rawBytes)
			frame := mustReadFrame(t, buf, len(tc.rawBytes))
			assert.Equal(t, frame.RSV1(), tc.wantRSV1, "incorrect RSV1")
			assert.Equal(t, frame.RSV2(), tc.wantRSV2, "incorrect RSV2")
			assert.Equal(t, frame.RSV3(), tc.wantRSV3, "incorrect RSV3")
		})
	}
}

func TestExampleFramesFromRFC(t *testing.T) {
	// This tests every example provided in RFC 6455 section 5.7:
	// https://datatracker.ietf.org/doc/html/rfc6455#section-5.7
	testCases := map[string]struct {
		rawBytes  []byte
		wantFrame *websocket.Frame
	}{
		"single-frame unmasked text": {
			rawBytes:  []byte{0x81, 0x05, 0x48, 0x65, 0x6c, 0x6c, 0x6f},
			wantFrame: websocket.NewFrame(websocket.OpcodeText, true, []byte("Hello")),
		},
		"single-frame masked text": {
			rawBytes:  []byte{0x81, 0x85, 0x37, 0xfa, 0x21, 0x3d, 0x7f, 0x9f, 0x4d, 0x51, 0x58},
			wantFrame: websocket.NewFrame(websocket.OpcodeText, true, []byte("Hello")),
		},
		"fragmented unmasked text part 1": {
			rawBytes:  []byte{0x01, 0x03, 0x48, 0x65, 0x6c},
			wantFrame: websocket.NewFrame(websocket.OpcodeText, false, []byte("Hel")),
		},
		"fragmented unmasked text part 2": {
			rawBytes:  []byte{0x80, 0x02, 0x6c, 0x6f},
			wantFrame: websocket.NewFrame(websocket.OpcodeContinuation, true, []byte("lo")),
		},
		"unmasked ping": {
			rawBytes: []byte{
				0x89, 0x05, 0x48, 0x65, 0x6c, 0x6c, 0x6f,
			},
			wantFrame: websocket.NewFrame(websocket.OpcodePing, true, []byte("Hello")),
		},
		"masked ping response": {
			rawBytes:  []byte{0x8a, 0x85, 0x37, 0xfa, 0x21, 0x3d, 0x7f, 0x9f, 0x4d, 0x51, 0x58},
			wantFrame: websocket.NewFrame(websocket.OpcodePong, true, []byte("Hello")),
		},
		"256 bytes binary message": {
			rawBytes: append(
				[]byte{0x82, 0x7E, 0x01, 0x00},
				make([]byte, 256)...,
			),
			wantFrame: websocket.NewFrame(websocket.OpcodeBinary, true, make([]byte, 256)),
		},
		"64KiB binary message": {
			rawBytes: append(
				[]byte{0x82, 0x7F, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00},
				make([]byte, 65536)...,
			),
			wantFrame: websocket.NewFrame(websocket.OpcodeBinary, true, make([]byte, 65536)),
		},
	}

	for name, tc := range testCases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			buf := bytes.NewReader(tc.rawBytes)
			got := mustReadFrame(t, buf, len(tc.rawBytes))
			assert.DeepEqual(t, got, tc.wantFrame, "frames do not match")
		})
	}
}

func TestIncompleteFrames(t *testing.T) {
	testCases := map[string]struct {
		rawBytes []byte
		wantErr  error
	}{
		"2-byte extended payload can't be read": {
			rawBytes: []byte{0x82, 0x7E},
			wantErr:  errors.New("error reading 2-byte extended payload length: EOF"),
		},
		"8-byte extended payload can't be read": {
			rawBytes: []byte{0x82, 0x7F},
			wantErr:  errors.New("error reading 8-byte extended payload length: EOF"),
		},
		"mask can't be read": {
			rawBytes: []byte{0x81, 0x85},
			wantErr:  errors.New("error reading mask key: EOF"),
		},
		"payload can't be read": {
			rawBytes: []byte{0x81, 0x05},
			wantErr:  errors.New("error reading 5 byte payload: EOF"),
		},
	}

	for name, tc := range testCases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			buf := bytes.NewReader(tc.rawBytes)
			_, err := websocket.ReadFrame(buf, websocket.ClientMode, 70000)
			assert.Error(t, err, tc.wantErr)
		})
	}
}

// ============================================================================
// Benchmarks
// ============================================================================
var benchMarkFrameSizes = []int{
	// test odd sizes to cover edge cases (e.g. in applyMask's optimization)
	(1 << 10) + 1, // ~1 KiB
	(1 << 20) + 1, // ~1 MiB
}

func BenchmarkReadFrame(b *testing.B) {
	for _, size := range benchMarkFrameSizes {
		frame := makeFrame(websocket.OpcodeText, true, size)
		buf := &bytes.Buffer{}
		assert.NilError(b, websocket.WriteFrame(buf, websocket.NewMaskingKey(), frame))

		// Run sub-benchmarks for each payload size
		b.Run(formatSize(size), func(b *testing.B) {
			src := bytes.NewReader(buf.Bytes())
			b.SetBytes(int64(buf.Len()))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = src.Seek(0, 0)
				frame2, err := websocket.ReadFrame(src, websocket.ServerMode, size)
				if err != nil {
					b.Fatalf("unexpected error: %v", err)
				}
				assert.Equal(b, len(frame2.Payload), len(frame.Payload), "payload length")
			}
		})
	}
}

func BenchmarkWriteFrame(b *testing.B) {
	for _, size := range benchMarkFrameSizes {
		name := "Masked/" + formatSize(size)
		b.Run(name, func(b *testing.B) {
			frame := makeFrame(websocket.OpcodeText, true, size)
			mask := websocket.NewMaskingKey()
			buf := &bytes.Buffer{}

			// Write the frame to the buffer once to get the size.
			assert.NilError(b, websocket.WriteFrame(buf, mask, frame))
			expectedSize := len(buf.Bytes())
			b.SetBytes(int64(expectedSize))
			buf.Reset()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				buf.Reset()
				assert.NilError(b, websocket.WriteFrame(buf, mask, frame))
				assert.Equal(b, len(buf.Bytes()), expectedSize, "payload length")
			}
		})
	}
}

func makeFrame(opcode websocket.Opcode, fin bool, payloadLen int) *websocket.Frame {
	payload := make([]byte, payloadLen)
	for i := range payload {
		payload[i] = 0x20 + byte(i%95) // Map to range 0x20 (space) to 0x7E (~)
	}
	return websocket.NewFrame(opcode, fin, payload)
}

func formatSize(b int) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f%ciB",
		float64(b)/float64(div), "KMGTPE"[exp])
}
