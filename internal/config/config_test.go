package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		content := `
log_level: debug
socket: /tmp/test.sock
units:
  - name: nginx
    type: service
    enabled: true
    health_checks:
      - type: http
        interval: 15s
        timeout: 3s
        retries: 5
        http:
          address: http://localhost:80
    restart:
      enabled: true
      backoff: 10s
      cooldown: 120s
  - name: backup
    type: timer
    enabled: true
notify:
  webhooks:
    - url: http://hooks.example.com/alert
      timeout: 10s
  scripts:
    - path: /usr/local/bin/notify.sh
      timeout: 30s
`
		cfg := loadFromString(t, content)

		assert.Equal(t, "debug", cfg.LogLevel)
		assert.Equal(t, "/tmp/test.sock", cfg.Socket)
		require.Len(t, cfg.Units, 2)

		nginx := cfg.Units[0]
		assert.Equal(t, "nginx", nginx.Name)
		assert.Equal(t, "service", nginx.Type)
		assert.True(t, nginx.Enabled)
		assert.Equal(t, "nginx.service", nginx.UnitName())

		require.Len(t, nginx.HealthChecks, 1)
		assert.Equal(t, "http", nginx.HealthChecks[0].Type)
		require.NotNil(t, nginx.HealthChecks[0].HTTP)
		assert.Equal(t, "http://localhost:80", nginx.HealthChecks[0].HTTP.Address)
		assert.Equal(t, 15*time.Second, nginx.HealthChecks[0].Interval)
		assert.Equal(t, 3*time.Second, nginx.HealthChecks[0].Timeout)
		assert.Equal(t, 5, nginx.HealthChecks[0].Retries)

		require.NotNil(t, nginx.Restart)
		assert.True(t, nginx.Restart.Enabled)
		assert.Equal(t, 10*time.Second, nginx.Restart.Backoff)
		assert.Equal(t, 120*time.Second, nginx.Restart.Cooldown)

		backup := cfg.Units[1]
		assert.Equal(t, "backup", backup.Name)
		assert.Equal(t, "timer", backup.Type)
		assert.Equal(t, "backup.timer", backup.UnitName())

		require.Len(t, cfg.Notify.Webhooks, 1)
		assert.Equal(t, "http://hooks.example.com/alert", cfg.Notify.Webhooks[0].URL)
		assert.Equal(t, 10*time.Second, cfg.Notify.Webhooks[0].Timeout)

		require.Len(t, cfg.Notify.Scripts, 1)
		assert.Equal(t, "/usr/local/bin/notify.sh", cfg.Notify.Scripts[0].Path)
	})

	t.Run("multiple health checks", func(t *testing.T) {
		content := `
units:
  - name: salt-master
    type: service
    enabled: true
    health_checks:
      - type: tcp
        interval: 10s
        timeout: 5s
        retries: 3
        tcp:
          address: localhost:4505
      - type: tcp
        interval: 10s
        timeout: 5s
        retries: 3
        tcp:
          address: localhost:4506
`
		cfg := loadFromString(t, content)
		require.Len(t, cfg.Units[0].HealthChecks, 2)
		assert.Equal(t, "localhost:4505", cfg.Units[0].HealthChecks[0].TCP.Address)
		assert.Equal(t, "localhost:4506", cfg.Units[0].HealthChecks[1].TCP.Address)
	})

	t.Run("defaults applied", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
    health_checks:
      - type: tcp
        interval: 10s
        timeout: 5s
        retries: 3
        tcp:
          address: localhost:8080
    restart:
      enabled: true
      backoff: 5s
`
		cfg := loadFromString(t, content)

		assert.Equal(t, "info", cfg.LogLevel)
		assert.Equal(t, "/var/run/systemd-supervisord.sock", cfg.Socket)

		assert.Equal(t, 5*time.Second, cfg.Units[0].Restart.Backoff)
		assert.Equal(t, 60*time.Second, cfg.Units[0].Restart.Cooldown)
	})

	t.Run("missing units", func(t *testing.T) {
		content := `log_level: info`
		_, err := loadStringConfig(content)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "validating config")
	})

	t.Run("invalid unit type", func(t *testing.T) {
		content := `
units:
  - name: test
    type: invalid
    enabled: true
`
		_, err := loadStringConfig(content)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "validating config")
	})

	t.Run("invalid log level", func(t *testing.T) {
		content := `
log_level: verbose
units:
  - name: test
    type: service
    enabled: true
`
		_, err := loadStringConfig(content)
		require.Error(t, err)
	})

	t.Run("script health check", func(t *testing.T) {
		content := `
units:
  - name: mydb
    type: service
    enabled: true
    health_checks:
      - type: script
        interval: 15s
        timeout: 10s
        retries: 3
        script:
          command: "pg_isready -h localhost"
`
		cfg := loadFromString(t, content)
		require.Len(t, cfg.Units[0].HealthChecks, 1)

		hc := cfg.Units[0].HealthChecks[0]
		assert.Equal(t, "script", hc.Type)
		require.NotNil(t, hc.Script)
		assert.Equal(t, "pg_isready -h localhost", hc.Script.Command)
		assert.Equal(t, 15*time.Second, hc.Interval)
	})

	t.Run("script health check missing command", func(t *testing.T) {
		content := `
units:
  - name: mydb
    type: service
    enabled: true
    health_checks:
      - type: script
        interval: 10s
        timeout: 5s
        retries: 3
`
		_, err := loadStringConfig(content)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "validating config")
	})

	t.Run("invalid health check type", func(t *testing.T) {
		content := `
units:
  - name: test
    type: service
    enabled: true
    health_checks:
      - type: grpc
        interval: 10s
        timeout: 5s
        retries: 3
`
		_, err := loadStringConfig(content)
		require.Error(t, err)
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := Load("/nonexistent/config.yaml")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading config")
	})

	t.Run("invalid yaml", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte(":\ninvalid: [yaml"), 0o644))

		_, err := Load(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing config")
	})
}

func TestUnitConfig(t *testing.T) {
	t.Run("static unit", func(t *testing.T) {
		u := UnitConfig{Name: "nginx", Type: "service"}

		assert.False(t, u.IsTemplate())
		assert.Equal(t, "nginx.service", u.UnitName())
		assert.Empty(t, u.TemplatePrefix())
	})

	t.Run("template unit", func(t *testing.T) {
		u := UnitConfig{
			Name:     "myapp@",
			Type:     "service",
			Discover: true,
		}

		assert.True(t, u.IsTemplate())
		assert.Equal(t, "myapp@.service", u.UnitName())
		assert.Equal(t, "myapp@", u.TemplatePrefix())
	})

	t.Run("resolve health checks with instance placeholder", func(t *testing.T) {
		u := UnitConfig{
			Name: "myapp@",
			Type: "service",
			HealthChecks: []HealthCheck{
				{Type: "tcp", TCP: &TCPHealthCheck{Address: "localhost:{{instance}}05"}, Interval: 10 * time.Second, Timeout: 5 * time.Second, Retries: 3},
				{Type: "tcp", TCP: &TCPHealthCheck{Address: "localhost:{{instance}}06"}, Interval: 10 * time.Second, Timeout: 5 * time.Second, Retries: 3},
			},
		}

		checks := u.ResolveHealthChecks("45")

		require.Len(t, checks, 2)
		assert.Equal(t, "localhost:4505", checks[0].TCP.Address)
		assert.Equal(t, "localhost:4506", checks[1].TCP.Address)
		assert.Equal(t, "localhost:{{instance}}05", u.HealthChecks[0].TCP.Address)
	})

	t.Run("resolve health checks nil", func(t *testing.T) {
		u := UnitConfig{Name: "myapp@", Type: "service"}
		assert.Nil(t, u.ResolveHealthChecks("test"))
	})
}

func TestLoadTemplateConfig(t *testing.T) {
	t.Run("template with discover", func(t *testing.T) {
		content := `
units:
  - name: myapp@
    type: service
    enabled: true
    discover: true
    health_checks:
      - type: tcp
        interval: 10s
        timeout: 5s
        retries: 3
        tcp:
          address: "localhost:{{instance}}"
    restart:
      enabled: true
      backoff: 5s
`
		cfg := loadFromString(t, content)
		require.Len(t, cfg.Units, 1)

		u := cfg.Units[0]
		assert.True(t, u.IsTemplate())
		assert.True(t, u.Discover)
		assert.Contains(t, u.HealthChecks[0].TCP.Address, "{{instance}}")
	})

	t.Run("template without discover rejected", func(t *testing.T) {
		content := `
units:
  - name: myapp@
    type: service
    enabled: true
`
		_, err := loadStringConfig(content)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires discover")
	})

	t.Run("discovery interval default", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
`
		cfg := loadFromString(t, content)
		assert.Equal(t, 30*time.Second, cfg.DiscoveryInterval)
	})

	t.Run("custom discovery interval", func(t *testing.T) {
		content := `
discovery_interval: 60s
units:
  - name: app
    type: service
    enabled: true
`
		cfg := loadFromString(t, content)
		assert.Equal(t, 60*time.Second, cfg.DiscoveryInterval)
	})
}

