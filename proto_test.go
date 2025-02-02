package websocket_test

import (
	"bytes"
	"errors"
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
	assert.NilError(t, websocket.WriteFrame(buf, websocket.NewMaskingKey(), clientFrame))

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
	assert.NilError(t, websocket.WriteFrame(buf, websocket.NewMaskingKey(), clientFrame))

	// read "server" frame from buffer.
	serverFrame, err := websocket.ReadFrame(buf, len(clientFrame.Payload)-1)
	assert.Error(t, err, websocket.ErrFrameTooLarge)
	assert.Equal(t, serverFrame, nil, "expected nil frame on error")
}

func TestRSV(t *testing.T) {
	// We don't currently support any extensions, so RSV bits are not allowed.
	// But we still need to be able properly parse and marshal them.
	const (
		finBit       = 0b1000_0000
		txtOpcodeBit = 0b0000_0001
		rsv1bit      = 0b0100_0000
		rsv2bit      = 0b0010_0000
		rsv3bit      = 0b0001_0000
	)
	testCases := map[string]struct {
		rawBytes []byte
		wantRSV1 bool
		wantRSV2 bool
		wantRSV3 bool
	}{
		"no RSV bits set": {
			rawBytes: []byte{0x81, 0x00},
		},
		"RSV1 set": {
			rawBytes: []byte{finBit | rsv1bit | txtOpcodeBit, 0x00},
			wantRSV1: true,
		},
		"RSV2 set": {
			rawBytes: []byte{finBit | rsv2bit | txtOpcodeBit, 0x00},
			wantRSV2: true,
		},
		"RSV3 set": {
			rawBytes: []byte{finBit | rsv3bit | txtOpcodeBit, 0x00},
			wantRSV3: true,
		},
		"all RSV bits set": {
			rawBytes: []byte{finBit | rsv1bit | rsv2bit | rsv3bit | txtOpcodeBit, 0x00},
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
			rawBytes: []byte{0x81, 0x05, 0x48, 0x65, 0x6c, 0x6c, 0x6f},
			wantFrame: &websocket.Frame{
				Fin:     true,
				Opcode:  websocket.OpcodeText,
				Payload: []byte("Hello"),
				Masked:  false,
			},
		},
		"single-frame masked text": {
			rawBytes: []byte{0x81, 0x85, 0x37, 0xfa, 0x21, 0x3d, 0x7f, 0x9f, 0x4d, 0x51, 0x58},
			wantFrame: &websocket.Frame{
				Fin:     true,
				Opcode:  websocket.OpcodeText,
				Payload: []byte("Hello"),
				Masked:  true,
			},
		},
		"fragmented unmasked text part 1": {
			rawBytes: []byte{0x01, 0x03, 0x48, 0x65, 0x6c},
			wantFrame: &websocket.Frame{
				Fin:     false,
				Opcode:  websocket.OpcodeText,
				Payload: []byte("Hel"),
			},
		},
		"fragmented unmasked text part 2": {
			rawBytes: []byte{0x80, 0x02, 0x6c, 0x6f},
			wantFrame: &websocket.Frame{
				Fin:     true,
				Opcode:  websocket.OpcodeContinuation,
				Payload: []byte("lo"),
			},
		},
		"unmasked ping": {
			rawBytes: []byte{
				0x89, 0x05, 0x48, 0x65, 0x6c, 0x6c, 0x6f,
			},
			wantFrame: &websocket.Frame{
				Fin:     true,
				Opcode:  websocket.OpcodePing,
				Payload: []byte("Hello"),
			},
		},
		"masked ping response": {
			rawBytes: []byte{0x8a, 0x85, 0x37, 0xfa, 0x21, 0x3d, 0x7f, 0x9f, 0x4d, 0x51, 0x58},
			wantFrame: &websocket.Frame{
				Fin:     true,
				Opcode:  websocket.OpcodePong,
				Payload: []byte("Hello"),
				Masked:  true,
			},
		},
		"256 bytes binary message": {
			rawBytes: append(
				[]byte{0x82, 0x7E, 0x01, 0x00},
				make([]byte, 256)...,
			),
			wantFrame: &websocket.Frame{
				Fin:     true,
				Opcode:  websocket.OpcodeBinary,
				Payload: make([]byte, 256),
			},
		},
		"64KiB binary message": {
			rawBytes: append(
				[]byte{0x82, 0x7F, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00},
				make([]byte, 65536)...,
			),
			wantFrame: &websocket.Frame{
				Fin:     true,
				Opcode:  websocket.OpcodeBinary,
				Payload: make([]byte, 65536),
			},
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
			_, err := websocket.ReadFrame(buf, 70000)
			assert.Error(t, err, tc.wantErr)
		})
	}
}
