package httphealth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vitalvas/systemd-supervisord/internal/config"
	"github.com/vitalvas/systemd-supervisord/internal/statemanager"
	"github.com/vitalvas/systemd-supervisord/internal/systemd"
)

type stubStatus struct {
	mu       sync.Mutex
	statuses map[string]*statemanager.UnitStatus
}

func newStubStatus() *stubStatus {
	return &stubStatus{statuses: make(map[string]*statemanager.UnitStatus)}
}

func (s *stubStatus) set(unit string, status *statemanager.UnitStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.statuses[unit] = status
}

func (s *stubStatus) GetStatus(unit string) *statemanager.UnitStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, ok := s.statuses[unit]
	if !ok {
		return nil
	}

	cp := *st

	return &cp
}

type stubCritical struct {
	units []string
}

func (s *stubCritical) CriticalUnits() []string {
	return append([]string(nil), s.units...)
}

func boolPtr(b bool) *bool { return &b }

func newTestServer(status StatusProvider, critical CriticalProvider) *Server {
	return New(config.HTTPConfig{
		Listen:          "127.0.0.1:0",
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
		ShutdownTimeout: time.Second,
	}, status, critical)
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) healthResponse {
	t.Helper()

	var resp healthResponse

	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	return resp
}

func TestHandleLive(t *testing.T) {
	t.Run("always returns ok", func(t *testing.T) {
		srv := newTestServer(newStubStatus(), &stubCritical{})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/live", nil)
		srv.handleLive(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)

		resp := decodeBody(t, rec)
		assert.Equal(t, "ok", resp.Status)
	})

	t.Run("rejects POST", func(t *testing.T) {
		srv := newTestServer(newStubStatus(), &stubCritical{})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/live", nil)
		srv.handleLive(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
	})
}

func TestHandleReady(t *testing.T) {
	t.Run("503 before MarkReady", func(t *testing.T) {
		srv := newTestServer(newStubStatus(), &stubCritical{})

		rec := httptest.NewRecorder()
		srv.handleReady(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

		resp := decodeBody(t, rec)
		assert.False(t, resp.Ready)
		assert.Equal(t, "unhealthy", resp.Status)
	})

	t.Run("200 after MarkReady", func(t *testing.T) {
		srv := newTestServer(newStubStatus(), &stubCritical{})
		srv.MarkReady()

		rec := httptest.NewRecorder()
		srv.handleReady(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

		assert.Equal(t, http.StatusOK, rec.Code)

		resp := decodeBody(t, rec)
		assert.True(t, resp.Ready)
		assert.Equal(t, "ok", resp.Status)
	})
}

func TestHandleHealth(t *testing.T) {
	t.Run("no critical units returns 200", func(t *testing.T) {
		srv := newTestServer(newStubStatus(), &stubCritical{})
		srv.MarkReady()

		rec := httptest.NewRecorder()
		srv.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		assert.Equal(t, http.StatusOK, rec.Code)

		resp := decodeBody(t, rec)
		assert.Equal(t, "ok", resp.Status)
		assert.Empty(t, resp.Units)
	})

	t.Run("all critical units healthy returns 200", func(t *testing.T) {
		status := newStubStatus()
		status.set("app.service", &statemanager.UnitStatus{
			UnitName:    "app.service",
			ActiveState: systemd.ActiveStateActive,
			SubState:    systemd.SubStateRunning,
			Healthy:     boolPtr(true),
		})

		critical := &stubCritical{units: []string{"app.service"}}
		srv := newTestServer(status, critical)

		rec := httptest.NewRecorder()
		srv.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		assert.Equal(t, http.StatusOK, rec.Code)

		resp := decodeBody(t, rec)
		assert.Equal(t, "ok", resp.Status)
		require.Len(t, resp.Units, 1)
		assert.Equal(t, "app.service", resp.Units[0].UnitName)
	})

	t.Run("active unit without health check is healthy", func(t *testing.T) {
		status := newStubStatus()
		status.set("db.service", &statemanager.UnitStatus{
			UnitName:    "db.service",
			ActiveState: systemd.ActiveStateActive,
			SubState:    systemd.SubStateRunning,
		})

		srv := newTestServer(status, &stubCritical{units: []string{"db.service"}})

		rec := httptest.NewRecorder()
		srv.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("inactive critical unit returns 503", func(t *testing.T) {
		status := newStubStatus()
		status.set("app.service", &statemanager.UnitStatus{
			UnitName:    "app.service",
			ActiveState: systemd.ActiveStateFailed,
			SubState:    systemd.SubStateFailed,
		})

		srv := newTestServer(status, &stubCritical{units: []string{"app.service"}})

		rec := httptest.NewRecorder()
		srv.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

		resp := decodeBody(t, rec)
		assert.Equal(t, "unhealthy", resp.Status)
	})

	t.Run("unhealthy critical unit returns 503", func(t *testing.T) {
		status := newStubStatus()
		status.set("app.service", &statemanager.UnitStatus{
			UnitName:    "app.service",
			ActiveState: systemd.ActiveStateActive,
			SubState:    systemd.SubStateRunning,
			Healthy:     boolPtr(false),
		})

		srv := newTestServer(status, &stubCritical{units: []string{"app.service"}})

		rec := httptest.NewRecorder()
		srv.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("missing critical unit returns 503", func(t *testing.T) {
		srv := newTestServer(newStubStatus(), &stubCritical{units: []string{"absent.service"}})

		rec := httptest.NewRecorder()
		srv.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

		resp := decodeBody(t, rec)
		require.Len(t, resp.Units, 1)
		assert.Equal(t, "absent.service", resp.Units[0].UnitName)
		assert.Empty(t, resp.Units[0].ActiveState)
	})

	t.Run("mixed healthy and unhealthy returns 503", func(t *testing.T) {
		status := newStubStatus()
		status.set("a.service", &statemanager.UnitStatus{
			UnitName:    "a.service",
			ActiveState: systemd.ActiveStateActive,
			SubState:    systemd.SubStateRunning,
			Healthy:     boolPtr(true),
		})
		status.set("b.service", &statemanager.UnitStatus{
			UnitName:    "b.service",
			ActiveState: systemd.ActiveStateActive,
			SubState:    systemd.SubStateRunning,
			Healthy:     boolPtr(false),
		})

		srv := newTestServer(status, &stubCritical{units: []string{"a.service", "b.service"}})

		rec := httptest.NewRecorder()
		srv.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("units returned in sorted order", func(t *testing.T) {
		status := newStubStatus()

		for _, name := range []string{"c.service", "a.service", "b.service"} {
			status.set(name, &statemanager.UnitStatus{
				UnitName:    name,
				ActiveState: systemd.ActiveStateActive,
				SubState:    systemd.SubStateRunning,
				Healthy:     boolPtr(true),
			})
		}

		srv := newTestServer(status, &stubCritical{units: []string{"c.service", "a.service", "b.service"}})

		rec := httptest.NewRecorder()
		srv.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		resp := decodeBody(t, rec)
		require.Len(t, resp.Units, 3)
		assert.Equal(t, "a.service", resp.Units[0].UnitName)
		assert.Equal(t, "b.service", resp.Units[1].UnitName)
		assert.Equal(t, "c.service", resp.Units[2].UnitName)
	})

	t.Run("rejects POST", func(t *testing.T) {
		srv := newTestServer(newStubStatus(), &stubCritical{})

		rec := httptest.NewRecorder()
		srv.handleHealth(rec, httptest.NewRequest(http.MethodPost, "/health", nil))

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
}

func TestServerStartStop(t *testing.T) {
	srv := New(config.HTTPConfig{
		Listen:          "127.0.0.1:0",
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
		ShutdownTimeout: time.Second,
	}, newStubStatus(), &stubCritical{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, srv.Start(ctx))

	addr := srv.listener.Addr().String()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()

			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/live", addr))
	require.NoError(t, err)

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.Contains(string(body), `"status":"ok"`))

	cancel()

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, err := http.Get(fmt.Sprintf("http://%s/live", addr))
		if err != nil {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("server did not shut down")
}

func TestServerStartListenError(t *testing.T) {
	srv := New(config.HTTPConfig{
		Listen:          "127.0.0.1:not-a-port",
		ShutdownTimeout: time.Second,
	}, newStubStatus(), &stubCritical{})

	err := srv.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listening on")
}
