package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vitalvas/systemd-supervisord/internal/daemon"
	"github.com/vitalvas/systemd-supervisord/internal/socketactivation"
	"github.com/vitalvas/systemd-supervisord/internal/statemanager"
	"github.com/vitalvas/systemd-supervisord/internal/systemd"
)

func startMockDaemon(t *testing.T, handler func(req daemon.Request) daemon.Response) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "cli")
	require.NoError(t, err)

	t.Cleanup(func() { os.RemoveAll(dir) })

	socketPath := filepath.Join(dir, "t.sock")

	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			go func(c net.Conn) {
				defer c.Close()

				var req daemon.Request

				if err := json.NewDecoder(c).Decode(&req); err != nil {
					return
				}

				resp := handler(req)

				json.NewEncoder(c).Encode(resp)
			}(conn)
		}
	}()

	return socketPath
}

func TestRootCommand(t *testing.T) {
	t.Run("has all subcommands", func(t *testing.T) {
		root := NewRootCommand()

		names := make(map[string]bool)
		for _, cmd := range root.Commands() {
			names[cmd.Name()] = true
		}

		assert.True(t, names["run"])
		assert.True(t, names["list"])
		assert.True(t, names["status"])
		assert.True(t, names["start"])
		assert.True(t, names["stop"])
		assert.True(t, names["restart"])
		assert.True(t, names["check"])
	})

	t.Run("version flag", func(t *testing.T) {
		root := NewRootCommand()
		root.Version = "1.2.3"

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetArgs([]string{"--version"})

		require.NoError(t, root.Execute())
		assert.Contains(t, buf.String(), "1.2.3")
	})

	t.Run("default flags", func(t *testing.T) {
		root := NewRootCommand()

		cfgFlag := root.PersistentFlags().Lookup("config")
		require.NotNil(t, cfgFlag)
		assert.Equal(t, "/etc/systemd-supervisord/config.yaml", cfgFlag.DefValue)

		sockFlag := root.PersistentFlags().Lookup("socket")
		require.NotNil(t, sockFlag)
		assert.Equal(t, "/var/run/systemd-supervisord.socket", sockFlag.DefValue)
	})

	t.Run("error propagated from subcommand", func(t *testing.T) {
		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"check", "--config", "/nonexistent/config.yaml"})

		err := root.Execute()
		assert.Error(t, err)
	})
}

func TestPrintUnitList(t *testing.T) {
	t.Run("prints unit names", func(t *testing.T) {
		units := []string{"nginx.service", "app.service", "backup.timer"}

		buf := new(bytes.Buffer)
		require.NoError(t, PrintUnitList(buf, units))

		output := buf.String()
		assert.Contains(t, output, "nginx.service\n")
		assert.Contains(t, output, "app.service\n")
		assert.Contains(t, output, "backup.timer\n")
	})

	t.Run("empty list", func(t *testing.T) {
		buf := new(bytes.Buffer)
		require.NoError(t, PrintUnitList(buf, []string{}))

		assert.Empty(t, buf.String())
	})
}

func TestListCmd(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		units := []string{"nginx.service", "app.service"}

		socketPath := startMockDaemon(t, func(req daemon.Request) daemon.Response {
			assert.Equal(t, "list", req.Command)

			return daemon.Response{Success: true, Data: units}
		})

		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", socketPath, "list"})

		require.NoError(t, root.Execute())

		output := buf.String()
		assert.Contains(t, output, "nginx.service")
		assert.Contains(t, output, "app.service")
	})

	t.Run("error response", func(t *testing.T) {
		socketPath := startMockDaemon(t, func(_ daemon.Request) daemon.Response {
			return daemon.Response{Success: false, Error: "daemon error"}
		})

		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", socketPath, "list"})

		err := root.Execute()
		assert.Error(t, err)
	})

	t.Run("connection failure", func(t *testing.T) {
		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", "/tmp/nonexistent-cli-test.sock", "list"})

		err := root.Execute()
		assert.Error(t, err)
	})
}

