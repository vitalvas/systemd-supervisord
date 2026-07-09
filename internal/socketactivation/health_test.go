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
)

func TestHTTPProbe(t *testing.T) {
	t.Run("healthy on 2xx", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		p := newHTTPProbe(srv.URL, time.Second)
		assert.NoError(t, p.Probe(context.Background()))
	})

	t.Run("unhealthy on 5xx", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		p := newHTTPProbe(srv.URL, time.Second)
		err := p.Probe(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "503")
	})

	t.Run("unhealthy on connection refused", func(t *testing.T) {
		p := newHTTPProbe("http://127.0.0.1:1/health", 200*time.Millisecond)
		assert.Error(t, p.Probe(context.Background()))
	})

	t.Run("invalid url", func(t *testing.T) {
		p := newHTTPProbe("://bad", time.Second)
		assert.Error(t, p.Probe(context.Background()))
	})
}

func TestTCPProbe(t *testing.T) {
	t.Run("healthy when listening", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		p := newTCPProbe(ln.Addr().String(), time.Second)
		assert.NoError(t, p.Probe(context.Background()))
	})

	t.Run("unhealthy when closed", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := ln.Addr().String()
		ln.Close()

		p := newTCPProbe(addr, 200*time.Millisecond)
		assert.Error(t, p.Probe(context.Background()))
	})
}
