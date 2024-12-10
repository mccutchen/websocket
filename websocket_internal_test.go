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
	"net"
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
	assert.Equal(t, ws.mode, ServerMode, "incorrect mode value")
	assert.Equal(t, ws.hooks.OnCloseHandshakeStart != nil, true, "OnCloseHandshakeStart hook is nil")
	assert.Equal(t, ws.hooks.OnCloseHandshakeDone != nil, true, "OnCloseHandshakeDone hook is nil")
	assert.Equal(t, ws.hooks.OnReadError != nil, true, "OnReadError hook is nil")
	assert.Equal(t, ws.hooks.OnReadFrame != nil, true, "OnReadFrame hook is nil")
	assert.Equal(t, ws.hooks.OnReadMessage != nil, true, "OnReadMessage hook is nil")
	assert.Equal(t, ws.hooks.OnWriteError != nil, true, "OnWriteError hook is nil")
	assert.Equal(t, ws.hooks.OnWriteFrame != nil, true, "OnWriteFrame hook is nil")
	assert.Equal(t, ws.hooks.OnWriteMessage != nil, true, "OnWriteMessage hook is nil")
}

func TestChooseDeadline(t *testing.T) {
	now := time.Now()
	nowSource := func() time.Time {
		return now
	}

	testCases := map[string]struct {
		baseTimeout  time.Duration
		ctxDeadline  time.Time
		ctxTimeout   time.Duration
		wantDeadline time.Time
	}{
		"no timeout, no deadline": {
			baseTimeout:  0,
			ctxTimeout:   0,
			wantDeadline: now,
		},
		"ctx timeout works if deadline unset": {
			baseTimeout:  0,
			ctxTimeout:   10 * time.Second,
			wantDeadline: now.Add(10 * time.Second),
		},
	}

	for name, tc := range testCases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.ctxTimeout)
			defer cancel()
			got := chooseDeadline(ctx, tc.baseTimeout, nowSource)
			assert.RoughlyEqual(t, got.UnixNano(), tc.wantDeadline.UnixNano(), (10 * time.Millisecond).Nanoseconds())
			assert.Equal(t, got, tc.wantDeadline, "expected a different deadline")
		})
	}
}
