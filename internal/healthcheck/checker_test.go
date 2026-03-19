package healthcheck

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vitalvas/systemd-supervisord/internal/config"
)

func acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}

		conn.Close()
	}
}

func TestChecker(t *testing.T) {
	t.Run("single tcp healthy", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		go acceptLoop(ln)

		resultCh := make(chan Result, 10)

		c := New("test.service", []config.HealthCheck{
			{Type: "tcp", TCP: &config.TCPHealthCheck{Address: ln.Addr().String()}, Interval: 100 * time.Millisecond, Timeout: 200 * time.Millisecond, Retries: 10},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		go c.Run(ctx)

		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)

		assert.Empty(t, resultCh)
	})

	t.Run("single tcp unhealthy", func(t *testing.T) {
		resultCh := make(chan Result, 10)

		c := New("test.service", []config.HealthCheck{
			{Type: "tcp", TCP: &config.TCPHealthCheck{Address: "127.0.0.1:1"}, Interval: 100 * time.Millisecond, Timeout: 100 * time.Millisecond, Retries: 2},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		go c.Run(ctx)

		select {
		case r := <-resultCh:
			assert.False(t, r.Healthy)
			assert.Equal(t, "test.service", r.UnitName)
			assert.Error(t, r.Err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for unhealthy result")
		}
	})

	t.Run("http healthy", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		resultCh := make(chan Result, 10)

		c := New("web.service", []config.HealthCheck{
			{Type: "http", HTTP: &config.HTTPHealthCheck{Address: srv.URL}, Interval: 100 * time.Millisecond, Timeout: 200 * time.Millisecond, Retries: 10},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		go c.Run(ctx)

		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)

		assert.Empty(t, resultCh)
	})

	t.Run("http unhealthy status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		resultCh := make(chan Result, 10)

		c := New("web.service", []config.HealthCheck{
			{Type: "http", HTTP: &config.HTTPHealthCheck{Address: srv.URL}, Interval: 100 * time.Millisecond, Timeout: 1 * time.Second, Retries: 1},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		go c.Run(ctx)

		select {
		case r := <-resultCh:
			assert.False(t, r.Healthy)
			assert.Equal(t, "web.service", r.UnitName)
		case <-ctx.Done():
			t.Fatal("timed out waiting for unhealthy result")
		}
	})

	t.Run("unix socket healthy", func(t *testing.T) {
		dir := t.TempDir()
		sockPath := filepath.Join(dir, "test.sock")

		ln, err := net.Listen("unix", sockPath)
		require.NoError(t, err)
		defer ln.Close()

		go acceptLoop(ln)

		resultCh := make(chan Result, 10)

		c := New("app.service", []config.HealthCheck{
			{Type: "unix", Unix: &config.UnixHealthCheck{Address: sockPath}, Interval: 100 * time.Millisecond, Timeout: 200 * time.Millisecond, Retries: 10},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		go c.Run(ctx)

		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)

		assert.Empty(t, resultCh)
	})

	t.Run("unix socket unhealthy", func(t *testing.T) {
		sockPath := filepath.Join(os.TempDir(), "nonexistent.sock")

		resultCh := make(chan Result, 10)

		c := New("app.service", []config.HealthCheck{
			{Type: "unix", Unix: &config.UnixHealthCheck{Address: sockPath}, Interval: 100 * time.Millisecond, Timeout: 100 * time.Millisecond, Retries: 1},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		go c.Run(ctx)

		select {
		case r := <-resultCh:
			assert.False(t, r.Healthy)
			assert.Equal(t, "app.service", r.UnitName)
		case <-ctx.Done():
			t.Fatal("timed out waiting for unhealthy result")
		}
	})

	t.Run("multiple endpoints all healthy", func(t *testing.T) {
		ln1, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln1.Close()

		ln2, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln2.Close()

		go acceptLoop(ln1)
		go acceptLoop(ln2)

		resultCh := make(chan Result, 10)

		c := New("multi.service", []config.HealthCheck{
			{Type: "tcp", TCP: &config.TCPHealthCheck{Address: ln1.Addr().String()}, Interval: 100 * time.Millisecond, Timeout: 200 * time.Millisecond, Retries: 10},
			{Type: "tcp", TCP: &config.TCPHealthCheck{Address: ln2.Addr().String()}, Interval: 100 * time.Millisecond, Timeout: 200 * time.Millisecond, Retries: 10},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		go c.Run(ctx)

		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)

		assert.Empty(t, resultCh)
	})

	t.Run("multiple endpoints one unhealthy", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		go acceptLoop(ln)

		resultCh := make(chan Result, 10)

		c := New("multi.service", []config.HealthCheck{
			{Type: "tcp", TCP: &config.TCPHealthCheck{Address: ln.Addr().String()}, Interval: 100 * time.Millisecond, Timeout: 100 * time.Millisecond, Retries: 1},
			{Type: "tcp", TCP: &config.TCPHealthCheck{Address: "127.0.0.1:1"}, Interval: 100 * time.Millisecond, Timeout: 100 * time.Millisecond, Retries: 1},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		go c.Run(ctx)

		select {
		case r := <-resultCh:
			assert.False(t, r.Healthy)
			assert.Equal(t, "multi.service", r.UnitName)
			assert.Error(t, r.Err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for unhealthy result")
		}
	})

	t.Run("mixed endpoint types", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		go acceptLoop(ln)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		resultCh := make(chan Result, 10)

		c := New("mixed.service", []config.HealthCheck{
			{Type: "tcp", TCP: &config.TCPHealthCheck{Address: ln.Addr().String()}, Interval: 100 * time.Millisecond, Timeout: 200 * time.Millisecond, Retries: 10},
			{Type: "http", HTTP: &config.HTTPHealthCheck{Address: srv.URL}, Interval: 100 * time.Millisecond, Timeout: 200 * time.Millisecond, Retries: 10},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		go c.Run(ctx)

		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)

		assert.Empty(t, resultCh)
	})

	t.Run("recovery emits healthy", func(t *testing.T) {
		var healthy bool

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		addr := ln.Addr().String()
		ln.Close()

		resultCh := make(chan Result, 10)

		c := New("test.service", []config.HealthCheck{
			{Type: "tcp", TCP: &config.TCPHealthCheck{Address: addr}, Interval: 100 * time.Millisecond, Timeout: 100 * time.Millisecond, Retries: 1},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		go c.Run(ctx)

		select {
		case r := <-resultCh:
			assert.False(t, r.Healthy)
		case <-ctx.Done():
			t.Fatal("timed out waiting for unhealthy result")
		}

		ln, err = net.Listen("tcp", addr)
		require.NoError(t, err)
		defer ln.Close()

		go acceptLoop(ln)

		select {
		case r := <-resultCh:
			healthy = r.Healthy
		case <-ctx.Done():
			t.Fatal("timed out waiting for recovery")
		}

		assert.True(t, healthy)
	})

	t.Run("script healthy", func(t *testing.T) {
		resultCh := make(chan Result, 10)

		c := New("db.service", []config.HealthCheck{
			{Type: "script", Script: &config.ScriptHealthCheck{Command: "true"}, Interval: 100 * time.Millisecond, Timeout: 5 * time.Second, Retries: 10},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		go c.Run(ctx)

		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)

		assert.Empty(t, resultCh)
	})

	t.Run("script unhealthy", func(t *testing.T) {
		resultCh := make(chan Result, 10)

		c := New("db.service", []config.HealthCheck{
			{Type: "script", Script: &config.ScriptHealthCheck{Command: "exit 1"}, Interval: 100 * time.Millisecond, Timeout: 5 * time.Second, Retries: 1},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		go c.Run(ctx)

		select {
		case r := <-resultCh:
			assert.False(t, r.Healthy)
			assert.Equal(t, "db.service", r.UnitName)
			assert.Error(t, r.Err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for unhealthy result")
		}
	})

	t.Run("script timeout", func(t *testing.T) {
		resultCh := make(chan Result, 10)

		c := New("db.service", []config.HealthCheck{
			{Type: "script", Script: &config.ScriptHealthCheck{Command: "sleep 30"}, Interval: 200 * time.Millisecond, Timeout: 500 * time.Millisecond, Retries: 1},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		go c.Run(ctx)

		select {
		case r := <-resultCh:
			assert.False(t, r.Healthy)
			assert.Error(t, r.Err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for unhealthy result")
		}
	})

	t.Run("script with output on failure", func(t *testing.T) {
		resultCh := make(chan Result, 10)

		c := New("db.service", []config.HealthCheck{
			{Type: "script", Script: &config.ScriptHealthCheck{Command: "echo 'connection refused' && exit 1"}, Interval: 100 * time.Millisecond, Timeout: 5 * time.Second, Retries: 1},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		go c.Run(ctx)

		select {
		case r := <-resultCh:
			assert.False(t, r.Healthy)
			assert.Contains(t, r.Err.Error(), "connection refused")
		case <-ctx.Done():
			t.Fatal("timed out waiting for unhealthy result")
		}
	})

	t.Run("http custom method", func(t *testing.T) {
		methodCh := make(chan string, 10)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case methodCh <- r.Method:
			default:
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		resultCh := make(chan Result, 10)

		c := New("web.service", []config.HealthCheck{
			{Type: "http", HTTP: &config.HTTPHealthCheck{Address: srv.URL, Method: "HEAD"}, Interval: 100 * time.Millisecond, Timeout: 200 * time.Millisecond, Retries: 10},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		go c.Run(ctx)

		select {
		case method := <-methodCh:
			assert.Equal(t, "HEAD", method)
		case <-ctx.Done():
			t.Fatal("timed out waiting for HTTP request")
		}

		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)

		assert.Empty(t, resultCh)
	})

	t.Run("http expected status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		resultCh := make(chan Result, 10)

		c := New("web.service", []config.HealthCheck{
			{Type: "http", HTTP: &config.HTTPHealthCheck{Address: srv.URL, ExpectedStatus: 204}, Interval: 100 * time.Millisecond, Timeout: 200 * time.Millisecond, Retries: 10},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		go c.Run(ctx)

		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)

		assert.Empty(t, resultCh)
	})

	t.Run("http expected status mismatch", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		resultCh := make(chan Result, 10)

		c := New("web.service", []config.HealthCheck{
			{Type: "http", HTTP: &config.HTTPHealthCheck{Address: srv.URL, ExpectedStatus: 204}, Interval: 100 * time.Millisecond, Timeout: 1 * time.Second, Retries: 1},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		go c.Run(ctx)

		select {
		case r := <-resultCh:
			assert.False(t, r.Healthy)
		case <-ctx.Done():
			t.Fatal("timed out waiting for unhealthy result")
		}
	})

	t.Run("http response match", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
		}))
		defer srv.Close()

		resultCh := make(chan Result, 10)

		c := New("web.service", []config.HealthCheck{
			{Type: "http", HTTP: &config.HTTPHealthCheck{Address: srv.URL, ResponseMatch: "\"status\":\"ok\""}, Interval: 100 * time.Millisecond, Timeout: 200 * time.Millisecond, Retries: 10},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		go c.Run(ctx)

		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)

		assert.Empty(t, resultCh)
	})

	t.Run("http response match failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"error"}`))
		}))
		defer srv.Close()

		resultCh := make(chan Result, 10)

		c := New("web.service", []config.HealthCheck{
			{Type: "http", HTTP: &config.HTTPHealthCheck{Address: srv.URL, ResponseMatch: "\"status\":\"ok\""}, Interval: 100 * time.Millisecond, Timeout: 1 * time.Second, Retries: 1},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		go c.Run(ctx)

		select {
		case r := <-resultCh:
			assert.False(t, r.Healthy)
		case <-ctx.Done():
			t.Fatal("timed out waiting for unhealthy result")
		}
	})

	t.Run("http custom headers", func(t *testing.T) {
		authCh := make(chan string, 10)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case authCh <- r.Header.Get("Authorization"):
			default:
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		resultCh := make(chan Result, 10)

		c := New("web.service", []config.HealthCheck{
			{
				Type: "http",
				HTTP: &config.HTTPHealthCheck{
					Address: srv.URL,
					Headers: map[string]string{"Authorization": "Bearer test123"},
				},
				Interval: 100 * time.Millisecond,
				Timeout:  200 * time.Millisecond,
				Retries:  10,
			},
		}, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		go c.Run(ctx)

		select {
		case auth := <-authCh:
			assert.Equal(t, "Bearer test123", auth)
		case <-ctx.Done():
			t.Fatal("timed out waiting for HTTP request")
		}

		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)

		assert.Empty(t, resultCh)
	})

	t.Run("empty checks returns immediately", func(t *testing.T) {
		resultCh := make(chan Result, 10)

		c := New("empty.service", nil, resultCh)

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		go c.Run(ctx)

		<-ctx.Done()

		assert.Empty(t, resultCh)
	})
}
