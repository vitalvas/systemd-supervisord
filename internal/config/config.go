package config

import (
	"fmt"
	"net"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

type Config struct {
	LogLevel          string                   `yaml:"log_level" validate:"omitempty,oneof=debug info warn error"`
	Units             []UnitConfig             `yaml:"-" validate:"required,min=1"`
	Notify            NotifyConfig             `yaml:"notify"`
	Socket            string                   `yaml:"socket" validate:"omitempty"`
	DiscoveryInterval time.Duration            `yaml:"discovery_interval" validate:"omitempty,min=5s"`
	HTTP              HTTPConfig               `yaml:"http"`
	SocketActivation  []SocketActivationConfig `yaml:"socket_activation" validate:"dive"`
}

type SocketActivationConfig struct {
	Unit           string         `yaml:"-" validate:"required"`
	Name           string         `yaml:"-"`
	Listen         string         `yaml:"listen" validate:"required,hostname_port"`
	Protocol       []string       `yaml:"protocol" validate:"dive,oneof=tcp udp"`
	Backend        string         `yaml:"backend" validate:"required,hostname_port"`
	StartupTimeout time.Duration  `yaml:"startup_timeout" validate:"omitempty,min=1s"`
	IdleTimeout    time.Duration  `yaml:"idle_timeout" validate:"omitempty,min=1s"`
	HealthChecks   []HealthCheck  `yaml:"health_checks" validate:"dive"`
	Restart        *RestartPolicy `yaml:"restart" validate:"omitempty"`
}

type HTTPConfig struct {
	Listen          string        `yaml:"listen" validate:"omitempty,hostname_port"`
	ReadTimeout     time.Duration `yaml:"read_timeout" validate:"omitempty,min=1s"`
	WriteTimeout    time.Duration `yaml:"write_timeout" validate:"omitempty,min=1s"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout" validate:"omitempty,min=1s"`
}

type UnitConfig struct {
	Name         string         `yaml:"-" validate:"required"`
	Type         string         `yaml:"-" validate:"required,oneof=service timer"`
	Enabled      *bool          `yaml:"enabled"`
	Critical     bool           `yaml:"critical"`
	Priority     int            `yaml:"priority" validate:"omitempty,min=1"`
	DependsOn    []string       `yaml:"depends_on"`
	GracePeriod  time.Duration  `yaml:"grace_period" validate:"omitempty,min=0s"`
	MaxDelay     time.Duration  `yaml:"max_delay" validate:"omitempty,min=1s"`
	HealthChecks []HealthCheck  `yaml:"health_checks" validate:"dive"`
	Restart      *RestartPolicy `yaml:"restart" validate:"omitempty"`

	instanceMatch *regexp.Regexp
}

type HealthCheck struct {
	Type     string        `yaml:"type" validate:"required,oneof=tcp http unix script"`
	Interval time.Duration `yaml:"interval" validate:"omitempty,min=1s"`
	Timeout  time.Duration `yaml:"timeout" validate:"omitempty,min=1s"`
	Retries  int           `yaml:"retries" validate:"omitempty,min=1"`

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
		bareDeps := make([]string, len(u.DependsOn))
		for i, dep := range u.DependsOn {
			bareDeps[i] = depBareName(dep)
		}

		deps[u.Name] = bareDeps
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
			if dep == unitName || depBareName(dep) == unitName {
				result = append(result, u.Name)

				break
			}
		}
	}

	return result
}

const DefaultPriority = 999

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

