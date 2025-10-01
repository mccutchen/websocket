package websocket

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"unicode/utf8"
)

const requiredVersion = "13"

// Opcode is a websocket OPCODE.
type Opcode uint8

// See the RFC for the set of defined opcodes:
// https://datatracker.ietf.org/doc/html/rfc6455#section-5.2
const (
	OpcodeContinuation Opcode = 0b0000_0000 // 0x0
	OpcodeText         Opcode = 0b0000_0001 // 0x1
	OpcodeBinary       Opcode = 0b0000_0010 // 0x2
	OpcodeClose        Opcode = 0b0000_1000 // 0x8
	OpcodePing         Opcode = 0b0000_1001 // 0x9
	OpcodePong         Opcode = 0b0000_1010 // 0xA
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

// StatusCode is a websocket close status code.
type StatusCode uint16

// Close status codes, as defined in RFC 6455 and the IANA registry.
//
// https://datatracker.ietf.org/doc/html/rfc6455#section-7.4.1
// https://www.iana.org/assignments/websocket/websocket.xhtml#close-code-number
const (
	StatusNormalClosure       StatusCode = 1000
	StatusGoingAway           StatusCode = 1001
	StatusProtocolError       StatusCode = 1002
	StatusUnsupportedData     StatusCode = 1003
	StatusNoStatusRcvd        StatusCode = 1005
	StatusAbnormalClose       StatusCode = 1006
	StatusInvalidFramePayload StatusCode = 1007
	StatusPolicyViolation     StatusCode = 1008
	StatusTooLarge            StatusCode = 1009
	StatusMandatoryExtension  StatusCode = 1010
	StatusInternalError       StatusCode = 1011
	StatusServiceRestart      StatusCode = 1012
	StatusTryAgainLater       StatusCode = 1013
	StatusGatewayError        StatusCode = 1014
	StatusTlSHandshake        StatusCode = 1015
)

// Protocol-level errors.
var (
	ErrClientFrameUnmasked    = newError(StatusProtocolError, "client frame must be masked")
	ErrClosePayloadInvalid    = newError(StatusProtocolError, "close frame payload must be at least 2 bytes")
	ErrCloseStatusInvalid     = newError(StatusProtocolError, "close status code out of range")
	ErrCloseStatusReserved    = newError(StatusProtocolError, "close status code is reserved")
	ErrContinuationExpected   = newError(StatusProtocolError, "continuation frame expected")
	ErrContinuationUnexpected = newError(StatusProtocolError, "continuation frame unexpected")
	ErrControlFrameFragmented = newError(StatusProtocolError, "control frame must not be fragmented")
	ErrControlFrameTooLarge   = newError(StatusProtocolError, "control frame payload exceeds 125 bytes")
	ErrInvalidFramePayload    = newError(StatusInvalidFramePayload, "payload must be valid UTF-8")
	ErrFrameTooLarge          = newError(StatusTooLarge, "frame payload too large")
	ErrMessageTooLarge        = newError(StatusTooLarge, "message paylaod too large")
	ErrOpcodeUnknown          = newError(StatusProtocolError, "opcode unknown")
	ErrRSVBitsUnsupported     = newError(StatusProtocolError, "RSV bits not supported")
)

// Error is a websocket error with an associated [StatusCode].
type Error struct {
	code StatusCode
	err  error
}

func (e *Error) Error() string {
	return e.err.Error()
}

func (e *Error) Unwrap() error {
	return e.err
}

func newError(code StatusCode, msg string, args ...any) *Error {
	return &Error{code, fmt.Errorf(msg, args...)}
}

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

// NewFrame creates a new [Frame] with the given [Opcode], fin bit, and
// payload.
func NewFrame(opcode Opcode, fin bool, payload []byte, rsv ...RSVBit) *Frame {
	f := &Frame{
		Payload: payload,
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

// NewCloseFrame creates a close [Frame]. If the given [StatusCode] is 0, the
// close frame will not have a payload.
func NewCloseFrame(code StatusCode, reason string) *Frame {
	var payload []byte
	if code > 0 {
		payload = make([]byte, 0, 2+len(reason))
		payload = binary.BigEndian.AppendUint16(payload, uint16(code))
		payload = append(payload, []byte(reason)...)
	}
	return NewFrame(OpcodeClose, true, payload)
}

// Frame is a websocket frame.
type Frame struct {
	header  byte
	Payload []byte
}

// Fin returns a bool indicating whether the frame's FIN bit is set.
func (f *Frame) Fin() bool {
	return f.header&finMask != 0
}

// Opcode returns the the frame's [Opcode].
func (f *Frame) Opcode() Opcode {
	return Opcode(f.header & opcodeMask)
}

// RSV1 returns true if the frame's RSV1 bit is set
func (f *Frame) RSV1() bool { return f.header&byte(RSV1) != 0 }

// RSV2 returns true if the frame's RSV2 bit is set
func (f *Frame) RSV2() bool { return f.header&byte(RSV2) != 0 }

// RSV3 returns true if the frame's RSV3 bit is set
func (f *Frame) RSV3() bool { return f.header&byte(RSV3) != 0 }

func (f Frame) String() string {
	return fmt.Sprintf("Frame{Fin: %v, Opcode: %v, Payload: %s}", f.Fin(), f.Opcode(), formatPayload(f.Payload))
}

// Message is an application-level message from the client, which may be
// constructed from one or more individual frames.
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

// ReadFrame reads a [Frame].
func ReadFrame(src io.Reader, mode Mode, maxPayloadLen int) (*Frame, error) {
	// Frame header is 2 bytes:
	// https://datatracker.ietf.org/doc/html/rfc6455#section-5.2
	//
	// First byte contains FIN, RSV1-3, OPCODE bits, but we store that byte
	// as-is and only extract the bits when needed.
	//
	// Second byte contains MASK bit and payload length, which must be
	// extracted here to read the rest of the payload.
	var header [2]byte
	if _, err := io.ReadFull(src, header[:]); err != nil {
		return nil, newError(StatusAbnormalClose, "error reading frame header: %w", err)
	}
	var (
		masked     = (header[1] & maskedMask) != 0
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
		var extendedLenBuf [2]byte
		if _, err := io.ReadFull(src, extendedLenBuf[:]); err != nil {
			return nil, newError(StatusAbnormalClose, "error reading 2-byte extended payload length: %w", err)
		}
		payloadLen = uint64(binary.BigEndian.Uint16(extendedLenBuf[:]))
	case 127:
		// Payload lengths >= 65536 are represented in the next 8 bytes
		// (64-bit unsigned integer)
		var extendedLenBuf [8]byte
		if _, err := io.ReadFull(src, extendedLenBuf[:]); err != nil {
			return nil, newError(StatusAbnormalClose, "error reading 8-byte extended payload length: %w", err)
		}
		payloadLen = binary.BigEndian.Uint64(extendedLenBuf[:])
	}

	if payloadLen > uint64(maxPayloadLen) {
		return nil, ErrFrameTooLarge
	}

	// read mask key (if present)
	var mask MaskingKey
	if masked {
		if _, err := io.ReadFull(src, mask[:]); err != nil {
			return nil, newError(StatusAbnormalClose, "error reading mask key: %w", err)
		}
	}

	// read & optionally unmask payload
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(src, payload); err != nil {
		return nil, newError(StatusAbnormalClose, "error reading %d byte payload: %w", payloadLen, err)
	}
	if masked {
		applyMask(payload, mask)
	}
	return &Frame{
		header:  header[0],
		Payload: payload,
	}, nil
}

// WriteFrame writes a [Frame] to dst with the given masking key. To write an
// unmasked frame, use the special [Unmasked] key.
func WriteFrame(dst io.Writer, mask MaskingKey, frame *Frame) error {
	if _, err := dst.Write(marshalFrame(frame, mask)); err != nil {
		return newError(StatusAbnormalClose, "error writing frame: %w", err)
	}
	return nil
}

// marshalFrame marshals a [Frame] into bytes for transmission.
func marshalFrame(frame *Frame, mask MaskingKey) []byte {
	var (
		payloadLen    = len(frame.Payload)
		payloadOffset = 2 // at least 2 bytes will be taken by header
		buf           = make([]byte, marshaledSize(payloadLen, mask))
		masked        = mask != Unmasked
	)

	// First header byte can be written directly
	buf[0] = frame.header

	// Second header byte encodes masked bit and payload size, additional
	// header bytes may be written for extended payload sizes.
	if masked {
		buf[1] |= 0b1000_0000
	}
	switch {
	case payloadLen <= 125:
		buf[1] |= byte(payloadLen)
	case payloadLen <= 65535:
		buf[1] |= 126
		binary.BigEndian.PutUint16(buf[payloadOffset:], uint16(payloadLen))
		payloadOffset += 2
	default:
		buf[1] |= 127
		binary.BigEndian.PutUint64(buf[payloadOffset:], uint64(payloadLen))
		payloadOffset += 8
	}

	// Optional masking key and actual payload
	if masked {
		copy(buf[payloadOffset:], mask[:])
		payloadOffset += 4
		copy(buf[payloadOffset:], frame.Payload)
		applyMask(buf[payloadOffset:payloadOffset+payloadLen], mask)
	} else {
		copy(buf[payloadOffset:], frame.Payload)
	}
	return buf
}

// marshaledSize returns the number of bytes required to marshal a
// [Frame] with the given payload length and [MaskingKey].
func marshaledSize(payloadLen int, mask MaskingKey) int {
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

// closeAckFrame returns an appropriate close frame to use when completing a
// closing handshake initiated by the other end. If the incoming close frame
// has a valid payload, it is returned as-is.
func closeAckFrame(closeFrame *Frame) *Frame {
	if len(closeFrame.Payload) >= 2 {
		return closeFrame
	}
	return NewCloseFrame(StatusNoStatusRcvd, "")

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

// validateFrame ensures that the frame is valid.
//
// TODO: validate in ReadFrame instead of ReadMessage.
func validateFrame(frame *Frame) error {
	// We do not support any extensions, per the spec all RSV bits must be 0:
	// https://datatracker.ietf.org/doc/html/rfc6455#section-5.2
	if frame.header&uint8(RSV1|RSV2|RSV3) != 0 {
		return ErrRSVBitsUnsupported
	}

	var (
		opcode     = frame.Opcode()
		payloadLen = len(frame.Payload)
	)
	switch opcode {
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

	if opcode == OpcodeClose {
		if payloadLen == 0 {
			return nil
		}
		// if a close frame has a payload, the first two bytes MUST encode a
		// closing status code.
		// https://datatracker.ietf.org/doc/html/rfc6455#section-5.5.1
		if payloadLen == 1 {
			return ErrClosePayloadInvalid
		}

		code := binary.BigEndian.Uint16(frame.Payload[:2])
		if code < 1000 || code >= 5000 {
			return ErrCloseStatusInvalid
		}
		if reservedStatusCodes[code] {
			return ErrCloseStatusReserved
		}
		if payloadLen > 2 && !utf8.Valid(frame.Payload[2:]) {
			return ErrInvalidFramePayload
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

// NewClientKey returns a randomly-generated [ClientKey] for client handshake.
// Panics on insufficient randomness.
func NewClientKey() ClientKey {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		panic(fmt.Sprintf("NewClientKey: failed to read random bytes: %s", err))
	}
	return ClientKey(base64.StdEncoding.EncodeToString(b))
}

// MaskingKey masks a client [Frame].
type MaskingKey [4]byte

// Unmasked is the zero [MaskingKey], indicating that a marshaled frame should
// not be masked.
var Unmasked = MaskingKey([4]byte{})

// NewMaskingKey returns a randomly generated [MaskingKey] for client frames.
// Panics on insufficient randomness.
func NewMaskingKey() MaskingKey {
	var key [4]byte
	_, err := rand.Read(key[:]) // Fill the key with 4 random bytes
	if err != nil {
		panic(fmt.Sprintf("NewMaskingKey: failed to read random bytes: %s", err))
	}
	return key
}

// applyMask applies a [MaskingKey] to the payload in-place.
func applyMask(payload []byte, mask MaskingKey) {
	n := len(payload)
	if n == 0 {
		return
	}

	// duplicate 4 byte masking key to make uint64 mask that can be applied to
	// the payload 8 bytes at a time in a single XOR.
	mask64 := uint64(mask[0]) | uint64(mask[1])<<8 | uint64(mask[2])<<16 | uint64(mask[3])<<24
	mask64 |= mask64 << 32

	// apply mask in-place 8 bytes at a time
	chunks := n / 8
	for i := range chunks {
		offset := i * 8
		data := binary.LittleEndian.Uint64(payload[offset : offset+8])
		data ^= mask64
		binary.LittleEndian.PutUint64(payload[offset:offset+8], data)
	}

	remainder := payload[chunks*8:]
	for i := range remainder {
		remainder[i] ^= mask[i&3] // i&3 == i%4, but faster
	}
}
