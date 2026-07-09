package socketactivation

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelay(t *testing.T) {
	t.Run("copies both directions and tracks traffic", func(t *testing.T) {
		clientLocal, clientRemote := net.Pipe()
		backendLocal, backendRemote := net.Pipe()

		var traffic atomic.Int32

		go relay(clientRemote, backendLocal, func() { traffic.Add(1) })

		// Client -> backend.
		go func() { _, _ = clientLocal.Write([]byte("ping")) }()

		buf := make([]byte, 4)
		require.NoError(t, backendRemote.SetReadDeadline(time.Now().Add(2*time.Second)))
		n, err := backendRemote.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, "ping", string(buf[:n]))

		// Backend -> client.
		go func() { _, _ = backendRemote.Write([]byte("pong")) }()

		require.NoError(t, clientLocal.SetReadDeadline(time.Now().Add(2*time.Second)))
		n, err = clientLocal.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, "pong", string(buf[:n]))

		require.Eventually(t, func() bool {
			return traffic.Load() >= 2
		}, 2*time.Second, 10*time.Millisecond)

		clientLocal.Close()
		backendRemote.Close()
	})

	t.Run("returns when a side closes", func(t *testing.T) {
		clientLocal, clientRemote := net.Pipe()
		backendLocal, backendRemote := net.Pipe()

		done := make(chan struct{})
		go func() {
			relay(clientRemote, backendLocal, nil)
			close(done)
		}()

		clientLocal.Close()
		backendRemote.Close()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("relay did not return after connections closed")
		}
	})
}

func TestCopyWithTraffic(t *testing.T) {
	t.Run("nil callback is tolerated", func(t *testing.T) {
		src, srcWriter := net.Pipe()
		dst, dstReader := net.Pipe()

		go copyWithTraffic(dst, src, nil)

		go func() {
			_, _ = srcWriter.Write([]byte("data"))
			srcWriter.Close()
		}()

		buf := make([]byte, 4)
		require.NoError(t, dstReader.SetReadDeadline(time.Now().Add(2*time.Second)))
		n, err := dstReader.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, "data", string(buf[:n]))

		src.Close()
		dst.Close()
		dstReader.Close()
	})
}

func TestCloseWrite(t *testing.T) {
	t.Run("half-closes tcp connection", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		accepted := make(chan net.Conn, 1)
		go func() {
			conn, err := ln.Accept()
			if err == nil {
				accepted <- conn
			}
		}()

		conn, err := net.Dial("tcp", ln.Addr().String())
		require.NoError(t, err)
		defer conn.Close()

		server := <-accepted
		defer server.Close()

		// Half-close the write side; the peer should observe EOF.
		closeWrite(conn)

		require.NoError(t, server.SetReadDeadline(time.Now().Add(2*time.Second)))
		buf := make([]byte, 1)
		_, err = server.Read(buf)
		assert.Error(t, err) // EOF
	})

	t.Run("no-op on non-tcp connection", func(_ *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()

		// net.Pipe conns do not implement CloseWrite; must not panic.
		closeWrite(a)
	})
}
