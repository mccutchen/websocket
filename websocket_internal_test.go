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
	"net"
	"testing"

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
	assert.Equal(t, ws.hooks.OnClose != nil, true, "OnClose hook is nil")
	assert.Equal(t, ws.hooks.OnReadError != nil, true, "OnReadError hook is nil")
	assert.Equal(t, ws.hooks.OnReadFrame != nil, true, "OnReadFrame hook is nil")
	assert.Equal(t, ws.hooks.OnReadMessage != nil, true, "OnReadMessage hook is nil")
	assert.Equal(t, ws.hooks.OnWriteError != nil, true, "OnWriteError hook is nil")
	assert.Equal(t, ws.hooks.OnWriteFrame != nil, true, "OnWriteFrame hook is nil")
	assert.Equal(t, ws.hooks.OnWriteMessage != nil, true, "OnWriteMessage hook is nil")
}

func TestWriteBufferPoolConfig(t *testing.T) {
	t.Parallel()

	// Ensure our manual writeBufferPoolSizes and writeBufferPools arrays are
	// kept in sync
	assert.Equal(t, len(writeBufferSizes), len(writeBufferPools),
		"writeBufferSizes and writeBufferPools arrays must have the same length")

	for i, expectedSize := range writeBufferSizes {
		buf := writeBufferPools[i].Get().([]byte)
		assert.Equal(t, len(buf), expectedSize,
			"pool at index %d should create buffers of size %d, got %d", i, expectedSize, len(buf))
		assert.Equal(t, cap(buf), expectedSize,
			"pool at index %d should create buffers with capacity %d, got %d", i, expectedSize, cap(buf))
		// Put it back to avoid affecting other tests
		writeBufferPools[i].Put(buf)
	}
}
