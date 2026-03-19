package notify

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vitalvas/systemd-supervisord/internal/config"
	"github.com/vitalvas/systemd-supervisord/internal/statemanager"
)

func TestNotifier(t *testing.T) {
	t.Run("webhook sends correct payload", func(t *testing.T) {
		receivedCh := make(chan EventPayload, 1)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			var payload EventPayload

			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))

			w.WriteHeader(http.StatusOK)

			receivedCh <- payload
		}))
		defer srv.Close()

		n := New(config.NotifyConfig{
			Webhooks: []config.WebhookConfig{
				{URL: srv.URL, Timeout: 5 * time.Second},
			},
		})

		ts := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "nginx.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   ts,
		})

		select {
		case received := <-receivedCh:
			assert.Equal(t, "state_changed", received.EventType)
			assert.Equal(t, "nginx.service", received.UnitName)
			assert.Equal(t, "failed", received.ActiveState)
			assert.Equal(t, "2026-01-15T10:00:00Z", received.Timestamp)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for webhook")
		}
	})

	t.Run("webhook includes variables", func(t *testing.T) {
		receivedCh := make(chan EventPayload, 1)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload EventPayload

			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))

			w.WriteHeader(http.StatusOK)

			receivedCh <- payload
		}))
		defer srv.Close()

		n := New(config.NotifyConfig{
			Variables: map[string]string{
				"hostname":    "web-01",
				"environment": "production",
			},
			Webhooks: []config.WebhookConfig{
				{URL: srv.URL, Timeout: 5 * time.Second},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "nginx.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Now(),
		})

		select {
		case received := <-receivedCh:
			assert.Equal(t, "state_changed", received.EventType)
			require.NotNil(t, received.Variables)
			assert.Equal(t, "web-01", received.Variables["hostname"])
			assert.Equal(t, "production", received.Variables["environment"])
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for webhook")
		}
	})

	t.Run("webhook health changed event", func(t *testing.T) {
		receivedCh := make(chan EventPayload, 1)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload EventPayload

			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))

			w.WriteHeader(http.StatusOK)

			receivedCh <- payload
		}))
		defer srv.Close()

		n := New(config.NotifyConfig{
			Webhooks: []config.WebhookConfig{
				{URL: srv.URL, Timeout: 5 * time.Second},
			},
		})

		healthy := false

		n.HandleEvent(statemanager.Event{
			Type:      statemanager.EventHealthChanged,
			UnitName:  "app.service",
			Healthy:   &healthy,
			Timestamp: time.Now(),
		})

		select {
		case received := <-receivedCh:
			assert.Equal(t, "health_changed", received.EventType)
			assert.Equal(t, "app.service", received.UnitName)
			require.NotNil(t, received.Healthy)
			assert.False(t, *received.Healthy)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for webhook")
		}
	})

	t.Run("webhook filtered to state_changed only", func(t *testing.T) {
		receivedCh := make(chan EventPayload, 1)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload EventPayload

			_ = json.NewDecoder(r.Body).Decode(&payload)

			w.WriteHeader(http.StatusOK)

			receivedCh <- payload
		}))
		defer srv.Close()

		n := New(config.NotifyConfig{
			Webhooks: []config.WebhookConfig{
				{URL: srv.URL, Timeout: 5 * time.Second, Events: []string{"state_changed"}},
			},
		})

		healthy := false
		n.HandleEvent(statemanager.Event{
			Type:      statemanager.EventHealthChanged,
			UnitName:  "app.service",
			Healthy:   &healthy,
			Timestamp: time.Now(),
		})

		time.Sleep(300 * time.Millisecond)

		select {
		case <-receivedCh:
			t.Fatal("webhook should not have been called for health_changed")
		default:
		}

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "active",
			SubState:    "running",
			Timestamp:   time.Now(),
		})

		select {
		case received := <-receivedCh:
			assert.Equal(t, "state_changed", received.EventType)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for webhook")
		}
	})

	t.Run("webhook filtered to health_changed only", func(t *testing.T) {
		receivedCh := make(chan EventPayload, 1)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload EventPayload

			_ = json.NewDecoder(r.Body).Decode(&payload)

			w.WriteHeader(http.StatusOK)

			receivedCh <- payload
		}))
		defer srv.Close()

		n := New(config.NotifyConfig{
			Webhooks: []config.WebhookConfig{
				{URL: srv.URL, Timeout: 5 * time.Second, Events: []string{"health_changed"}},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Now(),
		})

		time.Sleep(300 * time.Millisecond)

		select {
		case <-receivedCh:
			t.Fatal("webhook should not have been called for state_changed")
		default:
		}

		healthy := true
		n.HandleEvent(statemanager.Event{
			Type:      statemanager.EventHealthChanged,
			UnitName:  "app.service",
			Healthy:   &healthy,
			Timestamp: time.Now(),
		})

		select {
		case received := <-receivedCh:
			assert.Equal(t, "health_changed", received.EventType)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for webhook")
		}
	})

	t.Run("empty events filter sends all", func(t *testing.T) {
		receivedCh := make(chan EventPayload, 2)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload EventPayload

			_ = json.NewDecoder(r.Body).Decode(&payload)

			w.WriteHeader(http.StatusOK)

			receivedCh <- payload
		}))
		defer srv.Close()

		n := New(config.NotifyConfig{
			Webhooks: []config.WebhookConfig{
				{URL: srv.URL, Timeout: 5 * time.Second},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "active",
			SubState:    "running",
			Timestamp:   time.Now(),
		})

		healthy := true
		n.HandleEvent(statemanager.Event{
			Type:      statemanager.EventHealthChanged,
			UnitName:  "app.service",
			Healthy:   &healthy,
			Timestamp: time.Now(),
		})

		for i := 0; i < 2; i++ {
			select {
			case <-receivedCh:
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out waiting for event %d", i+1)
			}
		}
	})

	t.Run("webhook non-success status code", func(t *testing.T) {
		doneCh := make(chan struct{}, 1)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			doneCh <- struct{}{}
		}))
		defer srv.Close()

		n := New(config.NotifyConfig{
			Webhooks: []config.WebhookConfig{
				{URL: srv.URL, Timeout: 5 * time.Second},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Now(),
		})

		select {
		case <-doneCh:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for webhook call")
		}
	})

	t.Run("webhook with unreachable URL", func(_ *testing.T) {
		n := New(config.NotifyConfig{
			Webhooks: []config.WebhookConfig{
				{URL: "http://127.0.0.1:1", Timeout: 1 * time.Second},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Now(),
		})

		time.Sleep(2 * time.Second)
	})

	t.Run("webhook with default timeout", func(t *testing.T) {
		receivedCh := make(chan struct{}, 1)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			receivedCh <- struct{}{}
		}))
		defer srv.Close()

		n := New(config.NotifyConfig{
			Webhooks: []config.WebhookConfig{
				{URL: srv.URL, Timeout: 0},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Now(),
		})

		select {
		case <-receivedCh:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for webhook")
		}
	})

}

