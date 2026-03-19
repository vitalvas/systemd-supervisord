package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

type Config struct {
	LogLevel          string        `yaml:"log_level" validate:"omitempty,oneof=debug info warn error"`
	Units             []UnitConfig  `yaml:"units" validate:"required,min=1,dive"`
	Notify            NotifyConfig  `yaml:"notify"`
	Socket            string        `yaml:"socket" validate:"omitempty"`
	DiscoveryInterval time.Duration `yaml:"discovery_interval" validate:"omitempty,min=5s"`
}

type UnitConfig struct {
	Name         string         `yaml:"name" validate:"required"`
	Type         string         `yaml:"type" validate:"required,oneof=service timer"`
	Enabled      bool           `yaml:"enabled"`
	Discover     bool           `yaml:"discover"`
	DependsOn    []string       `yaml:"depends_on"`
	GracePeriod  time.Duration  `yaml:"grace_period" validate:"omitempty,min=0s"`
	HealthChecks []HealthCheck  `yaml:"health_checks" validate:"dive"`
	Restart      *RestartPolicy `yaml:"restart" validate:"omitempty"`
}

type HealthCheck struct {
	Type     string        `yaml:"type" validate:"required,oneof=tcp http unix script"`
	Interval time.Duration `yaml:"interval" validate:"required,min=1s"`
	Timeout  time.Duration `yaml:"timeout" validate:"required,min=1s"`
	Retries  int           `yaml:"retries" validate:"required,min=1"`

	TCP    *TCPHealthCheck    `yaml:"tcp" validate:"required_if=Type tcp"`
	HTTP   *HTTPHealthCheck   `yaml:"http" validate:"required_if=Type http"`
	Unix   *UnixHealthCheck   `yaml:"unix" validate:"required_if=Type unix"`
	Script *ScriptHealthCheck `yaml:"script" validate:"required_if=Type script"`
}

type TCPHealthCheck struct {
	Address string `yaml:"address" validate:"required"`
}

type HTTPHealthCheck struct {
	Address        string            `yaml:"address" validate:"required"`
	Method         string            `yaml:"method" validate:"omitempty,oneof=GET HEAD POST PUT"`
	ExpectedStatus int               `yaml:"expected_status" validate:"omitempty,min=100,max=599"`
	ResponseMatch  string            `yaml:"response_match"`
	Headers        map[string]string `yaml:"headers"`
}

type UnixHealthCheck struct {
	Address string `yaml:"address" validate:"required"`
}

type ScriptHealthCheck struct {
	Command string `yaml:"command" validate:"required"`
}

type RestartPolicy struct {
	Enabled  bool          `yaml:"enabled"`
	Backoff  time.Duration `yaml:"backoff" validate:"omitempty,min=1s"`
	Cooldown time.Duration `yaml:"cooldown" validate:"omitempty,min=1s"`
}

type NotifyConfig struct {
	Variables map[string]string `yaml:"variables"`
	Webhooks  []WebhookConfig   `yaml:"webhooks" validate:"dive"`
	Scripts   []ScriptConfig    `yaml:"scripts" validate:"dive"`
	Execs     []ExecConfig      `yaml:"execs" validate:"dive"`
}

type ExecConfig struct {
	Command string        `yaml:"command" validate:"required"`
	Timeout time.Duration `yaml:"timeout" validate:"omitempty,min=1s"`
	Events  []string      `yaml:"events" validate:"dive,oneof=state_changed health_changed"`
}

type WebhookConfig struct {
	URL     string        `yaml:"url" validate:"required,url"`
	Timeout time.Duration `yaml:"timeout" validate:"omitempty,min=1s"`
	Events  []string      `yaml:"events" validate:"dive,oneof=state_changed health_changed"`
}

type ScriptConfig struct {
	Path    string        `yaml:"path" validate:"required"`
	Timeout time.Duration `yaml:"timeout" validate:"omitempty,min=1s"`
	Events  []string      `yaml:"events" validate:"dive,oneof=state_changed health_changed"`
}

func (c *Config) DependencyOrder() []string {
	deps := make(map[string][]string, len(c.Units))
	for _, u := range c.Units {
		deps[u.Name] = u.DependsOn
	}

	visited := make(map[string]bool, len(c.Units))
	var order []string

	var visit func(name string)
	visit = func(name string) {
		if visited[name] {
			return
		}

		visited[name] = true

		for _, dep := range deps[name] {
			visit(dep)
		}

		order = append(order, name)
	}

	for _, u := range c.Units {
		visit(u.Name)
	}

	return order
}

