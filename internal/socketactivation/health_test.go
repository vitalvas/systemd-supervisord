package socketactivation

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/systemd-supervisord/internal/config"
)

func TestChecksProbe(t *testing.T) {
	t.Run("healthy when all checks pass", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		p := newChecksProbe([]config.HealthCheck{
			{Type: "http", Timeout: time.Second, HTTP: &config.HTTPHealthCheck{Address: srv.URL}},
			{Type: "tcp", Timeout: time.Second, TCP: &config.TCPHealthCheck{Address: ln.Addr().String()}},
		})

		assert.NoError(t, p.probe(context.Background()))
	})

	t.Run("unhealthy when any check fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		p := newChecksProbe([]config.HealthCheck{
			{Type: "http", Timeout: time.Second, HTTP: &config.HTTPHealthCheck{Address: srv.URL}},
		})

		err := p.probe(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "503")
	})

	t.Run("unhealthy on connection refused", func(t *testing.T) {
		p := newChecksProbe([]config.HealthCheck{
			{Type: "tcp", Timeout: 200 * time.Millisecond, TCP: &config.TCPHealthCheck{Address: "127.0.0.1:1"}},
		})

		assert.Error(t, p.probe(context.Background()))
	})
}

func TestChecksProbePollInterval(t *testing.T) {
	t.Run("smallest configured interval", func(t *testing.T) {
		p := newChecksProbe([]config.HealthCheck{
			{Type: "tcp", Interval: 3 * time.Second, TCP: &config.TCPHealthCheck{Address: "a"}},
			{Type: "tcp", Interval: time.Second, TCP: &config.TCPHealthCheck{Address: "b"}},
		})

		assert.Equal(t, time.Second, p.pollInterval())
	})

	t.Run("falls back to one second without checks", func(t *testing.T) {
		p := newChecksProbe(nil)
		assert.Equal(t, time.Second, p.pollInterval())
	})
}
