package websocket

// ============================================================================
// "Internal" tests
// ============================================================================
//
// The vast majority of this package's tests are executed against its public
// API (i.e. they are defined in `package websocket_test` and must import the
// websocket package).
//
// These internal tests have access to the package's internals, used to ensure
// coverage of details that can't be accessed via its public API.
//
// These tests should be minimized in favor of public API tests.

import (
	"context"
	"fmt"
	"io"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/mccutchen/websocket/internal/testing/assert"
)

func TestDefaults(t *testing.T) {
	t.Parallel()

	var (
		conn net.Conn
		key  = ClientKey("test-client-key")
		opts = Options{}
		ws   = New(conn, key, ServerMode, opts)
	)

	assert.Equal(t, ws.ClientKey(), key, "incorrect client key")
	assert.Equal(t, ws.maxFrameSize, DefaultMaxFrameSize, "incorrect max framesize")
	assert.Equal(t, ws.maxMessageSize, DefaultMaxMessageSize, "incorrect max message size")
	assert.Equal(t, ws.readTimeout, 0, "incorrect read timeout")
	assert.Equal(t, ws.writeTimeout, 0, "incorrect write timeout")
	assert.Equal(t, ws.closeTimeout, 0, "incorrect close timeout")
	assert.Equal(t, ws.mode, ServerMode, "incorrect mode value")
	assert.Equal(t, ws.hooks.OnCloseHandshakeStart != nil, true, "OnCloseHandshakeStart hook is nil")
	assert.Equal(t, ws.hooks.OnCloseHandshakeDone != nil, true, "OnCloseHandshakeDone hook is nil")
	assert.Equal(t, ws.hooks.OnReadError != nil, true, "OnReadError hook is nil")
	assert.Equal(t, ws.hooks.OnReadFrame != nil, true, "OnReadFrame hook is nil")
	assert.Equal(t, ws.hooks.OnReadMessage != nil, true, "OnReadMessage hook is nil")
	assert.Equal(t, ws.hooks.OnWriteError != nil, true, "OnWriteError hook is nil")
	assert.Equal(t, ws.hooks.OnWriteFrame != nil, true, "OnWriteFrame hook is nil")
	assert.Equal(t, ws.hooks.OnWriteMessage != nil, true, "OnWriteMessage hook is nil")

	t.Run("CloseTimeout defaults to ReadTimeout if set", func(t *testing.T) {
		var (
			conn        *net.TCPConn
			key         = ClientKey("test-client-key")
			readTimeout = 10 * time.Second
			opts        = Options{
				ReadTimeout: readTimeout,
			}
			ws = New(conn, key, ServerMode, opts)
		)
		assert.Equal(t, ws.readTimeout, readTimeout, "incorrect read timeout")
		assert.Equal(t, ws.closeTimeout, readTimeout, "incorrect close timeout")
		assert.Equal(t, ws.writeTimeout, 0, "incorrect write timeout")
	})
}

func TestMask(t *testing.T) {
	t.Parallel()

	var (
		conn net.Conn
		key  = ClientKey("test-client-key")
		opts = Options{}
	)

	{
		ws := New(conn, key, ServerMode, opts)
		mask := ws.mask()
		assert.Equal(t, mask, Unmasked, "ServerMode should be unmasked")
	}

	{
		ws := New(conn, key, ClientMode, opts)
		mask := ws.mask()
		assert.Equal(t, mask != Unmasked, true, "ClientMode should be masked")
	}
}

func TestSetState(t *testing.T) {
	t.Parallel()

	// build map of invalid transitions as the inverse of validTransitions
	invalidTransitions := make(map[connState][]connState)
	for state, validStates := range validTransitions {
		var invalidStates []connState
		for candidate := range validTransitions {
			if candidate == state {
				continue
			}
			if !slices.Contains(validStates, candidate) {
				invalidStates = append(invalidStates, candidate)
			}
		}
		if len(invalidStates) > 0 {
			invalidTransitions[state] = invalidStates
		}
	}

	// ensure each invalid transition triggers a panic
	for state, invalidStates := range invalidTransitions {
		for _, invalidState := range invalidStates {
			t.Run(fmt.Sprintf("illegal transition %s -> %s", state, invalidState), func(t *testing.T) {
				t.Parallel()
				defer func() {
					r := recover()
					if r == nil {
						t.Fatalf("expected to catch panic on invalid transition from %s -> %s", state, invalidState)
					}
					assert.Equal(t, fmt.Sprint(r), fmt.Sprintf("websocket: setState: invalid transition from %q to %q", state, invalidState), "incorrect panic message")
				}()

				var (
					conn net.Conn
					key  = ClientKey("test-client-key")
					opts = Options{}
					ws   = New(conn, key, ServerMode, opts)
				)
				ws.state = state
				ws.setState(invalidState)
			})
		}
	}
}

func TestStatusCodeForError(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		err        error
		wantStatus StatusCode
		wantReason string
	}{
		{
			err:        nil,
			wantStatus: StatusNormalClosure,
			wantReason: "",
		},
		{
			err:        ErrClientFrameUnmasked,
			wantStatus: StatusProtocolError,
			wantReason: "client frame must be masked",
		},
		{
			err:        context.DeadlineExceeded,
			wantStatus: StatusInternalError,
			wantReason: context.DeadlineExceeded.Error(),
		},
		{
			err:        fmt.Errorf("wrapped io.EOF: %w", io.EOF),
			wantStatus: StatusNormalClosure,
			wantReason: "", // io.EOF is not considered an error
		},
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprint(tc.err), func(t *testing.T) {
			t.Parallel()
			gotStatus, gotReason := statusCodeForError(tc.err)
			assert.Equal(t, gotStatus, tc.wantStatus, "incorrect status code")
			assert.Equal(t, gotReason, tc.wantReason, "incorrect reason")
		})
	}
}