func TestPrintUnitStatus(t *testing.T) {
	t.Run("with health check", func(t *testing.T) {
		healthy := true
		status := statemanager.UnitStatus{
			UnitName:       "nginx.service",
			ActiveState:    systemd.ActiveStateActive,
			SubState:       systemd.SubStateRunning,
			Healthy:        &healthy,
			RestartCount:   2,
			LastTransition: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		}

		buf := new(bytes.Buffer)
		require.NoError(t, PrintUnitStatus(buf, status))

		output := buf.String()
		assert.Contains(t, output, "nginx.service")
		assert.Contains(t, output, "active (running)")
		assert.Contains(t, output, "true")
		assert.Contains(t, output, "2")
		assert.Contains(t, output, "2026-01-15 10:00:00")
	})

	t.Run("without health check", func(t *testing.T) {
		status := statemanager.UnitStatus{
			UnitName:    "backup.timer",
			ActiveState: systemd.ActiveStateActive,
			SubState:    systemd.SubStateWaiting,
		}

		buf := new(bytes.Buffer)
		require.NoError(t, PrintUnitStatus(buf, status))

		output := buf.String()
		assert.Contains(t, output, "backup.timer")
		assert.Contains(t, output, "n/a")
	})
}

func TestPrintAllStatuses(t *testing.T) {
	t.Run("multiple units sorted by name", func(t *testing.T) {
		healthy := true
		unhealthy := false

		statuses := []statemanager.UnitStatus{
			{
				UnitName:     "nginx.service",
				ActiveState:  systemd.ActiveStateActive,
				SubState:     systemd.SubStateRunning,
				Healthy:      &healthy,
				RestartCount: 0,
			},
			{
				UnitName:     "app.service",
				ActiveState:  systemd.ActiveStateFailed,
				SubState:     systemd.SubStateFailed,
				Healthy:      &unhealthy,
				RestartCount: 3,
			},
			{
				UnitName:    "backup.timer",
				ActiveState: systemd.ActiveStateActive,
				SubState:    systemd.SubStateWaiting,
			},
		}

		buf := new(bytes.Buffer)
		require.NoError(t, PrintAllStatuses(buf, statuses))

		output := buf.String()
		assert.Contains(t, output, "UNIT")
		assert.Contains(t, output, "active/running")
		assert.Contains(t, output, "failed/failed")
		assert.Contains(t, output, "n/a")

		appIdx := strings.Index(output, "app.service")
		backupIdx := strings.Index(output, "backup.timer")
		nginxIdx := strings.Index(output, "nginx.service")
		assert.Greater(t, backupIdx, appIdx)
		assert.Greater(t, nginxIdx, backupIdx)
	})

	t.Run("empty list", func(t *testing.T) {
		buf := new(bytes.Buffer)
		require.NoError(t, PrintAllStatuses(buf, []statemanager.UnitStatus{}))

		output := buf.String()
		assert.Contains(t, output, "UNIT")
	})
}

func TestPrintSocketStatuses(t *testing.T) {
	t.Run("listeners sorted by unit", func(t *testing.T) {
		statuses := []socketactivation.Status{
			{
				Name:              "gemma",
				Unit:              "llama@gemma.service",
				Listen:            "127.0.0.1:4101",
				Protocol:          []string{"tcp"},
				Backend:           "127.0.0.1:5101",
				Running:           true,
				ActiveConnections: 2,
			},
			{
				Name:        "coredns",
				Unit:        "coredns.service",
				Listen:      "127.0.0.1:53",
				Protocol:    []string{"udp", "tcp"},
				Backend:     "127.0.0.1:5353",
				Running:     true,
				IdleSeconds: 42,
			},
			{
				Name:     "web",
				Unit:     "web.service",
				Listen:   "127.0.0.1:8080",
				Protocol: []string{"tcp"},
				Backend:  "127.0.0.1:9090",
				Running:  false,
			},
		}

		buf := new(bytes.Buffer)
		require.NoError(t, PrintSocketStatuses(buf, statuses))

		output := buf.String()
		assert.Contains(t, output, "UNIT")
		assert.Contains(t, output, "PROTOCOL")
		assert.Contains(t, output, "udp,tcp")
		assert.Contains(t, output, "running")
		assert.Contains(t, output, "stopped")
		assert.Contains(t, output, "42s")

		corednsIdx := strings.Index(output, "coredns.service")
		llamaIdx := strings.Index(output, "llama@gemma.service")
		webIdx := strings.Index(output, "web.service")
		assert.Greater(t, llamaIdx, corednsIdx)
		assert.Greater(t, webIdx, llamaIdx)
	})

	t.Run("active connection shows dash for idle", func(t *testing.T) {
		statuses := []socketactivation.Status{{
			Unit:              "app.service",
			Listen:            "127.0.0.1:1000",
			Protocol:          []string{"tcp"},
			Backend:           "127.0.0.1:2000",
			Running:           true,
			ActiveConnections: 1,
			IdleSeconds:       0,
		}}

		buf := new(bytes.Buffer)
		require.NoError(t, PrintSocketStatuses(buf, statuses))

		// The idle column is a dash while a connection is active.
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		require.Len(t, lines, 2)
		assert.True(t, strings.HasSuffix(lines[1], "-"))
	})

	t.Run("empty list", func(t *testing.T) {
		buf := new(bytes.Buffer)
		require.NoError(t, PrintSocketStatuses(buf, []socketactivation.Status{}))

		assert.Contains(t, buf.String(), "UNIT")
	})
}