func (c *Config) Dependents(unitName string) []string {
	var result []string

	for _, u := range c.Units {
		for _, dep := range u.DependsOn {
			if dep == unitName {
				result = append(result, u.Name)

				break
			}
		}
	}

	return result
}

func (u *UnitConfig) IsTemplate() bool {
	return strings.Contains(u.Name, "@")
}

func (u *UnitConfig) UnitName() string {
	return fmt.Sprintf("%s.%s", u.Name, u.Type)
}

func (u *UnitConfig) TemplatePrefix() string {
	if !u.IsTemplate() {
		return ""
	}

	return fmt.Sprintf("%s@", strings.TrimSuffix(u.Name, "@"))
}

func (u *UnitConfig) ResolveHealthChecks(instance string) []HealthCheck {
	if len(u.HealthChecks) == 0 {
		return nil
	}

	resolved := make([]HealthCheck, len(u.HealthChecks))

	for i, hc := range u.HealthChecks {
		resolved[i] = hc

		switch {
		case hc.TCP != nil:
			cp := *hc.TCP
			cp.Address = strings.ReplaceAll(cp.Address, "{{instance}}", instance)
			resolved[i].TCP = &cp
		case hc.HTTP != nil:
			cp := *hc.HTTP
			cp.Address = strings.ReplaceAll(cp.Address, "{{instance}}", instance)
			resolved[i].HTTP = &cp
		case hc.Unix != nil:
			cp := *hc.Unix
			cp.Address = strings.ReplaceAll(cp.Address, "{{instance}}", instance)
			resolved[i].Unix = &cp
		case hc.Script != nil:
			cp := *hc.Script
			cp.Command = strings.ReplaceAll(cp.Command, "{{instance}}", instance)
			resolved[i].Script = &cp
		}
	}

	return resolved
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg := &Config{
		LogLevel:          "info",
		Socket:            "/var/run/systemd-supervisord.sock",
		DiscoveryInterval: 30 * time.Second,
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := validator.New().Struct(cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	if err := validateTemplates(cfg.Units); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	if err := validateDependencies(cfg.Units); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	for i := range cfg.Units {
		applyDefaults(&cfg.Units[i])
	}

	return cfg, nil
}

func validateTemplates(units []UnitConfig) error {
	for _, u := range units {
		if u.IsTemplate() && !u.Discover {
			return fmt.Errorf("template unit %q requires discover: true", u.Name)
		}
	}

	return nil
}

func validateDependencies(units []UnitConfig) error {
	names := make(map[string]struct{}, len(units))
	for _, u := range units {
		names[u.Name] = struct{}{}
	}

	for _, u := range units {
		for _, dep := range u.DependsOn {
			if _, ok := names[dep]; !ok {
				return fmt.Errorf("unit %q depends on unknown unit %q", u.Name, dep)
			}
		}
	}

	return detectCycle(units)
}

func detectCycle(units []UnitConfig) error {
	deps := make(map[string][]string, len(units))
	for _, u := range units {
		deps[u.Name] = u.DependsOn
	}

	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)

	state := make(map[string]int, len(units))

	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case visiting:
			return fmt.Errorf("circular dependency detected involving unit %q", name)
		case visited:
			return nil
		}

		state[name] = visiting

		for _, dep := range deps[name] {
			if err := visit(dep); err != nil {
				return err
			}
		}

		state[name] = visited

		return nil
	}

	for _, u := range units {
		if err := visit(u.Name); err != nil {
			return err
		}
	}

	return nil
}

func applyDefaults(u *UnitConfig) {
	for i := range u.HealthChecks {
		if u.HealthChecks[i].Interval == 0 {
			u.HealthChecks[i].Interval = 10 * time.Second
		}
		if u.HealthChecks[i].Timeout == 0 {
			u.HealthChecks[i].Timeout = 5 * time.Second
		}
		if u.HealthChecks[i].Retries == 0 {
			u.HealthChecks[i].Retries = 3
		}
	}

	if u.Restart != nil {
		if u.Restart.Backoff == 0 {
			u.Restart.Backoff = 5 * time.Second
		}
		if u.Restart.Cooldown == 0 {
			u.Restart.Cooldown = 60 * time.Second
		}
	}
}
