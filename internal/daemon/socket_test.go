package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vitalvas/systemd-supervisord/internal/config"
	"github.com/vitalvas/systemd-supervisord/internal/systemd"
)

func TestRemoveStaleSocket(t *testing.T) {
	t.Run("no-op when path does not exist", func(t *testing.T) {
		err := removeStaleSocket(fmt.Sprintf("/tmp/nonexistent-socket-path-%d", time.Now().UnixNano()))
		assert.NoError(t, err)
	})

	t.Run("removes existing unix socket", func(t *testing.T) {
		socketPath := shortSocketPath(t)

		ln, err := net.Listen("unix", socketPath)
		require.NoError(t, err)

		// Keep listener open so the socket file exists.
		defer ln.Close()

		_, statErr := os.Stat(socketPath)
		require.NoError(t, statErr)

		err = removeStaleSocket(socketPath)
		assert.NoError(t, err)

		_, statErr = os.Stat(socketPath)
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("rejects directory path", func(t *testing.T) {
		dir := t.TempDir()

		err := removeStaleSocket(dir)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "directory")

		_, statErr := os.Stat(dir)
		assert.NoError(t, statErr)
	})

	t.Run("rejects regular file", func(t *testing.T) {
		f, err := os.CreateTemp("", "not-a-socket-*")
		require.NoError(t, err)
		f.Close()
		defer os.Remove(f.Name())

		err = removeStaleSocket(f.Name())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not a unix socket")

		_, statErr := os.Stat(f.Name())
		assert.NoError(t, statErr)
	})
}

func TestProcessRequest(t *testing.T) {
	t.Run("list registered units", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		d.mu.Lock()
		d.registeredUnits["app.service"] = struct{}{}
		d.registeredUnits["db.service"] = struct{}{}
		d.mu.Unlock()

		resp := d.processRequest(context.Background(), Request{Command: "list"})

		assert.True(t, resp.Success)
		assert.Empty(t, resp.Error)

		units, ok := resp.Data.([]string)
		require.True(t, ok)
		assert.Len(t, units, 2)
		assert.ElementsMatch(t, []string{"app.service", "db.service"}, units)
	})

	t.Run("list empty", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		resp := d.processRequest(context.Background(), Request{Command: "list"})

		assert.True(t, resp.Success)

		units, ok := resp.Data.([]string)
		require.True(t, ok)
		assert.Empty(t, units)
	})

	t.Run("status all units", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		d.sm.Register("app.service")
		d.sm.UpdateState("app.service", systemd.ActiveStateActive, systemd.SubStateRunning)

		resp := d.processRequest(context.Background(), Request{Command: "status"})

		assert.True(t, resp.Success)
		assert.Empty(t, resp.Error)
		assert.NotNil(t, resp.Data)
	})

	t.Run("status specific unit found", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		d.sm.Register("app.service")
		d.sm.UpdateState("app.service", systemd.ActiveStateActive, systemd.SubStateRunning)

		resp := d.processRequest(context.Background(), Request{
			Command:  "status",
			UnitName: "app.service",
		})

		assert.True(t, resp.Success)
		assert.Empty(t, resp.Error)
		assert.NotNil(t, resp.Data)
	})

	t.Run("status specific unit not found", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		resp := d.processRequest(context.Background(), Request{
			Command:  "status",
			UnitName: "nonexistent.service",
		})

		assert.False(t, resp.Success)
		assert.Contains(t, resp.Error, "not found")
	})

	t.Run("start success", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		resp := d.processRequest(context.Background(), Request{
			Command:  "start",
			UnitName: "app.service",
		})

		assert.True(t, resp.Success)
		assert.Empty(t, resp.Error)
		assert.Contains(t, mgr.startCalls, "app.service")
	})

	t.Run("start without unit name", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		resp := d.processRequest(context.Background(), Request{
			Command: "start",
		})

		assert.False(t, resp.Success)
		assert.Equal(t, "unit name required", resp.Error)
	})

	t.Run("start error", func(t *testing.T) {
		mgr := newMockManager()
		mgr.startErr = fmt.Errorf("failed to start")

		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		resp := d.processRequest(context.Background(), Request{
			Command:  "start",
			UnitName: "app.service",
		})

		assert.False(t, resp.Success)
		assert.Equal(t, "failed to start", resp.Error)
	})

	t.Run("stop success", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		resp := d.processRequest(context.Background(), Request{
			Command:  "stop",
			UnitName: "app.service",
		})

		assert.True(t, resp.Success)
		assert.Empty(t, resp.Error)
		assert.Contains(t, mgr.stopCalls, "app.service")
	})

	t.Run("stop without unit name", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		resp := d.processRequest(context.Background(), Request{
			Command: "stop",
		})

		assert.False(t, resp.Success)
		assert.Equal(t, "unit name required", resp.Error)
	})

	t.Run("stop error", func(t *testing.T) {
		mgr := newMockManager()
		mgr.stopErr = fmt.Errorf("failed to stop")

		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		resp := d.processRequest(context.Background(), Request{
			Command:  "stop",
			UnitName: "app.service",
		})

		assert.False(t, resp.Success)
		assert.Equal(t, "failed to stop", resp.Error)
	})

	t.Run("restart success", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		resp := d.processRequest(context.Background(), Request{
			Command:  "restart",
			UnitName: "app.service",
		})

		assert.True(t, resp.Success)
		assert.Empty(t, resp.Error)
		assert.Contains(t, mgr.restartCalls, "app.service")
	})

	t.Run("restart without unit name", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		resp := d.processRequest(context.Background(), Request{
			Command: "restart",
		})

		assert.False(t, resp.Success)
		assert.Equal(t, "unit name required", resp.Error)
	})

	t.Run("restart error", func(t *testing.T) {
		mgr := newMockManager()
		mgr.restartErr = fmt.Errorf("failed to restart")

		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		resp := d.processRequest(context.Background(), Request{
			Command:  "restart",
			UnitName: "app.service",
		})

		assert.False(t, resp.Success)
		assert.Equal(t, "failed to restart", resp.Error)
	})

	t.Run("unknown command", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		resp := d.processRequest(context.Background(), Request{
			Command: "invalid",
		})

		assert.False(t, resp.Success)
		assert.Contains(t, resp.Error, "unknown command")
		assert.Contains(t, resp.Error, "invalid")
	})
}