func TestNotifierScript(t *testing.T) {
	t.Run("script receives env variables", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("script test requires unix")
		}

		dir := t.TempDir()
		outFile := filepath.Join(dir, "output.txt")
		scriptPath := filepath.Join(dir, "notify.sh")

		script := fmt.Sprintf("#!/bin/sh\nenv | grep SUPERVISORD_ > %s\n", outFile)

		require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

		n := New(config.NotifyConfig{
			Scripts: []config.ScriptConfig{
				{Path: scriptPath, Timeout: 5 * time.Second},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "test.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		})

		require.Eventually(t, func() bool {
			data, err := os.ReadFile(outFile)
			return err == nil && len(data) > 0
		}, 5*time.Second, 100*time.Millisecond, "output file should be created by script")

		data, err := os.ReadFile(outFile)
		require.NoError(t, err)

		output := string(data)
		assert.Contains(t, output, "SUPERVISORD_EVENT_TYPE=state_changed")
		assert.Contains(t, output, "SUPERVISORD_UNIT_NAME=test.service")
		assert.Contains(t, output, "SUPERVISORD_ACTIVE_STATE=failed")
	})

	t.Run("script receives custom variables as env vars", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("script test requires unix")
		}

		dir := t.TempDir()
		outFile := filepath.Join(dir, "output.txt")
		scriptPath := filepath.Join(dir, "notify.sh")

		script := fmt.Sprintf("#!/bin/sh\nenv | grep SUPERVISORD_ > %s\n", outFile)

		require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

		n := New(config.NotifyConfig{
			Variables: map[string]string{
				"hostname":    "web-01",
				"environment": "production",
			},
			Scripts: []config.ScriptConfig{
				{Path: scriptPath, Timeout: 5 * time.Second},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "test.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Now(),
		})

		require.Eventually(t, func() bool {
			data, err := os.ReadFile(outFile)
			return err == nil && len(data) > 0
		}, 5*time.Second, 100*time.Millisecond)

		data, err := os.ReadFile(outFile)
		require.NoError(t, err)

		output := string(data)
		assert.Contains(t, output, "SUPERVISORD_VAR_HOSTNAME=web-01")
		assert.Contains(t, output, "SUPERVISORD_VAR_ENVIRONMENT=production")
	})

	t.Run("script with health_changed event sets healthy env var", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("script test requires unix")
		}

		dir := t.TempDir()
		outFile := filepath.Join(dir, "output.txt")
		scriptPath := filepath.Join(dir, "notify.sh")

		script := fmt.Sprintf("#!/bin/sh\nenv | grep SUPERVISORD_ > %s\n", outFile)

		require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

		n := New(config.NotifyConfig{
			Scripts: []config.ScriptConfig{
				{Path: scriptPath, Timeout: 5 * time.Second},
			},
		})

		healthy := true
		n.HandleEvent(statemanager.Event{
			Type:      statemanager.EventHealthChanged,
			UnitName:  "app.service",
			Healthy:   &healthy,
			Timestamp: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		})

		require.Eventually(t, func() bool {
			data, err := os.ReadFile(outFile)
			return err == nil && len(data) > 0
		}, 5*time.Second, 100*time.Millisecond)

		data, err := os.ReadFile(outFile)
		require.NoError(t, err)

		output := string(data)
		assert.Contains(t, output, "SUPERVISORD_EVENT_TYPE=health_changed")
		assert.Contains(t, output, "SUPERVISORD_UNIT_NAME=app.service")
		assert.Contains(t, output, "SUPERVISORD_HEALTHY=true")
	})

	t.Run("script that exits with error", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("script test requires unix")
		}

		dir := t.TempDir()
		scriptPath := filepath.Join(dir, "fail.sh")

		script := "#!/bin/sh\nexit 1\n"

		require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

		n := New(config.NotifyConfig{
			Scripts: []config.ScriptConfig{
				{Path: scriptPath, Timeout: 5 * time.Second},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Now(),
		})

		time.Sleep(1 * time.Second)
	})

	t.Run("script with default timeout", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("script test requires unix")
		}

		dir := t.TempDir()
		outFile := filepath.Join(dir, "output.txt")
		scriptPath := filepath.Join(dir, "notify.sh")

		script := fmt.Sprintf("#!/bin/sh\necho done > %s\n", outFile)

		require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

		n := New(config.NotifyConfig{
			Scripts: []config.ScriptConfig{
				{Path: scriptPath, Timeout: 0},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Now(),
		})

		require.Eventually(t, func() bool {
			data, err := os.ReadFile(outFile)
			return err == nil && len(data) > 0
		}, 5*time.Second, 100*time.Millisecond)

		data, err := os.ReadFile(outFile)
		require.NoError(t, err)
		assert.Contains(t, string(data), "done")
	})
}

