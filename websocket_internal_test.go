package websocket

import (
	"net"
	"testing"

	"github.com/mccutchen/websocket/internal/testing/assert"
)

func TestDefaults(t *testing.T) {
	t.Parallel()

	var (
		wrapedConn net.Conn
		key        = ClientKey("test-client-key")
		opts       = Options{}
		conn       = New(wrapedConn, key, opts)
	)

	assert.Equal(t, conn.maxFragmentSize, DefaultMaxFragmentSize, "incorrect max fragment size")
	assert.Equal(t, conn.maxMessageSize, DefaultMaxMessageSize, "incorrect max message size")
	assert.Equal(t, conn.readTimeout, 0, "incorrect read timeout")
	assert.Equal(t, conn.writeTimeout, 0, "incorrect write timeout")
	assert.Equal(t, conn.server, true, "incorrect server value")
	assert.Equal(t, conn.hooks.OnClose != nil, true, "OnClose hook is nil")
	assert.Equal(t, conn.hooks.OnReadError != nil, true, "OnReadError hook is nil")
	assert.Equal(t, conn.hooks.OnReadFrame != nil, true, "OnReadFrame hook is nil")
	assert.Equal(t, conn.hooks.OnReadMessage != nil, true, "OnReadMessage hook is nil")
	assert.Equal(t, conn.hooks.OnWriteError != nil, true, "OnWriteError hook is nil")
	assert.Equal(t, conn.hooks.OnWriteFrame != nil, true, "OnWriteFrame hook is nil")
	assert.Equal(t, conn.hooks.OnWriteMessage != nil, true, "OnWriteMessage hook is nil")
}