func shortSocketPath(t *testing.T) string {
	t.Helper()

	f, err := os.CreateTemp("", "sd-*.sock")
	require.NoError(t, err)

	path := f.Name()
	f.Close()
	os.Remove(path)

	t.Cleanup(func() { os.Remove(path) })

	return path
}

func TestCreateListener(t *testing.T) {
	t.Run("creates unix socket with correct permissions", func(t *testing.T) {
		socketPath := shortSocketPath(t)

		mgr := newMockManager()
		cfg := &config.Config{Socket: socketPath}
		d := newTestDaemon(mgr, cfg)

		ln, err := d.createListener()
		require.NoError(t, err)
		defer ln.Close()

		info, statErr := os.Stat(socketPath)
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o660), info.Mode().Perm())
	})

	t.Run("fails for invalid path", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{Socket: "/nonexistent/deeply/nested/path/test.sock"}
		d := newTestDaemon(mgr, cfg)

		_, err := d.createListener()
		assert.Error(t, err)
	})
}

func TestAcquireListener(t *testing.T) {
	t.Run("falls back to create listener without socket activation", func(t *testing.T) {
		socketPath := shortSocketPath(t)

		mgr := newMockManager()
		cfg := &config.Config{Socket: socketPath}
		d := newTestDaemon(mgr, cfg)

		ln, err := d.acquireListener()
		require.NoError(t, err)
		defer ln.Close()

		_, statErr := os.Stat(socketPath)
		require.NoError(t, statErr)
	})
}

func TestListenSocket(t *testing.T) {
	t.Run("creates socket and accepts connections", func(t *testing.T) {
		socketPath := shortSocketPath(t)

		mgr := newMockManager()
		cfg := &config.Config{
			Socket: socketPath,
		}
		d := newTestDaemon(mgr, cfg)

		d.sm.Register("app.service")
		d.sm.UpdateState("app.service", systemd.ActiveStateActive, systemd.SubStateRunning)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err := d.ListenSocket(ctx)
		require.NoError(t, err)

		_, statErr := os.Stat(socketPath)
		require.NoError(t, statErr)

		conn, err := net.Dial("unix", socketPath)
		require.NoError(t, err)
		defer conn.Close()

		req := Request{Command: "status"}
		require.NoError(t, json.NewEncoder(conn).Encode(req))

		var resp Response
		require.NoError(t, json.NewDecoder(conn).Decode(&resp))

		assert.True(t, resp.Success)
	})

	t.Run("closes socket on context cancellation", func(t *testing.T) {
		socketPath := shortSocketPath(t)

		mgr := newMockManager()
		cfg := &config.Config{
			Socket: socketPath,
		}
		d := newTestDaemon(mgr, cfg)

		ctx, cancel := context.WithCancel(context.Background())

		err := d.ListenSocket(ctx)
		require.NoError(t, err)

		cancel()

		require.Eventually(t, func() bool {
			_, dialErr := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
			return dialErr != nil
		}, 2*time.Second, 50*time.Millisecond)
	})

	t.Run("removes existing socket before listening", func(t *testing.T) {
		socketPath := shortSocketPath(t)

		oldLn, err := net.Listen("unix", socketPath)
		require.NoError(t, err)
		oldLn.Close()

		mgr := newMockManager()
		cfg := &config.Config{
			Socket: socketPath,
		}
		d := newTestDaemon(mgr, cfg)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err = d.ListenSocket(ctx)
		require.NoError(t, err)

		info, statErr := os.Stat(socketPath)
		require.NoError(t, statErr)
		assert.NotZero(t, info.Mode()&os.ModeSocket)
	})

	t.Run("fails when socket path is invalid", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{
			Socket: "/nonexistent/deeply/nested/path/test.sock",
		}
		d := newTestDaemon(mgr, cfg)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		err := d.ListenSocket(ctx)
		assert.Error(t, err)
	})

	t.Run("handles accept error after listener close", func(t *testing.T) {
		socketPath := shortSocketPath(t)

		mgr := newMockManager()
		cfg := &config.Config{
			Socket: socketPath,
		}
		d := newTestDaemon(mgr, cfg)

		ctx, cancel := context.WithCancel(context.Background())

		err := d.ListenSocket(ctx)
		require.NoError(t, err)

		// Cancel context to close the listener, which triggers accept error path.
		cancel()

		// Wait for the goroutines to process the cancellation.
		time.Sleep(100 * time.Millisecond)
	})
}

