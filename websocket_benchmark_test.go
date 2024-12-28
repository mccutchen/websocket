package websocket_test

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/mccutchen/websocket"
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
		websocket.WriteFrameMasked(buf, frame, mask)

		// Run sub-benchmarks for each payload size
		b.Run(strconv.Itoa(size)+"b", func(b *testing.B) {
			src := bytes.NewReader(buf.Bytes())
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				src.Seek(0, 0)
				_, err := websocket.ReadFrame(src)
				if err != nil {
					b.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

/*

TODO: benchmark reading entire message after refactoring

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

	echoHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, websocket.Options{
			MaxFragmentSize: 1024 * 1024,
			MaxMessageSize:  2 * 1024 * 1024,
			// Hooks:           newTestHooks(b),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ws.Serve(r.Context(), websocket.EchoHandler)
	})

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

			name := fmt.Sprintf("MessageSize=%db/FrameSize=%db/FrameCount=%d", msgSize, frameSize, frameCount)
			b.Run(name, func(b *testing.B) {
				_, conn := setupRawConn(b, echoHandler)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					n, err := conn.Write(buf.Bytes())
					assert.NilError(b, err)
					assert.Equal(b, n, len(buf.Bytes()), "incorrect number of bytes written")
					resp, err := io.ReadAll(conn)
					assert.NilError(b, err)
					assert.Equal(b, len(resp) >= msgSize, true, "expected to read full message back")
				}
			})
		}
	}
}

*/
