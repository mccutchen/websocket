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
	buf := &bytes.Buffer{}
	assert.NilError(t, websocket.WriteFrameMasked(buf, clientFrame, websocket.NewMaskingKey()))

	// read "server" frame from buffer.
	serverFrame, err := websocket.ReadFrame(buf, len(clientFrame.Payload))
	assert.NilError(t, err)

	// ensure client and server frame match
	assert.Equal(t, serverFrame.Fin, clientFrame.Fin, "expected matching FIN bits")
	assert.Equal(t, serverFrame.Opcode, clientFrame.Opcode, "expected matching opcodes")
	assert.Equal(t, string(serverFrame.Payload), string(clientFrame.Payload), "expected matching payloads")
}

func TestMaxFrameSize(t *testing.T) {
	// Basic test to ensure that we can read back the same frame that we
	// write.
	t.Parallel()

	// write masked "client" frame to buffer
	clientFrame := &websocket.Frame{
		Opcode:  websocket.OpcodeText,
		Fin:     true,
		Payload: []byte("hello"),
	}
	buf := &bytes.Buffer{}
	assert.NilError(t, websocket.WriteFrameMasked(buf, clientFrame, websocket.NewMaskingKey()))

	// read "server" frame from buffer.
	serverFrame, err := websocket.ReadFrame(buf, len(clientFrame.Payload)-1)
	assert.Error(t, err, websocket.ErrFrameTooLarge)
	assert.Equal(t, serverFrame, nil, "expected nil frame on error")
}
