package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/vitalvas/systemd-supervisord/internal/config"
	"github.com/vitalvas/systemd-supervisord/internal/statemanager"
)

type EventPayload struct {
	EventType   string            `json:"event_type"`
	UnitName    string            `json:"unit_name"`
	ActiveState string            `json:"active_state,omitempty"`
	SubState    string            `json:"sub_state,omitempty"`
	Healthy     *bool             `json:"healthy,omitempty"`
	Timestamp   string            `json:"timestamp"`
	Variables   map[string]string `json:"variables,omitempty"`
}

type Notifier struct {
	variables map[string]string
	webhooks  []config.WebhookConfig
	scripts   []config.ScriptConfig
	execs     []config.ExecConfig
}

func New(cfg config.NotifyConfig) *Notifier {
	return &Notifier{
		variables: cfg.Variables,
		webhooks:  cfg.Webhooks,
		scripts:   cfg.Scripts,
		execs:     cfg.Execs,
	}
}

func (n *Notifier) HandleEvent(ev statemanager.Event) {
	payload := EventPayload{
		UnitName:  ev.UnitName,
		Timestamp: ev.Timestamp.UTC().Format(time.RFC3339),
		Variables: n.variables,
	}

	switch ev.Type {
	case statemanager.EventStateChanged:
		payload.EventType = "state_changed"
		payload.ActiveState = ev.ActiveState
		payload.SubState = ev.SubState
	case statemanager.EventHealthChanged:
		payload.EventType = "health_changed"
		payload.Healthy = ev.Healthy
	}

	for _, wh := range n.webhooks {
		if matchesFilter(wh.Events, payload.EventType) {
			go n.sendWebhook(wh, payload)
		}
	}

	for _, sc := range n.scripts {
		if matchesFilter(sc.Events, payload.EventType) {
			go n.runScript(sc, payload)
		}
	}

	for _, ec := range n.execs {
		if matchesFilter(ec.Events, payload.EventType) {
			go n.runExec(ec, payload)
		}
	}
}

func matchesFilter(filter []string, eventType string) bool {
	if len(filter) == 0 {
		return true
	}

	return slices.Contains(filter, eventType)
}

func (n *Notifier) sendWebhook(wh config.WebhookConfig, payload EventPayload) {
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("marshaling webhook payload", "error", err)

		return
	}

	timeout := wh.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		slog.Error("creating webhook request", "url", wh.URL, "error", err)

		return
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("sending webhook", "url", wh.URL, "error", err)

		return
	}

	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		slog.Warn("webhook returned non-success status", "url", wh.URL, "status", resp.StatusCode)
	}
}

func (n *Notifier) runScript(sc config.ScriptConfig, payload EventPayload) {
	timeout := sc.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, sc.Path)
	cmd.Env = append(cmd.Environ(),
		fmt.Sprintf("SUPERVISORD_EVENT_TYPE=%s", payload.EventType),
		fmt.Sprintf("SUPERVISORD_UNIT_NAME=%s", payload.UnitName),
		fmt.Sprintf("SUPERVISORD_ACTIVE_STATE=%s", payload.ActiveState),
		fmt.Sprintf("SUPERVISORD_SUB_STATE=%s", payload.SubState),
		fmt.Sprintf("SUPERVISORD_TIMESTAMP=%s", payload.Timestamp),
	)

	if payload.Healthy != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("SUPERVISORD_HEALTHY=%t", *payload.Healthy))
	}

	cmd.Env = appendVariablesEnv(cmd.Env, payload.Variables)

	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("running notification script",
			"path", sc.Path,
			"error", err,
			"output", string(output),
		)

		return
	}

	slog.Debug("notification script completed", "path", sc.Path)
}

func (n *Notifier) runExec(ec config.ExecConfig, payload EventPayload) {
	timeout := ec.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", ec.Command)
	cmd.Env = append(cmd.Environ(),
		fmt.Sprintf("SUPERVISORD_EVENT_TYPE=%s", payload.EventType),
		fmt.Sprintf("SUPERVISORD_UNIT_NAME=%s", payload.UnitName),
		fmt.Sprintf("SUPERVISORD_ACTIVE_STATE=%s", payload.ActiveState),
		fmt.Sprintf("SUPERVISORD_SUB_STATE=%s", payload.SubState),
		fmt.Sprintf("SUPERVISORD_TIMESTAMP=%s", payload.Timestamp),
	)

	if payload.Healthy != nil {
		cmd.Env = append(cmd.Env, fmt.Sprintf("SUPERVISORD_HEALTHY=%t", *payload.Healthy))
	}

	cmd.Env = appendVariablesEnv(cmd.Env, payload.Variables)

	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("running exec command",
			"command", ec.Command,
			"error", err,
			"output", string(output),
		)

		return
	}

	slog.Debug("exec command completed", "command", ec.Command)
}

func appendVariablesEnv(env []string, vars map[string]string) []string {
	for key, val := range vars {
		env = append(env, fmt.Sprintf("SUPERVISORD_VAR_%s=%s", strings.ToUpper(key), val))
	}

	return env
}
