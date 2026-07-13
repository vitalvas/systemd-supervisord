package cli

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vitalvas/systemd-supervisord/internal/daemon"
)

func startTestSocketServer(t *testing.T, socketPath string, handler func(net.Conn)) net.Listener {
	t.Helper()

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go handler(conn)
		}
	}()

	return listener
}

func tempSocketPath(t *testing.T) string {
	t.Helper()

	f, err := os.CreateTemp("", "test-*.socket")
	require.NoError(t, err)

	path := f.Name()
	f.Close()
	os.Remove(path)

	t.Cleanup(func() {
		os.Remove(path)
	})

	return path
}

func TestSendRequest(t *testing.T) {
	t.Run("successful_request_response", func(t *testing.T) {
		socketPath := tempSocketPath(t)

		listener := startTestSocketServer(t, socketPath, func(conn net.Conn) {
			defer conn.Close()

			var req daemon.Request

			err := json.NewDecoder(conn).Decode(&req)
			assert.NoError(t, err)
			assert.Equal(t, "status", req.Command)
			assert.Equal(t, "nginx", req.UnitName)

			resp := daemon.Response{
				Success: true,
				Data:    "running",
			}

			err = json.NewEncoder(conn).Encode(resp)
			assert.NoError(t, err)
		})
		defer listener.Close()

		req := daemon.Request{
			Command:  "status",
			UnitName: "nginx",
		}

		resp, err := SendRequest(socketPath, req)
		require.NoError(t, err)
		assert.True(t, resp.Success)
		assert.Empty(t, resp.Error)
		assert.Equal(t, "running", resp.Data)
	})

	t.Run("error_response_from_server", func(t *testing.T) {
		socketPath := tempSocketPath(t)

		listener := startTestSocketServer(t, socketPath, func(conn net.Conn) {
			defer conn.Close()

			var req daemon.Request

			err := json.NewDecoder(conn).Decode(&req)
			assert.NoError(t, err)

			resp := daemon.Response{
				Success: false,
				Error:   "unit not found",
			}

			err = json.NewEncoder(conn).Encode(resp)
			assert.NoError(t, err)
		})
		defer listener.Close()

		req := daemon.Request{
			Command:  "status",
			UnitName: "unknown",
		}

		resp, err := SendRequest(socketPath, req)
		require.NoError(t, err)
		assert.False(t, resp.Success)
		assert.Equal(t, "unit not found", resp.Error)
	})

	t.Run("connection_failure_nonexistent_socket", func(t *testing.T) {
		socketPath := filepath.Join(os.TempDir(), "nonexistent_test.socket")

		req := daemon.Request{
			Command: "status",
		}

		resp, err := SendRequest(socketPath, req)
		require.Error(t, err)
		assert.Nil(t, resp)
		assert.Contains(t, err.Error(), "connecting to daemon")
	})

	t.Run("invalid_response_from_server", func(t *testing.T) {
		socketPath := tempSocketPath(t)

		listener := startTestSocketServer(t, socketPath, func(conn net.Conn) {
			defer conn.Close()

			var req daemon.Request

			err := json.NewDecoder(conn).Decode(&req)
			assert.NoError(t, err)

			_, err = conn.Write([]byte("not valid json"))
			assert.NoError(t, err)
		})
		defer listener.Close()

		req := daemon.Request{
			Command: "status",
		}

		resp, err := SendRequest(socketPath, req)
		require.Error(t, err)
		assert.Nil(t, resp)
		assert.Contains(t, err.Error(), "reading response")
	})
}