type rawConfig struct {
	LogLevel          string        `yaml:"log_level"`
	Units             yaml.Node     `yaml:"units"`
	Notify            NotifyConfig  `yaml:"notify"`
	Socket            string        `yaml:"socket"`
	DiscoveryInterval time.Duration `yaml:"discovery_interval"`
	HTTP              HTTPConfig    `yaml:"http"`
	SocketActivation  yaml.Node     `yaml:"socket_activation"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	raw := &rawConfig{}
	if err := yaml.Unmarshal(data, raw); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	units, err := parseUnits(&raw.Units)
	if err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	socketActivation, err := parseSocketActivation(&raw.SocketActivation)
	if err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg := &Config{
		LogLevel:          raw.LogLevel,
		Units:             units,
		Notify:            raw.Notify,
		Socket:            raw.Socket,
		DiscoveryInterval: raw.DiscoveryInterval,
		HTTP:              raw.HTTP,
		SocketActivation:  socketActivation,
	}

	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}

	if cfg.Socket == "" {
		cfg.Socket = "/var/run/systemd-supervisord.socket"
	}

	if cfg.DiscoveryInterval == 0 {
		cfg.DiscoveryInterval = 30 * time.Second
	}

	applyHTTPDefaults(&cfg.HTTP)

	for i := range cfg.SocketActivation {
		applySocketActivationDefaults(&cfg.SocketActivation[i])
	}

	for i := range cfg.Units {
		applyDefaults(&cfg.Units[i])
	}

	validate := validator.New()
	validate.RegisterTagNameFunc(func(fld reflect.StructField) string {
		tag := fld.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			return strings.ToLower(fld.Name)
		}

		return strings.Split(tag, ",")[0]
	})

	if err := validate.Struct(cfg); err != nil {
		return nil, formatFirstValidationError("", err)
	}

	for i := range cfg.Units {
		if err := validate.Struct(&cfg.Units[i]); err != nil {
			return nil, formatFirstValidationError(cfg.Units[i].UnitName(), err)
		}
	}

	if err := validateTemplates(cfg.Units); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	if err := validateDependencies(cfg.Units); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	if err := validateSocketActivation(cfg.SocketActivation); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	sort.SliceStable(cfg.Units, func(i, j int) bool {
		return cfg.Units[i].Priority < cfg.Units[j].Priority
	})

	return cfg, nil
}

func parseUnitKey(key string) (string, string, error) {
	if strings.HasSuffix(key, ".service") {
		name := strings.TrimSuffix(key, ".service")
		if name == "" {
			return "", "", fmt.Errorf("empty unit name in key %q", key)
		}

		return name, "service", nil
	}

	if strings.HasSuffix(key, ".timer") {
		name := strings.TrimSuffix(key, ".timer")
		if name == "" {
			return "", "", fmt.Errorf("empty unit name in key %q", key)
		}

		return name, "timer", nil
	}

	return "", "", fmt.Errorf("unit key %q must end with .service or .timer", key)
}

func parseUnits(node *yaml.Node) ([]UnitConfig, error) {
	if node.Kind == 0 {
		return nil, nil
	}

	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("units must be a map, got %v", node.Kind)
	}

	if len(node.Content)%2 != 0 {
		return nil, fmt.Errorf("invalid units map structure")
	}

	seen := make(map[string]struct{})
	units := make([]UnitConfig, 0, len(node.Content)/2)

	for i := 0; i < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]

		key := keyNode.Value

		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate unit %q", key)
		}

		seen[key] = struct{}{}

		name, typ, err := parseUnitKey(key)
		if err != nil {
			return nil, err
		}

		var u UnitConfig
		if err := valNode.Decode(&u); err != nil {
			return nil, fmt.Errorf("decoding unit %q: %w", key, err)
		}

		u.Name = name
		u.Type = typ

		units = append(units, u)
	}

	return units, nil
}

func parseSocketActivation(node *yaml.Node) ([]SocketActivationConfig, error) {
	if node.Kind == 0 {
		return nil, nil
	}

	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("socket_activation must be a map keyed by unit name, got %v", node.Kind)
	}

	if len(node.Content)%2 != 0 {
		return nil, fmt.Errorf("invalid socket_activation map structure")
	}

	seen := make(map[string]struct{})
	entries := make([]SocketActivationConfig, 0, len(node.Content)/2)

	for i := 0; i < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]

		unit := keyNode.Value

		if unit == "" {
			return nil, fmt.Errorf("empty socket_activation unit name")
		}

		if _, exists := seen[unit]; exists {
			return nil, fmt.Errorf("duplicate socket_activation unit %q", unit)
		}

		seen[unit] = struct{}{}

		var e SocketActivationConfig
		if err := valNode.Decode(&e); err != nil {
			return nil, fmt.Errorf("decoding socket_activation %q: %w", unit, err)
		}

		e.Unit = unit
		e.Name = socketActivationName(unit)

		entries = append(entries, e)
	}

	return entries, nil
}

// socketActivationName derives a friendly log identifier from a unit name by
// stripping the type suffix and any template instance prefix.
func socketActivationName(unit string) string {
	name := strings.TrimSuffix(strings.TrimSuffix(unit, ".service"), ".timer")

	if idx := strings.Index(name, "@"); idx >= 0 {
		instance := name[idx+1:]
		if instance != "" {
			return instance
		}

		return name[:idx]
	}

	return name
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

func isQualifiedDep(dep string) bool {
	return strings.HasSuffix(dep, ".service") || strings.HasSuffix(dep, ".timer")
}

func validateDependencies(units []UnitConfig) error {
	bareNames := make(map[string]struct{}, len(units))
	qualifiedNames := make(map[string]struct{}, len(units))

	for _, u := range units {
		bareNames[u.Name] = struct{}{}
		qualifiedNames[u.UnitName()] = struct{}{}
	}

	for _, u := range units {
		for _, dep := range u.DependsOn {
			if isQualifiedDep(dep) {
				if _, ok := qualifiedNames[dep]; !ok {
					return fmt.Errorf("unit %q depends on unknown unit %q", u.UnitName(), dep)
				}
			} else {
				if _, ok := bareNames[dep]; !ok {
					return fmt.Errorf("unit %q depends on unknown unit %q", u.Name, dep)
				}
			}
		}
	}

	return detectCycle(units)
}

func depBareName(dep string) string {
	if isQualifiedDep(dep) {
		return strings.TrimSuffix(strings.TrimSuffix(dep, ".service"), ".timer")
	}

	return dep
}

func detectCycle(units []UnitConfig) error {
	deps := make(map[string][]string, len(units))
	for _, u := range units {
		bareDeps := make([]string, len(u.DependsOn))
		for i, dep := range u.DependsOn {
			bareDeps[i] = depBareName(dep)
		}

		deps[u.Name] = bareDeps
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

func formatFirstValidationError(unit string, err error) error {
	errs, ok := err.(validator.ValidationErrors)
	if !ok {
		return err
	}

	e := errs[0]

	field := e.Namespace()
	if idx := strings.Index(field, "."); idx >= 0 {
		field = field[idx+1:]
	}

	var msg string

	switch e.Tag() {
	case "required", "required_if":
		msg = fmt.Sprintf("%s is required", field)
	case "oneof":
		msg = fmt.Sprintf("%s must be one of [%s]", field, e.Param())
	case "min":
		msg = fmt.Sprintf("%s must be at least %s", field, e.Param())
	case "max":
		msg = fmt.Sprintf("%s must be at most %s", field, e.Param())
	case "url":
		msg = fmt.Sprintf("%s must be a valid URL", field)
	default:
		msg = fmt.Sprintf("%s failed validation (%s)", field, e.Tag())
	}

	if unit != "" {
		return fmt.Errorf("%s: %s", unit, msg)
	}

	return fmt.Errorf("%s", msg)
}

const DefaultHTTPPort = "9999"

func applyHTTPDefaults(h *HTTPConfig) {
	if h.Listen == "" {
		return
	}

	h.Listen = ensureHTTPPort(h.Listen)

	if h.ReadTimeout == 0 {
		h.ReadTimeout = 5 * time.Second
	}

	if h.WriteTimeout == 0 {
		h.WriteTimeout = 5 * time.Second
	}

	if h.ShutdownTimeout == 0 {
		h.ShutdownTimeout = 5 * time.Second
	}
}

func ensureHTTPPort(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return net.JoinHostPort(listen, DefaultHTTPPort)
	}

	if port == "" {
		host, _, _ := net.SplitHostPort(listen)

		return net.JoinHostPort(host, DefaultHTTPPort)
	}

	return listen
}

func (h *HTTPConfig) Enabled() bool {
	return h.Listen != ""
}

const (
	DefaultStartupTimeout = 30 * time.Second
	DefaultIdleTimeout    = 5 * time.Minute
)

func applySocketActivationDefaults(s *SocketActivationConfig) {
	if len(s.Protocol) == 0 {
		s.Protocol = []string{"tcp"}
	} else {
		s.Protocol = dedupeProtocols(s.Protocol)
	}

	if s.StartupTimeout == 0 {
		s.StartupTimeout = DefaultStartupTimeout
	}

	if s.IdleTimeout == 0 {
		s.IdleTimeout = DefaultIdleTimeout
	}

	applyHealthCheckDefaults(s.HealthChecks)

	s.Restart = restartPolicyWithDefaults(s.Restart)
}

func dedupeProtocols(protocols []string) []string {
	seen := make(map[string]struct{}, len(protocols))
	result := make([]string, 0, len(protocols))

	for _, p := range protocols {
		if _, ok := seen[p]; ok {
			continue
		}

		seen[p] = struct{}{}
		result = append(result, p)
	}

	return result
}

func validateSocketActivation(entries []SocketActivationConfig) error {
	listens := make(map[string]struct{}, len(entries))

	for i := range entries {
		e := &entries[i]

		for _, proto := range e.Protocol {
			key := fmt.Sprintf("%s/%s", proto, e.Listen)

			if _, exists := listens[key]; exists {
				return fmt.Errorf("duplicate socket_activation listener %s %q", proto, e.Listen)
			}

			listens[key] = struct{}{}
		}
	}

	return nil
}

func applyHealthCheckDefaults(checks []HealthCheck) {
	for i := range checks {
		if checks[i].Interval == 0 {
			checks[i].Interval = 10 * time.Second
		}
		if checks[i].Timeout == 0 {
			checks[i].Timeout = 5 * time.Second
		}
		if checks[i].Retries == 0 {
			checks[i].Retries = 3
		}
	}
}

func applyDefaults(u *UnitConfig) {
	if u.Priority == 0 {
		u.Priority = DefaultPriority
	}

	applyHealthCheckDefaults(u.HealthChecks)

	u.Restart = restartPolicyWithDefaults(u.Restart)
}

// restartPolicyWithDefaults returns policy with the standard backoff and
// cooldown filled in. A nil policy defaults to enabled, matching the behavior
// units and monitored socket-activation backends rely on.
func restartPolicyWithDefaults(policy *RestartPolicy) *RestartPolicy {
	if policy == nil {
		policy = &RestartPolicy{Enabled: true}
	}

	if policy.Backoff == 0 {
		policy.Backoff = 5 * time.Second
	}
	if policy.Cooldown == 0 {
		policy.Cooldown = 60 * time.Second
	}

	return policy
}