func TestSocketsCmd(t *testing.T) {
	t.Run("lists listeners", func(t *testing.T) {
		statuses := []socketactivation.Status{{
			Name:     "coredns",
			Unit:     "coredns.service",
			Listen:   "127.0.0.1:53",
			Protocol: []string{"udp", "tcp"},
			Backend:  "127.0.0.1:5353",
			Running:  false,
		}}

		socketPath := startMockDaemon(t, func(req daemon.Request) daemon.Response {
			assert.Equal(t, "sockets", req.Command)

			return daemon.Response{Success: true, Data: statuses}
		})

		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", socketPath, "sockets"})

		require.NoError(t, root.Execute())

		output := buf.String()
		assert.Contains(t, output, "coredns.service")
		assert.Contains(t, output, "udp,tcp")
	})

	t.Run("error response", func(t *testing.T) {
		socketPath := startMockDaemon(t, func(_ daemon.Request) daemon.Response {
			return daemon.Response{Success: false, Error: "boom"}
		})

		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", socketPath, "sockets"})

		err := root.Execute()
		assert.Error(t, err)
	})

	t.Run("connection failure", func(t *testing.T) {
		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", "/tmp/nonexistent-cli-sockets-test.sock", "sockets"})

		err := root.Execute()
		assert.Error(t, err)
	})
}

func TestStatusCmd(t *testing.T) {
	t.Run("all units", func(t *testing.T) {
		healthy := true
		statuses := []statemanager.UnitStatus{
			{
				UnitName:     "nginx.service",
				ActiveState:  systemd.ActiveStateActive,
				SubState:     systemd.SubStateRunning,
				Healthy:      &healthy,
				RestartCount: 0,
			},
			{
				UnitName:     "app.service",
				ActiveState:  systemd.ActiveStateActive,
				SubState:     systemd.SubStateRunning,
				RestartCount: 1,
			},
		}

		socketPath := startMockDaemon(t, func(req daemon.Request) daemon.Response {
			assert.Equal(t, "status", req.Command)
			assert.Empty(t, req.UnitName)

			return daemon.Response{Success: true, Data: statuses}
		})

		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", socketPath, "status"})

		require.NoError(t, root.Execute())

		output := buf.String()
		assert.Contains(t, output, "nginx.service")
		assert.Contains(t, output, "app.service")
		assert.Contains(t, output, "UNIT")
	})

	t.Run("single unit", func(t *testing.T) {
		healthy := true
		status := statemanager.UnitStatus{
			UnitName:       "nginx.service",
			ActiveState:    systemd.ActiveStateActive,
			SubState:       systemd.SubStateRunning,
			Healthy:        &healthy,
			RestartCount:   2,
			LastTransition: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		}

		socketPath := startMockDaemon(t, func(req daemon.Request) daemon.Response {
			assert.Equal(t, "status", req.Command)
			assert.Equal(t, "nginx.service", req.UnitName)

			return daemon.Response{Success: true, Data: status}
		})

		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", socketPath, "status", "nginx.service"})

		require.NoError(t, root.Execute())

		output := buf.String()
		assert.Contains(t, output, "nginx.service")
		assert.Contains(t, output, "active (running)")
		assert.Contains(t, output, "true")
	})

	t.Run("error response", func(t *testing.T) {
		socketPath := startMockDaemon(t, func(_ daemon.Request) daemon.Response {
			return daemon.Response{Success: false, Error: "unit not found"}
		})

		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", socketPath, "status", "unknown.service"})

		err := root.Execute()
		assert.Error(t, err)
	})

	t.Run("connection failure", func(t *testing.T) {
		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", "/tmp/nonexistent-cli-test.sock", "status"})

		err := root.Execute()
		assert.Error(t, err)
	})
}

