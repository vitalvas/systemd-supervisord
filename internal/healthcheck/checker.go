package healthcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/vitalvas/systemd-supervisord/internal/config"
)

type Result struct {
	UnitName string
	Healthy  bool
	Err      error
}

type Checker struct {
	unitName string
	checks   []config.HealthCheck
	failCnt  int
	resultCh chan<- Result
}

func New(unitName string, checks []config.HealthCheck, resultCh chan<- Result) *Checker {
	return &Checker{
		unitName: unitName,
		checks:   checks,
		resultCh: resultCh,
	}
}

func (c *Checker) Run(ctx context.Context) {
	if len(c.checks) == 0 {
		return
	}

	interval := c.checks[0].Interval
	retries := c.checks[0].Retries

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := c.checkAll(ctx)
			if err != nil {
				c.failCnt++

				slog.Warn("health check failed",
					"unit", c.unitName,
					"consecutive_failures", c.failCnt,
					"error", err,
				)

				if c.failCnt >= retries {
					c.resultCh <- Result{UnitName: c.unitName, Healthy: false, Err: err}
				}
			} else {
				if c.failCnt >= retries {
					c.resultCh <- Result{UnitName: c.unitName, Healthy: true}
				}

				c.failCnt = 0
			}
		}
	}
}

func (c *Checker) checkAll(ctx context.Context) error {
	var errs []error

	for i := range c.checks {
		if err := checkEndpoint(ctx, &c.checks[i]); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func checkEndpoint(ctx context.Context, hc *config.HealthCheck) error {
	switch hc.Type {
	case "tcp":
		return checkTCP(ctx, hc.Timeout, hc.TCP)
	case "http":
		return checkHTTP(ctx, hc.Timeout, hc.HTTP)
	case "unix":
		return checkUnix(ctx, hc.Timeout, hc.Unix)
	case "script":
		return checkScript(ctx, hc.Timeout, hc.Script)
	default:
		return fmt.Errorf("unknown health check type: %s", hc.Type)
	}
}

func checkTCP(ctx context.Context, timeout time.Duration, cfg *config.TCPHealthCheck) error {
	dialer := net.Dialer{Timeout: timeout}

	conn, err := dialer.DialContext(ctx, "tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("tcp dial %s: %w", cfg.Address, err)
	}

	return conn.Close()
}

func checkHTTP(ctx context.Context, timeout time.Duration, cfg *config.HTTPHealthCheck) error {
	client := &http.Client{Timeout: timeout}

	method := cfg.Method
	if method == "" {
		method = http.MethodGet
	}

	req, err := http.NewRequestWithContext(ctx, method, cfg.Address, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	for key, val := range cfg.Headers {
		req.Header.Set(key, val)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http %s %s: %w", method, cfg.Address, err)
	}

	defer resp.Body.Close()

	if cfg.ExpectedStatus > 0 {
		if resp.StatusCode != cfg.ExpectedStatus {
			return fmt.Errorf("http %s expected status %d, got %d", cfg.Address, cfg.ExpectedStatus, resp.StatusCode)
		}
	} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %s returned status %d", cfg.Address, resp.StatusCode)
	}

	if cfg.ResponseMatch != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
		if err != nil {
			return fmt.Errorf("reading response body from %s: %w", cfg.Address, err)
		}

		if !strings.Contains(string(body), cfg.ResponseMatch) {
			return fmt.Errorf("http %s response does not contain %q", cfg.Address, cfg.ResponseMatch)
		}
	}

	return nil
}

func checkUnix(ctx context.Context, timeout time.Duration, cfg *config.UnixHealthCheck) error {
	dialer := net.Dialer{Timeout: timeout}

	conn, err := dialer.DialContext(ctx, "unix", cfg.Address)
	if err != nil {
		return fmt.Errorf("unix dial %s: %w", cfg.Address, err)
	}

	return conn.Close()
}

func checkScript(ctx context.Context, timeout time.Duration, cfg *config.ScriptHealthCheck) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Command)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("script %q: %s: %w", cfg.Command, strings.TrimSpace(string(output)), err)
	}

	return nil
}
