package websocket_test

import (
	"bytes"
	"testing"

	"github.com/mccutchen/websocket"
	"github.com/mccutchen/websocket/internal/testing/assert"
)

func TestFrameRoundTrip(t *testing.T) {
	// Basic test to ensure that we can read back the same frame that we
	// write.
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

	// read "server" frame from buffer.
	serverFrame, err := websocket.ReadFrame(buf, len(clientFrame.Payload))
	assert.NilError(t, err)

	// ensure client and server frame match
	assert.Equal(t, serverFrame.Fin, clientFrame.Fin, "expected matching FIN bits")
	assert.Equal(t, serverFrame.Opcode, clientFrame.Opcode, "expected matching opcodes")
	assert.Equal(t, string(serverFrame.Payload), string(clientFrame.Payload), "expected matching payloads")
}