func TestGracePeriod(t *testing.T) {
	t.Run("grace period parsed", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
    grace_period: 30s
`
		cfg := loadFromString(t, content)
		assert.Equal(t, 30*time.Second, cfg.Units[0].GracePeriod)
	})

	t.Run("grace period zero by default", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
`
		cfg := loadFromString(t, content)
		assert.Equal(t, time.Duration(0), cfg.Units[0].GracePeriod)
	})
}

func TestHTTPHealthCheckOptions(t *testing.T) {
	t.Run("method and expected_status", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
    health_checks:
      - type: http
        interval: 10s
        timeout: 5s
        retries: 3
        http:
          address: http://localhost:8080/health
          method: HEAD
          expected_status: 204
`
		cfg := loadFromString(t, content)
		hc := cfg.Units[0].HealthChecks[0]
		require.NotNil(t, hc.HTTP)
		assert.Equal(t, "HEAD", hc.HTTP.Method)
		assert.Equal(t, 204, hc.HTTP.ExpectedStatus)
	})

	t.Run("response_match and headers", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
    health_checks:
      - type: http
        interval: 10s
        timeout: 5s
        retries: 3
        http:
          address: http://localhost:8080/health
          response_match: "\"status\":\"ok\""
          headers:
            Authorization: Bearer token123
            Accept: application/json
`
		cfg := loadFromString(t, content)
		hc := cfg.Units[0].HealthChecks[0]
		require.NotNil(t, hc.HTTP)
		assert.Equal(t, "\"status\":\"ok\"", hc.HTTP.ResponseMatch)
		assert.Equal(t, "Bearer token123", hc.HTTP.Headers["Authorization"])
		assert.Equal(t, "application/json", hc.HTTP.Headers["Accept"])
	})

	t.Run("invalid method rejected", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
    health_checks:
      - type: http
        interval: 10s
        timeout: 5s
        retries: 3
        http:
          address: http://localhost:8080
          method: DELETE
`
		_, err := loadStringConfig(content)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "validating config")
	})
}

func TestExecConfig(t *testing.T) {
	t.Run("exec in notify config", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
notify:
  execs:
    - command: "echo alert"
      timeout: 10s
    - command: "/usr/local/bin/alert.sh"
      timeout: 30s
      events:
        - state_changed
`
		cfg := loadFromString(t, content)
		require.Len(t, cfg.Notify.Execs, 2)
		assert.Equal(t, "echo alert", cfg.Notify.Execs[0].Command)
		assert.Equal(t, 10*time.Second, cfg.Notify.Execs[0].Timeout)
		assert.Equal(t, []string{"state_changed"}, cfg.Notify.Execs[1].Events)
	})
}

