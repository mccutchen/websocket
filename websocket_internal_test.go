package websocket

import (
	"bufio"
	"net"
	"testing"

	"github.com/mccutchen/websocket/internal/testing/assert"
)

func TestDefaults(t *testing.T) {
	t.Parallel()

	var (
		conn    net.Conn
		connBuf *bufio.ReadWriter
		key     = ClientKey("test-client-key")
		opts    = Options{}
		ws      = New(conn, connBuf, key, opts)
	)

	assert.Equal(t, ws.ClientKey(), key, "incorrect client key")
	assert.Equal(t, ws.maxFrameSize, DefaultMaxFrameSize, "incorrect max framesize")
	assert.Equal(t, ws.maxMessageSize, DefaultMaxMessageSize, "incorrect max message size")
	assert.Equal(t, ws.readTimeout, 0, "incorrect read timeout")
	assert.Equal(t, ws.writeTimeout, 0, "incorrect write timeout")
	assert.Equal(t, ws.server, true, "incorrect server value")
	assert.Equal(t, ws.hooks.OnClose != nil, true, "OnClose hook is nil")
	assert.Equal(t, ws.hooks.OnReadError != nil, true, "OnReadError hook is nil")
	assert.Equal(t, ws.hooks.OnReadFrame != nil, true, "OnReadFrame hook is nil")
	assert.Equal(t, ws.hooks.OnReadMessage != nil, true, "OnReadMessage hook is nil")
	assert.Equal(t, ws.hooks.OnWriteError != nil, true, "OnWriteError hook is nil")
	assert.Equal(t, ws.hooks.OnWriteFrame != nil, true, "OnWriteFrame hook is nil")
	assert.Equal(t, ws.hooks.OnWriteMessage != nil, true, "OnWriteMessage hook is nil")
}
