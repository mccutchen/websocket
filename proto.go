package websocket

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const requiredVersion = "13"

// Protocol-level errors.
var (
	ErrClientFrameUnmasked    = errors.New("client frame must be masked")
	ErrClosePayloadInvalid    = errors.New("close frame payload must be at least 2 bytes")
	ErrCloseStatusInvalid     = errors.New("close status code out of range")
	ErrCloseStatusReserved    = errors.New("close status code is reserved")
	ErrContinuationExpected   = errors.New("continuation frame expected")
	ErrContinuationUnexpected = errors.New("continuation frame unexpected")
	ErrControlFrameFragmented = errors.New("control frame must not be fragmented")
	ErrControlFrameTooLarge   = errors.New("control frame payload exceeds 125 bytes")
	ErrEncodingInvalid        = errors.New("payload must be valid UTF-8")
	ErrFrameTooLarge          = errors.New("frame payload too large")
	ErrMessageTooLarge        = errors.New("message paylaod too large")
	ErrOpcodeUnknown          = errors.New("opcode unknown")
	ErrRSVBitsUnsupported     = errors.New("RSV bits not supported")
)

// Opcode is a websocket OPCODE.
type Opcode uint8

// See the RFC for the set of defined opcodes:
// https://datatracker.ietf.org/doc/html/rfc6455#section-5.2
const (
	OpcodeContinuation Opcode = 0b0000_0000
	OpcodeText         Opcode = 0b0000_0001
	OpcodeBinary       Opcode = 0b0000_0010
	OpcodeClose        Opcode = 0b0000_1000
	OpcodePing         Opcode = 0b0000_1001
	OpcodePong         Opcode = 0b0000_1010
)

func (c Opcode) String() string {
	switch c {
	case OpcodeContinuation:
		return "Cont"
	case OpcodeText:
		return "Text"
	case OpcodeBinary:
		return "Binary"
	case OpcodeClose:
		return "Close"
	case OpcodePing:
		return "Ping"
	case OpcodePong:
		return "Pong"
	default:
		panic(fmt.Sprintf("unknown opcode: %#v", c))
	}
}

// StatusCode is a websocket status code.
type StatusCode uint16

// See the RFC for the set of defined status codes:
// https://datatracker.ietf.org/doc/html/rfc6455#section-7.4.1
const (
	StatusNormalClosure      StatusCode = 1000
	StatusGoingAway          StatusCode = 1001
	StatusProtocolError      StatusCode = 1002
	StatusUnsupported        StatusCode = 1003
	StatusNoStatusRcvd       StatusCode = 1005
	StatusAbnormalClose      StatusCode = 1006
	StatusUnsupportedPayload StatusCode = 1007
	StatusPolicyViolation    StatusCode = 1008
	StatusTooLarge           StatusCode = 1009
	StatusTlSHandshake       StatusCode = 1015
	StatusServerError        StatusCode = 1011
)

const (
	// first header byte
	finMask    = 0b1000_0000
	opcodeMask = 0b0000_1111

	// second header byte
	maskedMask     = 0b1000_0000
	payloadLenMask = 0b0111_1111
)

// RSVBit is a bit mask for RSV bits 1-3
type RSVBit byte

// RSV bits 1-3
const (
	RSV1 RSVBit = 0b0100_0000
	RSV2 RSVBit = 0b0010_0000
	RSV3 RSVBit = 0b0001_0000
)

// NewFrame creates a new websocket frame with the given opcode, fin bit, and
// payload.
func NewFrame(opcode Opcode, fin bool, payload []byte, rsv ...RSVBit) *Frame {
	f := &Frame{
		payload: payload,
	}
	// Encode FIN, RSV1-3, and OPCODE.
	//
	// The second header byte encodes mask bit and payload size, but in-memory
	// frames are unmasked and the payload size is directly accessible.
	//
	// These bits will be encoded as necessary when writing a frame.
	if fin {
		f.header |= finMask
	}
	for _, r := range rsv {
		f.header |= byte(r)
	}
	f.header |= uint8(opcode) & opcodeMask
	return f
}

// NewCloseFrame creates a close frame with an optional error message.
func NewCloseFrame(code StatusCode, reason string) *Frame {
	var payload []byte
	if code > 0 {
		payload = make([]byte, 0, 2+len(reason))
		payload = binary.BigEndian.AppendUint16(payload, uint16(code))
		payload = append(payload, []byte(reason)...)
	}
	return NewFrame(OpcodeClose, true, payload)
}

// Frame is a websocket protocol frame.
type Frame struct {
	header  byte
	payload []byte
}

// Fin returns a bool indicating whether the frame's FIN bit is set.
func (f *Frame) Fin() bool {
	return f.header&finMask != 0
}

