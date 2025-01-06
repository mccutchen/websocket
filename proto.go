package websocket

import (
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
	ErrContinuationExpected = errors.New("expected continuation frame")
	ErrInvalidContinuation  = errors.New("unexpected continuation frame")
	ErrInvalidUT8           = errors.New("invalid UTF-8")
	ErrUnmaskedClientFrame  = errors.New("received unmasked client frame")
	ErrUnsupportedRSVBits   = errors.New("frame has unsupported RSV bits set")
	ErrFrameTooLarge        = errors.New("frame payload too large")
)

var zeroMask [4]byte

// ClientKey is a websocket client key.
type ClientKey string

// Opcode is a websocket OPCODE.
type Opcode uint8

// See the RFC for the set of defined opcodes:
// https://datatracker.ietf.org/doc/html/rfc6455#section-5.2
const (
	OpcodeContinuation Opcode = 0x0
	OpcodeText         Opcode = 0x1
	OpcodeBinary       Opcode = 0x2
	OpcodeClose        Opcode = 0x8
	OpcodePing         Opcode = 0x9
	OpcodePong         Opcode = 0xA
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

// Frame is a websocket protocol frame.
type Frame struct {
	Fin     bool
	RSV1    bool
	RSV3    bool
	RSV2    bool
	Opcode  Opcode
	Payload []byte
	Masked  bool
}

func (f Frame) String() string {
	return fmt.Sprintf("Frame{Fin: %v, Opcode: %v, Payload: %q}", f.Fin, f.Opcode, truncatedPayload(f.Payload, 512))
}

// Message is an application-level message from the client, which may be
// constructed from one or more individual protocol frames.
type Message struct {
	Binary  bool
	Payload []byte
}

func (m Message) String() string {
	if m.Binary {
		return fmt.Sprintf("Message{Binary: %v, Payload: %v}", m.Binary, truncatedPayload(m.Payload, 512))
	}
	return fmt.Sprintf("Message{Binary: %v, Payload: %q}", m.Binary, truncatedPayload(m.Payload, 512))
}

func truncatedPayload(p []byte, limit int) string {
	if len(p) < limit {
		return string(p)
	}
	suffix := fmt.Sprintf(" ... [%d bytes truncated]", len(p)-limit)
	return string(p[:limit]) + suffix
}

// ReadFrame reads a websocket frame from the wire.
func ReadFrame(buf io.Reader, maxPayloadLen int) (*Frame, error) {
	bb := make([]byte, 2)
	if _, err := io.ReadFull(buf, bb); err != nil {
		return nil, fmt.Errorf("error reading frame header: %w", err)
	}

	// parse first header byte
	var (
		b0     = bb[0]
		fin    = b0&0b10000000 != 0
		rsv1   = b0&0b01000000 != 0
		rsv2   = b0&0b00100000 != 0
		rsv3   = b0&0b00010000 != 0
		opcode = Opcode(b0 & 0b00001111)
	)

	// parse second header byte
	var (
		b1         = bb[1]
		masked     = b1&0b10000000 != 0
		payloadLen = uint64(b1 & 0b01111111)
	)

	// figure out if we need to read an "extended" payload length
	switch payloadLen {
	case 126:
		// Payload length is represented in the next 2 bytes (16-bit unsigned integer)
		var l uint16
		if err := binary.Read(buf, binary.BigEndian, &l); err != nil {
			return nil, fmt.Errorf("error reading 2-byte extended payload length: %w", err)
		}
		payloadLen = uint64(l)
	case 127:
		// Payload length is represented in the next 8 bytes (64-bit unsigned integer)
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
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(buf, payload); err != nil {
		return nil, fmt.Errorf("error reading %d byte payload: %w", payloadLen, err)
	}
	if masked {
		for i, b := range payload {
			payload[i] = b ^ mask[i%4]
		}
	}

	return &Frame{
		Fin:     fin,
		RSV1:    rsv1,
		RSV2:    rsv2,
		RSV3:    rsv3,
		Opcode:  opcode,
		Payload: payload,
		Masked:  masked,
	}, nil
}

// WriteFrame writes an unmasked (i.e. server-side) websocket frame to the
// wire.
func WriteFrame(dst io.Writer, frame *Frame) error {
	return WriteFrameMasked(dst, frame, zeroMask)
}

// WriteFrameMasked writes a masked websocket frame to the wire.
func WriteFrameMasked(dst io.Writer, frame *Frame, mask [4]byte) error {
	// worst case payload size is 13 header bytes + payload size, where 13 is
	// (1 byte header) + (1-8 byte length) + (0-4 byte mask key)
	buf := make([]byte, 0, 13+len(frame.Payload))
	masked := mask != zeroMask

	// FIN, RSV1-3, OPCODE
	var b0 byte
	if frame.Fin {
		b0 |= 0b10000000
	}
	if frame.RSV1 {
		b0 |= 0b01000000
	}
	if frame.RSV2 {
		b0 |= 0b00100000
	}
	if frame.RSV3 {
		b0 |= 0b00010000
	}
	b0 |= uint8(frame.Opcode) & 0b00001111
	buf = append(buf, b0)

	// Masked bit, payload length
	var b1 byte
	if masked {
		b1 |= 0b10000000
	}

	payloadLen := int64(len(frame.Payload))
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
		for i, b := range frame.Payload {
			buf = append(buf, b^mask[i%4])
		}
	} else {
		buf = append(buf, frame.Payload...)
	}

	n, err := dst.Write(buf)
	if err != nil {
		return fmt.Errorf("error writing frame: %w", err)
	}
	if n != len(buf) {
		return fmt.Errorf("short write: %d != %d", n, len(buf))
	}
	return nil
}

// writeCloseFrame writes a close frame to the wire, with an optional error
// message.
func writeCloseFrame(dst io.Writer, code StatusCode, err error) error {
	var payload []byte
	if code > 0 {
		var errMsg []byte
		if err != nil {
			errMsg = []byte(err.Error())
		}
		payload = make([]byte, 0, 2+len(errMsg))
		payload = binary.BigEndian.AppendUint16(payload, uint16(code))
		payload = append(payload, errMsg...)
	}
	return WriteFrame(dst, &Frame{
		Fin:     true,
		Opcode:  OpcodeClose,
		Payload: payload,
	})
}

// messageFrames splits a message into N frames with payloads of at most
// fragmentSize bytes.
func messageFrames(msg *Message, fragmentSize int) []*Frame {
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
		end := offset + fragmentSize
		if end >= dataLen {
			fin = true
			end = dataLen
		}
		result = append(result, &Frame{
			Fin:     fin,
			Opcode:  opcode,
			Payload: msg.Payload[offset:end],
		})
		if fin {
			break
		}
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

func validateFrame(frame *Frame, maxFragmentSize int, requireMask bool) error {
	// We do not support any extensions, per the spec all RSV bits must be 0:
	// https://datatracker.ietf.org/doc/html/rfc6455#section-5.2
	if frame.RSV1 || frame.RSV2 || frame.RSV3 {
		return ErrUnsupportedRSVBits
	}

	if requireMask && !frame.Masked {
		return ErrUnmaskedClientFrame
	}

	switch frame.Opcode {
	case OpcodeContinuation, OpcodeText, OpcodeBinary:
		if len(frame.Payload) > maxFragmentSize {
			return fmt.Errorf("frame payload size %d exceeds maximum of %d bytes", len(frame.Payload), maxFragmentSize)
		}
	case OpcodeClose, OpcodePing, OpcodePong:
		// All control frames MUST have a payload length of 125 bytes or less
		// and MUST NOT be fragmented.
		// https://datatracker.ietf.org/doc/html/rfc6455#section-5.5
		if len(frame.Payload) > 125 {
			return fmt.Errorf("control frame %v payload size %d exceeds 125 bytes", frame.Opcode, len(frame.Payload))
		}
		if !frame.Fin {
			return fmt.Errorf("control frame %v must not be fragmented", frame.Opcode)
		}
	}

	if frame.Opcode == OpcodeClose {
		if len(frame.Payload) == 0 {
			return nil
		}
		if len(frame.Payload) == 1 {
			return fmt.Errorf("close frame payload must be at least 2 bytes")
		}

		code := binary.BigEndian.Uint16(frame.Payload[:2])
		if code < 1000 || code >= 5000 {
			return fmt.Errorf("close frame status code %d out of range", code)
		}
		if reservedStatusCodes[code] {
			return fmt.Errorf("close frame status code %d is reserved", code)
		}

		if len(frame.Payload) > 2 {
			if !utf8.Valid(frame.Payload[2:]) {
				return errors.New("close frame payload must be vaid UTF-8")
			}
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
