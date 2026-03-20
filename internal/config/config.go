package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
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
	Type         string         `yaml:"type" validate:"omitempty,oneof=service timer"`
	Enabled      *bool          `yaml:"enabled"`
	Priority     *int           `yaml:"priority" validate:"omitempty,min=0"`
	DependsOn    []string       `yaml:"depends_on"`
	GracePeriod  time.Duration  `yaml:"grace_period" validate:"omitempty,min=0s"`
	MaxDelay     time.Duration  `yaml:"max_delay" validate:"omitempty,min=1s"`
	HealthChecks []HealthCheck  `yaml:"health_checks" validate:"dive"`
	Restart      *RestartPolicy `yaml:"restart" validate:"omitempty"`

	instanceMatch *regexp.Regexp
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

const DefaultPriority = 999

func (u *UnitConfig) GetPriority() int {
	if u.Priority == nil {
		return DefaultPriority
	}

	return *u.Priority
}

func (u *UnitConfig) IsEnabled() bool {
	if u.Enabled == nil {
		return true
	}

	return *u.Enabled
}

func (u *UnitConfig) IsTemplate() bool {
	idx := strings.Index(u.Name, "@")
	if idx < 0 {
		return false
	}

	after := u.Name[idx+1:]

	return after == "" || (strings.HasPrefix(after, "{") && strings.HasSuffix(after, "}"))
}

func (u *UnitConfig) UnitName() string {
	return fmt.Sprintf("%s.%s", u.Name, u.Type)
}

func (u *UnitConfig) TemplatePrefix() string {
	if !u.IsTemplate() {
		return ""
	}

	idx := strings.Index(u.Name, "@")

	return u.Name[:idx+1]
}

func (u *UnitConfig) InstancePattern() string {
	if !u.IsTemplate() {
		return ""
	}

	idx := strings.Index(u.Name, "@")
	after := u.Name[idx+1:]

	if !strings.HasPrefix(after, "{") || !strings.HasSuffix(after, "}") {
		return ""
	}

	return after[1 : len(after)-1]
}

func (u *UnitConfig) MatchInstance(instance string) bool {
	if u.instanceMatch == nil {
		return true
	}

	return u.instanceMatch.MatchString(instance)
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

	for i := range cfg.Units {
		if cfg.Units[i].Type == "" {
			cfg.Units[i].Type = "service"
		}
	}

	if err := validator.New().Struct(cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	if err := validateUniqueUnits(cfg.Units); err != nil {
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

	sort.SliceStable(cfg.Units, func(i, j int) bool {
		return cfg.Units[i].GetPriority() < cfg.Units[j].GetPriority()
	})

	return cfg, nil
}

func validateUniqueUnits(units []UnitConfig) error {
	seen := make(map[string]struct{}, len(units))

	for _, u := range units {
		key := u.UnitName()
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate unit %q", key)
		}

		seen[key] = struct{}{}
	}

	return nil
}

func validateTemplates(units []UnitConfig) error {
	for i := range units {
		if units[i].MaxDelay > 0 && units[i].Type != "timer" {
			return fmt.Errorf("max_delay is only allowed on timer units, found on %q", units[i].Name)
		}

		if err := validateInstancePattern(&units[i]); err != nil {
			return err
		}
	}

	return nil
}

func validateInstancePattern(u *UnitConfig) error {
	idx := strings.Index(u.Name, "@")
	if idx < 0 {
		return nil
	}

	after := u.Name[idx+1:]
	if after == "" {
		return nil
	}

	if !strings.HasPrefix(after, "{") || !strings.HasSuffix(after, "}") {
		return nil
	}

	pattern := after[1 : len(after)-1]
	if pattern == "" {
		return fmt.Errorf("empty instance pattern in unit %q", u.Name)
	}

	re, err := regexp.Compile(fmt.Sprintf("^%s$", pattern))
	if err != nil {
		return fmt.Errorf("invalid instance pattern in unit %q: %w", u.Name, err)
	}

	u.instanceMatch = re

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

	if u.Restart == nil {
		u.Restart = &RestartPolicy{Enabled: true}
	}

	if u.Restart.Backoff == 0 {
		u.Restart.Backoff = 5 * time.Second
	}
	if u.Restart.Cooldown == 0 {
		u.Restart.Cooldown = 60 * time.Second
	}
}
