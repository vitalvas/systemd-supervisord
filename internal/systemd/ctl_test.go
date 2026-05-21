package systemd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createFakeSystemctl(t *testing.T, script string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("fake systemctl not supported on windows")
	}

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "systemctl")

	fullScript := fmt.Sprintf("#!/bin/sh\n%s", script)
	require.NoError(t, os.WriteFile(scriptPath, []byte(fullScript), 0o755))

	t.Setenv("PATH", fmt.Sprintf("%s:%s", dir, os.Getenv("PATH")))
}

func TestParseProperties(t *testing.T) {
	t.Run("single property", func(t *testing.T) {
		result := parseProperties("ActiveState=active\n")
		assert.Equal(t, map[string]string{"ActiveState": "active"}, result)
	})

	t.Run("multiple properties", func(t *testing.T) {
		input := "ActiveState=active\nSubState=running\nLoadState=loaded\n"
		result := parseProperties(input)

		assert.Len(t, result, 3)
		assert.Equal(t, "active", result["ActiveState"])
		assert.Equal(t, "running", result["SubState"])
		assert.Equal(t, "loaded", result["LoadState"])
	})

	t.Run("empty input", func(t *testing.T) {
		result := parseProperties("")
		assert.Empty(t, result)
	})

	t.Run("line without equals sign", func(t *testing.T) {
		input := "ActiveState=active\nno-equals-here\nSubState=running\n"
		result := parseProperties(input)

		assert.Len(t, result, 2)
		assert.Equal(t, "active", result["ActiveState"])
		assert.Equal(t, "running", result["SubState"])
	})

	t.Run("value containing equals sign", func(t *testing.T) {
		input := "ExecStart=path=/usr/bin/test arg=value\n"
		result := parseProperties(input)

		assert.Len(t, result, 1)
		assert.Equal(t, "path=/usr/bin/test arg=value", result["ExecStart"])
	})

	t.Run("empty value", func(t *testing.T) {
		input := "Description=\n"
		result := parseProperties(input)

		assert.Len(t, result, 1)
		assert.Equal(t, "", result["Description"])
	})

	t.Run("no trailing newline", func(t *testing.T) {
		input := "ActiveState=active"
		result := parseProperties(input)

		assert.Len(t, result, 1)
		assert.Equal(t, "active", result["ActiveState"])
	})

	t.Run("blank lines ignored", func(t *testing.T) {
		input := "ActiveState=active\n\nSubState=running\n"
		result := parseProperties(input)

		assert.Len(t, result, 2)
		assert.Equal(t, "active", result["ActiveState"])
		assert.Equal(t, "running", result["SubState"])
	})

	t.Run("duplicate keys last wins", func(t *testing.T) {
		input := "Key=first\nKey=second\n"
		result := parseProperties(input)

		assert.Len(t, result, 1)
		assert.Equal(t, "second", result["Key"])
	})
}

func TestNewCtlManager(t *testing.T) {
	t.Run("returns non-nil manager", func(t *testing.T) {
		mgr := NewCtlManager()
		require.NotNil(t, mgr)
	})

	t.Run("poll interval is 2 seconds", func(t *testing.T) {
		mgr := NewCtlManager()
		assert.Equal(t, 2*time.Second, mgr.pollInterval)
	})

	t.Run("implements Manager interface", func(_ *testing.T) {
		mgr := NewCtlManager()
		var _ Manager = mgr
	})

	t.Run("close returns nil", func(t *testing.T) {
		mgr := NewCtlManager()
		err := mgr.Close()
		assert.NoError(t, err)
	})
}

func TestCtlManager_Start(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		createFakeSystemctl(t, `exit 0`)

		mgr := NewCtlManager()
		err := mgr.Start(context.Background(), "test.service")
		assert.NoError(t, err)
	})

	t.Run("failure", func(t *testing.T) {
		createFakeSystemctl(t, `echo "Failed to start test.service" >&2; exit 1`)

		mgr := NewCtlManager()
		err := mgr.Start(context.Background(), "test.service")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "systemctl start test.service")
	})

	t.Run("context cancelled", func(t *testing.T) {
		createFakeSystemctl(t, `sleep 10; exit 0`)

		mgr := NewCtlManager()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := mgr.Start(ctx, "test.service")
		assert.Error(t, err)
	})
}

func TestCtlManager_Stop(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		createFakeSystemctl(t, `exit 0`)

		mgr := NewCtlManager()
		err := mgr.Stop(context.Background(), "test.service")
		assert.NoError(t, err)
	})

	t.Run("failure", func(t *testing.T) {
		createFakeSystemctl(t, `echo "Failed to stop" >&2; exit 1`)

		mgr := NewCtlManager()
		err := mgr.Stop(context.Background(), "test.service")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "systemctl stop test.service")
	})
}