func TestNotifyVariables(t *testing.T) {
	t.Run("variables parsed", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
notify:
  variables:
    hostname: web-01
    environment: production
`
		cfg := loadFromString(t, content)
		require.Len(t, cfg.Notify.Variables, 2)
		assert.Equal(t, "web-01", cfg.Notify.Variables["hostname"])
		assert.Equal(t, "production", cfg.Notify.Variables["environment"])
	})

	t.Run("empty variables", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
notify:
  variables: {}
`
		cfg := loadFromString(t, content)
		assert.Empty(t, cfg.Notify.Variables)
	})
}

func TestMaxDelay(t *testing.T) {
	t.Run("timer with max_delay", func(t *testing.T) {
		content := `
units:
  - name: certbot-renew
    type: timer
    enabled: true
    max_delay: 24h
`
		cfg := loadFromString(t, content)
		assert.Equal(t, 24*time.Hour, cfg.Units[0].MaxDelay)
	})

	t.Run("max_delay zero by default", func(t *testing.T) {
		content := `
units:
  - name: certbot-renew
    type: timer
    enabled: true
`
		cfg := loadFromString(t, content)
		assert.Equal(t, time.Duration(0), cfg.Units[0].MaxDelay)
	})

	t.Run("max_delay on service rejected", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
    max_delay: 10s
`
		_, err := loadStringConfig(content)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max_delay is only allowed on timer units")
	})
}

func TestDependencies(t *testing.T) {
	t.Run("valid dependencies", func(t *testing.T) {
		content := `
units:
  - name: db
    type: service
    enabled: true
  - name: app
    type: service
    enabled: true
    depends_on:
      - db
`
		cfg := loadFromString(t, content)
		assert.Equal(t, []string{"db"}, cfg.Units[1].DependsOn)
	})

	t.Run("unknown dependency rejected", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
    depends_on:
      - nonexistent
`
		_, err := loadStringConfig(content)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown unit")
	})

	t.Run("circular dependency rejected", func(t *testing.T) {
		content := `
units:
  - name: a
    type: service
    enabled: true
    depends_on:
      - b
  - name: b
    type: service
    enabled: true
    depends_on:
      - a
`
		_, err := loadStringConfig(content)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "circular dependency")
	})

	t.Run("self dependency rejected", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
    depends_on:
      - app
`
		_, err := loadStringConfig(content)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "circular dependency")
	})

	t.Run("dependency order", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
    depends_on:
      - db
  - name: db
    type: service
    enabled: true
  - name: cache
    type: service
    enabled: true
    depends_on:
      - db
`
		cfg := loadFromString(t, content)
		order := cfg.DependencyOrder()
		dbIdx := -1
		appIdx := -1
		cacheIdx := -1

		for i, name := range order {
			switch name {
			case "db":
				dbIdx = i
			case "app":
				appIdx = i
			case "cache":
				cacheIdx = i
			}
		}

		assert.Greater(t, appIdx, dbIdx)
		assert.Greater(t, cacheIdx, dbIdx)
	})

	t.Run("dependents lookup", func(t *testing.T) {
		content := `
units:
  - name: db
    type: service
    enabled: true
  - name: app
    type: service
    enabled: true
    depends_on:
      - db
  - name: worker
    type: service
    enabled: true
    depends_on:
      - db
`
		cfg := loadFromString(t, content)
		deps := cfg.Dependents("db")
		assert.ElementsMatch(t, []string{"app", "worker"}, deps)
	})

	t.Run("no dependents", func(t *testing.T) {
		content := `
units:
  - name: app
    type: service
    enabled: true
`
		cfg := loadFromString(t, content)
		deps := cfg.Dependents("app")
		assert.Nil(t, deps)
	})
}

func loadFromString(t *testing.T, content string) *Config {
	t.Helper()

	cfg, err := loadStringConfig(content)
	require.NoError(t, err)

	return cfg
}

func loadStringConfig(content string) (*Config, error) {
	dir, err := os.MkdirTemp("", "config-test")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return nil, err
	}

	return Load(path)
}