// Opcode returns the the frame's OPCODE.
func (f *Frame) Opcode() Opcode {
	return Opcode(f.header & opcodeMask)
}

// RSV1 returns true if the frame's RSV1 bit is set
func (f *Frame) RSV1() bool { return f.header&byte(RSV1) != 0 }

// RSV2 returns true if the frame's RSV2 bit is set
func (f *Frame) RSV2() bool { return f.header&byte(RSV2) != 0 }

// RSV3 returns true if the frame's RSV3 bit is set
func (f *Frame) RSV3() bool { return f.header&byte(RSV3) != 0 }

// Payload returns the frame's payload.
func (f *Frame) Payload() []byte {
	return f.payload
}

func (f Frame) String() string {
	return fmt.Sprintf("Frame{Fin: %v, Opcode: %v, Payload: %s}", f.Fin(), f.Opcode(), formatPayload(f.Payload()))
}

// Message is an application-level message from the client, which may be
// constructed from one or more individual protocol frames.
type Message struct {
	Binary  bool
	Payload []byte
}

func (m Message) String() string {
	return fmt.Sprintf("Message{Binary: %v, Payload: %s}", m.Binary, formatPayload(m.Payload))
}

// formatPayload is the formatter used by Frame and Message String() methods,
// which may be overridden in tests for more detailed output.
var formatPayload = func(p []byte) string {
	if len(p) <= 64 {
		return fmt.Sprintf("%q", p)
	}
	return fmt.Sprintf("[%d bytes]", len(p))
}

// ReadFrame reads a websocket frame from the wire.
func ReadFrame(buf io.Reader, mode Mode, maxPayloadLen int) (*Frame, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(buf, header); err != nil {
		return nil, fmt.Errorf("error reading frame header: %w", err)
	}

	frame := &Frame{
		header: header[0],
	}

	// figure out how to parse payload
	var (
		masked     = header[1]&maskedMask != 0
		payloadLen = uint64(header[1] & payloadLenMask)
	)

	// If the data is being sent by the client, the frame(s) MUST be masked
	// https://datatracker.ietf.org/doc/html/rfc6455#section-6.1
	if mode == ServerMode && !masked {
		return nil, ErrClientFrameUnmasked
	}

	// Payload lengths 0 to 125 encoded directly in last 7 bits of payload
	// field, but we may need to read an "extended" payload length.
	switch payloadLen {
	case 126:
		// Payload lengths 126 to 65535 are represented in the next 2 bytes
		// (16-bit unsigned integer)
		var l uint16
		if err := binary.Read(buf, binary.BigEndian, &l); err != nil {
			return nil, fmt.Errorf("error reading 2-byte extended payload length: %w", err)
		}
		payloadLen = uint64(l)
	case 127:
		// Payload lengths >= 65536 are represented in the next 8 bytes
		// (64-bit unsigned integer)
		if err := binary.Read(buf, binary.BigEndian, &payloadLen); err != nil {
			return nil, fmt.Errorf("error reading 8-byte extended payload length: %w", err)
		}
	}

	if payloadLen > uint64(maxPayloadLen) {
		return nil, ErrFrameTooLarge
	}

	// read mask key (if present)
	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(buf, mask); err != nil {
			return nil, fmt.Errorf("error reading mask key: %w", err)
		}
	}

	// read & optionally unmask payload
	frame.payload = make([]byte, payloadLen)
	if _, err := io.ReadFull(buf, frame.Payload()); err != nil {
		return nil, fmt.Errorf("error reading %d byte payload: %w", payloadLen, err)
	}
	if masked {
		for i, b := range frame.Payload() {
			frame.Payload()[i] = b ^ mask[i%4]
		}
	}
	return frame, nil
}

// WriteFrame writes a masked websocket frame to the wire.
func WriteFrame(dst io.Writer, mask MaskingKey, frame *Frame) error {
	_, err := dst.Write(MarshalFrame(frame, mask))
	if err != nil {
		return fmt.Errorf("error writing frame: %w", err)
	}
	return nil
}

// MarshalFrame marshals a frame into bytes for transmission.
func MarshalFrame(frame *Frame, mask MaskingKey) []byte {
	// worst case payload size is 13 header bytes + payload size, where 13 is
	// (1 byte header) + (1-8 byte length) + (0-4 byte mask key)
	buf := make([]byte, 0, marshaledSize(frame, mask))
	masked := mask != Unmasked

	buf = append(buf, frame.header)

	// Masked bit, payload length
	var b1 byte
	if masked {
		b1 |= 0b1000_0000
	}

	payloadLen := int64(len(frame.Payload()))
	switch {
	case payloadLen <= 125:
		buf = append(buf, b1|byte(payloadLen))
	case payloadLen <= 65535:
		buf = append(buf, b1|126)
		buf = binary.BigEndian.AppendUint16(buf, uint16(payloadLen))
	default:
		buf = append(buf, b1|127)
		buf = binary.BigEndian.AppendUint64(buf, uint64(payloadLen))
	}

	// Optional masking key and actual payload
	if masked {
		buf = append(buf, mask[:]...)
		for i, b := range frame.Payload() {
			buf = append(buf, b^mask[i%4])
		}
	} else {
		buf = append(buf, frame.Payload()...)
	}
	return buf
}

