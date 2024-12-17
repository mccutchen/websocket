package websocket

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/mccutchen/websocket/internal/testing/assert"
)

func TestDefaults(t *testing.T) {
	t.Parallel()

	var (
		wrapedConn net.Conn
		wrappedBuf *bufio.ReadWriter
		key        = ClientKey("test-client-key")
		opts       = Options{}
		conn       = newConn(wrapedConn, wrappedBuf, key, opts)
	)

	assert.Equal(t, conn.maxFragmentSize, DefaultMaxFragmentSize, "incorrect max fragment size")
	assert.Equal(t, conn.maxMessageSize, DefaultMaxMessageSize, "incorrect max message size")
	assert.Equal(t, conn.readTimeout, 0, "incorrect read timeout")
	assert.Equal(t, conn.writeTimeout, 0, "incorrect write timeout")
	assert.Equal(t, conn.isServer, true, "incorrect server value")
	assert.Equal(t, conn.hooks.OnCloseHandshakeStart != nil, true, "OnCloseHandshakeStart hook is nil")
	assert.Equal(t, conn.hooks.OnCloseHandshakeDone != nil, true, "OnCloseHandshakeDone hook is nil")
	assert.Equal(t, conn.hooks.OnReadError != nil, true, "OnReadError hook is nil")
	assert.Equal(t, conn.hooks.OnReadFrame != nil, true, "OnReadFrame hook is nil")
	assert.Equal(t, conn.hooks.OnReadMessage != nil, true, "OnReadMessage hook is nil")
	assert.Equal(t, conn.hooks.OnWriteError != nil, true, "OnWriteError hook is nil")
	assert.Equal(t, conn.hooks.OnWriteFrame != nil, true, "OnWriteFrame hook is nil")
	assert.Equal(t, conn.hooks.OnWriteMessage != nil, true, "OnWriteMessage hook is nil")
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
