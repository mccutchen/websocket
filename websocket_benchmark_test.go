package websocket_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/mccutchen/websocket"
	"github.com/mccutchen/websocket/internal/testing/assert"
)

func makeFrame(opcode websocket.Opcode, fin bool, payloadLen int) *websocket.Frame {
	payload := make([]byte, payloadLen)
	for i := range payload {
		payload[i] = 0x20 + byte(i%95) // Map to range 0x20 (space) to 0x7E (~)
	}

	return &websocket.Frame{
		Opcode:  opcode,
		Fin:     fin,
		Payload: payload,
	}
}

func BenchmarkReadFrame(b *testing.B) {
	frameSizes := []int{
		64,
		256,
		1024,
		64 * 1024,
		1024 * 1024,
	}

	for _, size := range frameSizes {
		frame := makeFrame(websocket.OpcodeText, true, size)
		mask := [4]byte{1, 2, 3, 4}

		buf := &bytes.Buffer{}
		assert.NilError(b, websocket.WriteFrameMasked(buf, frame, mask))

		// Run sub-benchmarks for each payload size
		b.Run(strconv.Itoa(size)+"b", func(b *testing.B) {
			src := bytes.NewReader(buf.Bytes())
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = src.Seek(0, 0)
				_, err := websocket.ReadFrame(src)
				if err != nil {
					b.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func BenchmarkReadMessage(b *testing.B) {
	frameSizes := []int{
		64,
		// 256,
		// 1024,
		// 64 * 1024,
		// 1024 * 1024,
	}

	messageSizes := []int{
		512,
		// 1024,
		// 256 * 1024,
		// 1024 * 1024,
		// 2 * 1024 * 1024,
	}

	for _, msgSize := range messageSizes {
		for _, frameSize := range frameSizes {
			if msgSize%frameSize != 0 {
				continue
			}
			buf := &bytes.Buffer{}
			frameCount := msgSize / frameSize
			for i := 0; i < frameCount; i++ {
				opcode := websocket.OpcodeText
				if i > 0 {
					opcode = websocket.OpcodeContinuation
				}
				fin := i == frameCount-1
				b.Logf("frame=%d frameCount=%d fin=%v", i, frameCount, fin)
				frame := makeFrame(opcode, fin, frameSize)
				websocket.WriteFrameMasked(buf, frame, makeMaskingKey())
			}
			payload := buf.Bytes()

			name := fmt.Sprintf("MessageSize=%db/FrameSize=%db/FrameCount=%d", msgSize, frameSize, frameCount)
			b.Run(name, func(b *testing.B) {
				conn := &dummyConn{
					in:  bytes.NewReader(payload),
					out: &bytes.Buffer{},
				}
				ws := websocket.NewConn(conn, websocket.ClientKey(makeClientKey()), websocket.Options{
					MaxFragmentSize: 1024 * 1024,
					MaxMessageSize:  2 * 1024 * 1024,
				})
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					msg, err := ws.Read(context.Background())
					assert.NilError(b, err)
					assert.Equal(b, len(msg.Payload), msgSize, "expected message payload")
				}
			})
		}
	}
}

type dummyConn struct {
	in     io.Reader
	out    io.Writer
	closed atomic.Bool
}

func (c *dummyConn) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, errors.New("reader closed")
	}
	return c.in.Read(p)
}

func (c *dummyConn) Write(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, errors.New("writer closed")
	}
	return c.out.Write(p)
}

func (c *dummyConn) Close() error {
	c.closed.Swap(true)
	return nil
}