func TestHandleConnection(t *testing.T) {
	t.Run("handles valid request", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		d.sm.Register("app.service")

		server, client := net.Pipe()
		defer server.Close()
		defer client.Close()

		done := make(chan struct{})
		go func() {
			defer close(done)
			d.handleConnection(context.Background(), server)
		}()

		req := Request{Command: "status", UnitName: "app.service"}
		require.NoError(t, json.NewEncoder(client).Encode(req))

		var resp Response
		require.NoError(t, json.NewDecoder(client).Decode(&resp))

		assert.True(t, resp.Success)

		<-done
	})

	t.Run("handles invalid json", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		server, client := net.Pipe()
		defer server.Close()
		defer client.Close()

		done := make(chan struct{})
		go func() {
			defer close(done)
			d.handleConnection(context.Background(), server)
		}()

		_, err := client.Write([]byte("not json\n"))
		require.NoError(t, err)

		var resp Response
		require.NoError(t, json.NewDecoder(client).Decode(&resp))

		assert.False(t, resp.Success)
		assert.Equal(t, "invalid request", resp.Error)

		<-done
	})

	t.Run("processes start command via connection", func(t *testing.T) {
		mgr := newMockManager()
		cfg := &config.Config{}
		d := newTestDaemon(mgr, cfg)

		server, client := net.Pipe()
		defer server.Close()
		defer client.Close()

		done := make(chan struct{})
		go func() {
			defer close(done)
			d.handleConnection(context.Background(), server)
		}()

		req := Request{Command: "start", UnitName: "app.service"}
		require.NoError(t, json.NewEncoder(client).Encode(req))

		var resp Response
		require.NoError(t, json.NewDecoder(client).Decode(&resp))

		assert.True(t, resp.Success)
		assert.Contains(t, mgr.startCalls, "app.service")

		<-done
	})
}

func TestWriteResponse(t *testing.T) {
	t.Run("writes response to open connection", func(t *testing.T) {
		server, client := net.Pipe()
		defer server.Close()
		defer client.Close()

		done := make(chan struct{})
		go func() {
			defer close(done)
			writeResponse(server, Response{Success: true, Data: "hello"})
		}()

		var resp Response
		require.NoError(t, json.NewDecoder(client).Decode(&resp))

		assert.True(t, resp.Success)

		<-done
	})

	t.Run("handles write to closed connection", func(_ *testing.T) {
		server, client := net.Pipe()
		client.Close()
		server.Close()

		writeResponse(server, Response{Success: true, Data: "hello"})
	})
}

func TestRequestResponseSerialization(t *testing.T) {
	t.Run("request serialization", func(t *testing.T) {
		req := Request{Command: "status", UnitName: "app.service"}

		data, err := json.Marshal(req)
		require.NoError(t, err)

		var decoded Request
		require.NoError(t, json.Unmarshal(data, &decoded))

		assert.Equal(t, req.Command, decoded.Command)
		assert.Equal(t, req.UnitName, decoded.UnitName)
	})

	t.Run("request with empty unit name omits field", func(t *testing.T) {
		req := Request{Command: "status"}

		data, err := json.Marshal(req)
		require.NoError(t, err)

		var raw map[string]interface{}
		require.NoError(t, json.Unmarshal(data, &raw))

		_, hasUnitName := raw["unit_name"]
		assert.False(t, hasUnitName)
	})

	t.Run("response serialization", func(t *testing.T) {
		resp := Response{Success: true, Data: "test"}

		data, err := json.Marshal(resp)
		require.NoError(t, err)

		var decoded Response
		require.NoError(t, json.Unmarshal(data, &decoded))

		assert.True(t, decoded.Success)
	})

	t.Run("error response omits data", func(t *testing.T) {
		resp := Response{Success: false, Error: "something failed"}

		data, err := json.Marshal(resp)
		require.NoError(t, err)

		var raw map[string]interface{}
		require.NoError(t, json.Unmarshal(data, &raw))

		_, hasData := raw["data"]
		assert.False(t, hasData)
	})
}
