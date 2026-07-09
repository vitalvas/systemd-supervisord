package socketactivation

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// HealthProbe reports whether the backend is ready to receive connections.
// It returns nil when the backend is healthy, or an error describing why it is
// not. Implementations must respect the provided context deadline.
type HealthProbe interface {
	Probe(ctx context.Context) error
}

// HTTPProbe checks a backend by issuing an HTTP GET and requiring a 2xx status.
type HTTPProbe struct {
	url     string
	timeout time.Duration
	client  *http.Client
}

func newHTTPProbe(url string, timeout time.Duration) *HTTPProbe {
	return &HTTPProbe{
		url:     url,
		timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
}

func (p *HTTPProbe) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("http get %s: %w", p.url, err)
	}

	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http get %s returned status %d", p.url, resp.StatusCode)
	}

	return nil
}

// TCPProbe checks a backend by establishing a TCP connection to its address.
type TCPProbe struct {
	address string
	timeout time.Duration
}

func newTCPProbe(address string, timeout time.Duration) *TCPProbe {
	return &TCPProbe{address: address, timeout: timeout}
}

func (p *TCPProbe) Probe(ctx context.Context) error {
	dialer := net.Dialer{Timeout: p.timeout}

	conn, err := dialer.DialContext(ctx, "tcp", p.address)
	if err != nil {
		return fmt.Errorf("tcp dial %s: %w", p.address, err)
	}

	return conn.Close()
}