func TestCtlManager_Restart(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		createFakeSystemctl(t, `exit 0`)

		mgr := NewCtlManager()
		err := mgr.Restart(context.Background(), "test.service")
		assert.NoError(t, err)
	})

	t.Run("failure", func(t *testing.T) {
		createFakeSystemctl(t, `echo "Failed to restart" >&2; exit 1`)

		mgr := NewCtlManager()
		err := mgr.Restart(context.Background(), "test.service")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "systemctl restart test.service")
	})
}

func TestCtlManager_GetUnitState(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		createFakeSystemctl(t, `
echo "ActiveState=active"
echo "SubState=running"
echo "LoadState=loaded"
exit 0
`)

		mgr := NewCtlManager()
		state, err := mgr.GetUnitState(context.Background(), "test.service")
		require.NoError(t, err)
		assert.Equal(t, "test.service", state.Name)
		assert.Equal(t, ActiveStateActive, state.ActiveState)
		assert.Equal(t, SubStateRunning, state.SubState)
		assert.Equal(t, "loaded", state.LoadState)
	})

	t.Run("inactive unit", func(t *testing.T) {
		createFakeSystemctl(t, `
echo "ActiveState=inactive"
echo "SubState=dead"
echo "LoadState=loaded"
exit 0
`)

		mgr := NewCtlManager()
		state, err := mgr.GetUnitState(context.Background(), "stopped.service")
		require.NoError(t, err)
		assert.Equal(t, ActiveStateInactive, state.ActiveState)
		assert.Equal(t, SubStateDead, state.SubState)
	})

	t.Run("failure", func(t *testing.T) {
		createFakeSystemctl(t, `exit 1`)

		mgr := NewCtlManager()
		state, err := mgr.GetUnitState(context.Background(), "test.service")
		assert.Error(t, err)
		assert.Nil(t, state)
		assert.Contains(t, err.Error(), "running systemctl show for test.service")
	})

	t.Run("context cancelled", func(t *testing.T) {
		createFakeSystemctl(t, `sleep 10; exit 0`)

		mgr := NewCtlManager()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		state, err := mgr.GetUnitState(ctx, "test.service")
		assert.Error(t, err)
		assert.Nil(t, state)
	})
}

func TestCtlManager_GetTimerLastTrigger(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		createFakeSystemctl(t, `echo "LastTriggerUSec=Thu 2026-03-19 10:00:00 UTC"`)

		mgr := NewCtlManager()
		result, err := mgr.GetTimerLastTrigger(context.Background(), "backup.timer")
		require.NoError(t, err)
		assert.Equal(t, 2026, result.Year())
		assert.Equal(t, time.March, result.Month())
		assert.Equal(t, 19, result.Day())
	})

	t.Run("never triggered", func(t *testing.T) {
		createFakeSystemctl(t, `echo "LastTriggerUSec=n/a"`)

		mgr := NewCtlManager()
		result, err := mgr.GetTimerLastTrigger(context.Background(), "backup.timer")
		require.NoError(t, err)
		assert.True(t, result.IsZero())
	})

	t.Run("empty value", func(t *testing.T) {
		createFakeSystemctl(t, `echo "LastTriggerUSec="`)

		mgr := NewCtlManager()
		result, err := mgr.GetTimerLastTrigger(context.Background(), "backup.timer")
		require.NoError(t, err)
		assert.True(t, result.IsZero())
	})

	t.Run("command failure", func(t *testing.T) {
		createFakeSystemctl(t, `exit 1`)

		mgr := NewCtlManager()
		result, err := mgr.GetTimerLastTrigger(context.Background(), "backup.timer")
		assert.Error(t, err)
		assert.True(t, result.IsZero())
		assert.Contains(t, err.Error(), "getting timer properties for backup.timer")
	})

	t.Run("invalid timestamp format", func(t *testing.T) {
		createFakeSystemctl(t, `echo "LastTriggerUSec=invalid-date"`)

		mgr := NewCtlManager()
		result, err := mgr.GetTimerLastTrigger(context.Background(), "backup.timer")
		assert.Error(t, err)
		assert.True(t, result.IsZero())
		assert.Contains(t, err.Error(), "parsing LastTriggerUSec")
	})
}

func TestCtlManager_ListUnits(t *testing.T) {
	t.Run("success with units", func(t *testing.T) {
		createFakeSystemctl(t, `
echo "myapp-web.service      loaded active running  Web server"
echo "myapp-worker.service   loaded active running  Worker"
echo "myapp-cleanup.timer    loaded active waiting  Cleanup timer"
exit 0
`)

		mgr := NewCtlManager()
		units, err := mgr.ListUnits(context.Background(), "myapp-")
		require.NoError(t, err)
		assert.Equal(t, []string{"myapp-web.service", "myapp-worker.service", "myapp-cleanup.timer"}, units)
	})

	t.Run("empty result", func(t *testing.T) {
		createFakeSystemctl(t, `exit 0`)

		mgr := NewCtlManager()
		units, err := mgr.ListUnits(context.Background(), "nonexistent-")
		require.NoError(t, err)
		assert.Empty(t, units)
	})

	t.Run("failure", func(t *testing.T) {
		createFakeSystemctl(t, `exit 1`)

		mgr := NewCtlManager()
		units, err := mgr.ListUnits(context.Background(), "test-")
		assert.Error(t, err)
		assert.Nil(t, units)
		assert.Contains(t, err.Error(), "listing units with prefix test-")
	})

	t.Run("single unit", func(t *testing.T) {
		createFakeSystemctl(t, `echo "myapp.service loaded active running My App"`)

		mgr := NewCtlManager()
		units, err := mgr.ListUnits(context.Background(), "myapp")
		require.NoError(t, err)
		assert.Equal(t, []string{"myapp.service"}, units)
	})
}

