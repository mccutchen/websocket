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
	clientFrame := websocket.NewFrame(websocket.OpcodeText, true, []byte("hello"))
	buf := &bytes.Buffer{}
	assert.NilError(t, websocket.WriteFrame(buf, websocket.NewMaskingKey(), clientFrame))

	// read "server" frame from buffer.
	serverFrame, err := websocket.ReadFrame(buf, websocket.ServerMode, len(clientFrame.Payload()))
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
	serverFrame, err := websocket.ReadFrame(buf, websocket.ServerMode, len(clientFrame.Payload())-1)
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