// marshaledSize returns the number of bytes required to marshal a frame.
func marshaledSize(f *Frame, mask MaskingKey) int {
	payloadLen := len(f.Payload())
	size := 2 + payloadLen
	switch {
	case payloadLen >= 64<<10:
		size += 8
	case payloadLen > 125:
		size += 2
	}
	if mask != Unmasked {
		size += 4
	}
	return size
}

// FrameMessage splits a message into N frames with payloads of at most
// frameSize bytes.
func FrameMessage(msg *Message, frameSize int) []*Frame {
	var result []*Frame

	fin := false
	opcode := OpcodeText
	if msg.Binary {
		opcode = OpcodeBinary
	}

	offset := 0
	dataLen := len(msg.Payload)
	for {
		if offset > 0 {
			opcode = OpcodeContinuation
		}
		end := offset + frameSize
		if end >= dataLen {
			fin = true
			end = dataLen
		}
		result = append(result, NewFrame(opcode, fin, msg.Payload[offset:end]))
		if fin {
			break
		}
		offset += frameSize
	}
	return result
}

var reservedStatusCodes = map[uint16]bool{
	// Explicitly reserved by RFC section 7.4.1 Defined Status Codes:
	// https://datatracker.ietf.org/doc/html/rfc6455#section-7.4.1
	1004: true,
	1005: true,
	1006: true,
	1015: true,
	// Apparently reserved, according to the autobahn testsuite's fuzzingclient
	// tests, though it's not clear to me why, based on the RFC.
	//
	// See: https://github.com/crossbario/autobahn-testsuite
	1016: true,
	1100: true,
	2000: true,
	2999: true,
}

func validateFrame(frame *Frame) error {
	// We do not support any extensions, per the spec all RSV bits must be 0:
	// https://datatracker.ietf.org/doc/html/rfc6455#section-5.2
	if frame.header&uint8(RSV1|RSV2|RSV3) != 0 {
		return ErrRSVBitsUnsupported
	}

	payloadLen := len(frame.Payload())
	switch frame.Opcode() {
	case OpcodeClose, OpcodePing, OpcodePong:
		// All control frames MUST have a payload length of 125 bytes or less
		// and MUST NOT be fragmented.
		// https://datatracker.ietf.org/doc/html/rfc6455#section-5.5
		if payloadLen > 125 {
			return ErrControlFrameTooLarge
		}
		if !frame.Fin() {
			return ErrControlFrameFragmented
		}
	}

	if frame.Opcode() == OpcodeClose {
		if payloadLen == 0 {
			return nil
		}
		if payloadLen == 1 {
			return ErrClosePayloadInvalid
		}

		code := binary.BigEndian.Uint16(frame.Payload()[:2])
		if code < 1000 || code >= 5000 {
			return ErrCloseStatusInvalid
		}
		if reservedStatusCodes[code] {
			return ErrCloseStatusReserved
		}
		if payloadLen > 2 && !utf8.Valid(frame.Payload()[2:]) {
			return ErrEncodingInvalid
		}
	}

	return nil
}

func acceptKey(clientKey string) string {
	// Magic value comes from RFC 6455 section 1.3: Opening Handshake
	// https://www.rfc-editor.org/rfc/rfc6455#section-1.3
	h := sha1.New()
	_, _ = io.WriteString(h, clientKey+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// ClientKey identifies the client in the websocket handshake.
type ClientKey string

// NewClientKey returns a randomly-generated ClientKey for client handshake.
// Panics on insufficient randomness.
func NewClientKey() ClientKey {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		panic(fmt.Sprintf("NewClientKey: failed to read random bytes: %s", err))
	}
	return ClientKey(base64.StdEncoding.EncodeToString(b))
}

// MaskingKey masks client frames.
type MaskingKey [4]byte

// Unmasked is the zero masking key, indicating that a marshaled frame should
// not be masked.
var Unmasked = MaskingKey([4]byte{})

// NewMaskingKey returns a randomly generated making key for client frames.
// Panics on insufficient randomness.
func NewMaskingKey() MaskingKey {
	var key [4]byte
	_, err := rand.Read(key[:]) // Fill the key with 4 random bytes
	if err != nil {
		panic(fmt.Sprintf("NewMaskingKey: failed to read random bytes: %s", err))
	}
	return key
}