func TestCtlManager_WatchUnit(t *testing.T) {
	t.Run("detects state change", func(t *testing.T) {
		dir := t.TempDir()
		stateFile := filepath.Join(dir, "state")

		createFakeSystemctl(t, fmt.Sprintf(`
STATEFILE="%s"
if [ "$1" = "show" ]; then
    if [ -f "$STATEFILE" ]; then
        echo "ActiveState=failed"
        echo "SubState=failed"
        echo "LoadState=loaded"
    else
        touch "$STATEFILE"
        echo "ActiveState=active"
        echo "SubState=running"
        echo "LoadState=loaded"
    fi
fi
exit 0
`, stateFile))

		mgr := NewCtlManager()
		mgr.pollInterval = 50 * time.Millisecond

		ch := make(chan StateChange, 10)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		err := mgr.WatchUnit(ctx, "test.service", ch)
		require.NoError(t, err)

		// First state change: active/running
		change1 := <-ch
		assert.Equal(t, "test.service", change1.UnitName)
		assert.Equal(t, ActiveStateActive, change1.ActiveState)
		assert.Equal(t, SubStateRunning, change1.SubState)

		// Second state change: failed/failed
		change2 := <-ch
		assert.Equal(t, "test.service", change2.UnitName)
		assert.Equal(t, ActiveStateFailed, change2.ActiveState)
		assert.Equal(t, SubStateFailed, change2.SubState)

		cancel()
	})

	t.Run("no duplicate events for same state", func(t *testing.T) {
		createFakeSystemctl(t, `
if [ "$1" = "show" ]; then
    echo "ActiveState=active"
    echo "SubState=running"
    echo "LoadState=loaded"
fi
exit 0
`)

		mgr := NewCtlManager()
		mgr.pollInterval = 50 * time.Millisecond

		ch := make(chan StateChange, 10)
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		err := mgr.WatchUnit(ctx, "test.service", ch)
		require.NoError(t, err)

		// Should get exactly one event (first detection)
		select {
		case change := <-ch:
			assert.Equal(t, ActiveStateActive, change.ActiveState)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for first state change")
		}

		// Wait for context to expire, should not get more events
		<-ctx.Done()
		time.Sleep(100 * time.Millisecond)

		// Drain channel - should have no more events
		remaining := 0
		for {
			select {
			case <-ch:
				remaining++
			default:
				goto done
			}
		}
	done:
		assert.Equal(t, 0, remaining, "should not send duplicate state events")
	})

	t.Run("context cancellation stops goroutine", func(t *testing.T) {
		createFakeSystemctl(t, `
if [ "$1" = "show" ]; then
    echo "ActiveState=active"
    echo "SubState=running"
    echo "LoadState=loaded"
fi
exit 0
`)

		mgr := NewCtlManager()
		mgr.pollInterval = 50 * time.Millisecond

		ch := make(chan StateChange, 10)
		ctx, cancel := context.WithCancel(context.Background())

		err := mgr.WatchUnit(ctx, "test.service", ch)
		require.NoError(t, err)

		// Read initial event
		<-ch

		// Cancel context to stop goroutine
		cancel()

		// Allow goroutine to finish
		time.Sleep(100 * time.Millisecond)
	})

	t.Run("handles GetUnitState errors gracefully", func(t *testing.T) {
		createFakeSystemctl(t, `exit 1`)

		mgr := NewCtlManager()
		mgr.pollInterval = 50 * time.Millisecond

		ch := make(chan StateChange, 10)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		err := mgr.WatchUnit(ctx, "test.service", ch)
		require.NoError(t, err)

		// Wait for context to expire
		<-ctx.Done()

		// Channel should be empty since all polls failed
		select {
		case <-ch:
			t.Fatal("should not have received any state changes when all polls fail")
		default:
			// Expected
		}
	})
}

func TestCtlManager_RunSystemctl(t *testing.T) {
	t.Run("error includes output", func(t *testing.T) {
		createFakeSystemctl(t, `echo "Unit test.service not found."; exit 1`)

		mgr := NewCtlManager()
		err := mgr.runSystemctl(context.Background(), "start", "test.service")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Unit test.service not found.")
	})

	t.Run("success returns nil", func(t *testing.T) {
		createFakeSystemctl(t, `exit 0`)

		mgr := NewCtlManager()
		err := mgr.runSystemctl(context.Background(), "start", "test.service")
		assert.NoError(t, err)
	})
}