func TestNotifierExec(t *testing.T) {
	t.Run("exec receives env variables", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("exec test requires unix")
		}

		dir := t.TempDir()
		outFile := filepath.Join(dir, "output.txt")

		n := New(config.NotifyConfig{
			Execs: []config.ExecConfig{
				{Command: fmt.Sprintf("env | grep SUPERVISORD_ > %s", outFile), Timeout: 5 * time.Second},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "test.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		})

		require.Eventually(t, func() bool {
			data, err := os.ReadFile(outFile)
			return err == nil && len(data) > 0
		}, 5*time.Second, 100*time.Millisecond)

		data, err := os.ReadFile(outFile)
		require.NoError(t, err)

		output := string(data)
		assert.Contains(t, output, "SUPERVISORD_EVENT_TYPE=state_changed")
		assert.Contains(t, output, "SUPERVISORD_UNIT_NAME=test.service")
		assert.Contains(t, output, "SUPERVISORD_ACTIVE_STATE=failed")
	})

	t.Run("exec receives custom variables as env vars", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("exec test requires unix")
		}

		dir := t.TempDir()
		outFile := filepath.Join(dir, "output.txt")

		n := New(config.NotifyConfig{
			Variables: map[string]string{
				"hostname": "db-01",
			},
			Execs: []config.ExecConfig{
				{Command: fmt.Sprintf("env | grep SUPERVISORD_ > %s", outFile), Timeout: 5 * time.Second},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "test.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Now(),
		})

		require.Eventually(t, func() bool {
			data, err := os.ReadFile(outFile)
			return err == nil && len(data) > 0
		}, 5*time.Second, 100*time.Millisecond)

		data, err := os.ReadFile(outFile)
		require.NoError(t, err)

		output := string(data)
		assert.Contains(t, output, "SUPERVISORD_VAR_HOSTNAME=db-01")
	})

	t.Run("exec with event filter", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("exec test requires unix")
		}

		dir := t.TempDir()
		outFile := filepath.Join(dir, "output.txt")

		n := New(config.NotifyConfig{
			Execs: []config.ExecConfig{
				{
					Command: fmt.Sprintf("echo triggered > %s", outFile),
					Timeout: 5 * time.Second,
					Events:  []string{"health_changed"},
				},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "test.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Now(),
		})

		time.Sleep(500 * time.Millisecond)

		_, err := os.ReadFile(outFile)
		assert.Error(t, err, "file should not exist because state_changed was filtered out")

		healthy := false
		n.HandleEvent(statemanager.Event{
			Type:      statemanager.EventHealthChanged,
			UnitName:  "test.service",
			Healthy:   &healthy,
			Timestamp: time.Now(),
		})

		require.Eventually(t, func() bool {
			data, err := os.ReadFile(outFile)
			return err == nil && len(data) > 0
		}, 5*time.Second, 100*time.Millisecond)

		data, err := os.ReadFile(outFile)
		require.NoError(t, err)
		assert.Contains(t, string(data), "triggered")
	})

	t.Run("exec with default timeout", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("exec test requires unix")
		}

		dir := t.TempDir()
		outFile := filepath.Join(dir, "output.txt")

		n := New(config.NotifyConfig{
			Execs: []config.ExecConfig{
				{Command: fmt.Sprintf("echo done > %s", outFile), Timeout: 0},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Now(),
		})

		require.Eventually(t, func() bool {
			data, err := os.ReadFile(outFile)
			return err == nil && len(data) > 0
		}, 5*time.Second, 100*time.Millisecond)

		data, err := os.ReadFile(outFile)
		require.NoError(t, err)
		assert.Contains(t, string(data), "done")
	})

	t.Run("exec that fails", func(_ *testing.T) {
		n := New(config.NotifyConfig{
			Execs: []config.ExecConfig{
				{Command: "exit 1", Timeout: 5 * time.Second},
			},
		})

		n.HandleEvent(statemanager.Event{
			Type:        statemanager.EventStateChanged,
			UnitName:    "app.service",
			ActiveState: "failed",
			SubState:    "failed",
			Timestamp:   time.Now(),
		})

		time.Sleep(500 * time.Millisecond)
	})
}