func TestStartCmd(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		socketPath := startMockDaemon(t, func(req daemon.Request) daemon.Response {
			assert.Equal(t, "start", req.Command)
			assert.Equal(t, "nginx.service", req.UnitName)

			return daemon.Response{Success: true}
		})

		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", socketPath, "start", "nginx.service"})

		require.NoError(t, root.Execute())
	})

	t.Run("error response", func(t *testing.T) {
		socketPath := startMockDaemon(t, func(_ daemon.Request) daemon.Response {
			return daemon.Response{Success: false, Error: "unit not found"}
		})

		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", socketPath, "start", "unknown.service"})

		err := root.Execute()
		assert.Error(t, err)
	})

	t.Run("connection failure", func(t *testing.T) {
		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--socket", "/tmp/nonexistent-cli-test.sock", "start", "nginx.service"})

		err := root.Execute()
		assert.Error(t, err)
	})
}

func TestStopAndRestartCmd(t *testing.T) {
	commands := []struct {
		name    string
		command string
		unit    string
	}{
		{"stop", "stop", "app.service"},
		{"restart", "restart", "nginx.service"},
	}

	for _, tc := range commands {
		t.Run(fmt.Sprintf("%s success", tc.name), func(t *testing.T) {
			socketPath := startMockDaemon(t, func(req daemon.Request) daemon.Response {
				assert.Equal(t, tc.command, req.Command)
				assert.Equal(t, tc.unit, req.UnitName)

				return daemon.Response{Success: true}
			})

			root := NewRootCommand()

			buf := new(bytes.Buffer)
			root.SetOut(buf)
			root.SetErr(buf)
			root.SetArgs([]string{"--socket", socketPath, tc.command, tc.unit})

			require.NoError(t, root.Execute())
		})

		t.Run(fmt.Sprintf("%s error response", tc.name), func(t *testing.T) {
			socketPath := startMockDaemon(t, func(_ daemon.Request) daemon.Response {
				return daemon.Response{Success: false, Error: fmt.Sprintf("%s failed", tc.command)}
			})

			root := NewRootCommand()

			buf := new(bytes.Buffer)
			root.SetOut(buf)
			root.SetErr(buf)
			root.SetArgs([]string{"--socket", socketPath, tc.command, tc.unit})

			err := root.Execute()
			assert.Error(t, err)
		})
	}
}

func TestCheckCmd(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")

		configContent := `units:
  test.service:
    enabled: true
`
		require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o644))

		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--config", configPath, "check"})

		require.NoError(t, root.Execute())
	})

	t.Run("invalid config file", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "bad.yaml")

		require.NoError(t, os.WriteFile(configPath, []byte("invalid: yaml: content: ["), 0o644))

		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--config", configPath, "check"})

		err := root.Execute()
		assert.Error(t, err)
	})

	t.Run("missing config file", func(t *testing.T) {
		root := NewRootCommand()

		buf := new(bytes.Buffer)
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{"--config", "/tmp/nonexistent-cli-test-config.yaml", "check"})

		err := root.Execute()
		assert.Error(t, err)
	})
}

func TestSendUnitCommand(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		socketPath := startMockDaemon(t, func(req daemon.Request) daemon.Response {
			assert.Equal(t, "start", req.Command)
			assert.Equal(t, "nginx.service", req.UnitName)

			return daemon.Response{Success: true}
		})

		err := sendUnitCommand(os.Stdout, socketPath, "start", "nginx.service")
		require.NoError(t, err)
	})

	t.Run("error response", func(t *testing.T) {
		socketPath := startMockDaemon(t, func(_ daemon.Request) daemon.Response {
			return daemon.Response{Success: false, Error: "unit not found"}
		})

		err := sendUnitCommand(os.Stdout, socketPath, "start", "unknown.service")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unit not found")
	})

	t.Run("connection failure", func(t *testing.T) {
		err := sendUnitCommand(os.Stdout, "/tmp/nonexistent-cli-test.sock", "start", "nginx.service")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connecting to daemon")
	})
}
