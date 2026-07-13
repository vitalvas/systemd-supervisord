# Configuration Reference

systemd-supervisord is configured via a YAML file. The default path is `/etc/systemd-supervisord/config.yaml`.

Validate the configuration before starting the daemon:

```sh
systemd-supervisord check
systemd-supervisord check -c /path/to/config.yaml
```

Run the daemon in dry-run mode to observe behavior without performing any actions (start, stop, restart):

```sh
systemd-supervisord run --dry-run
```

In dry-run mode, the daemon loads configuration, discovers units, evaluates health checks, and watches for state changes, but all mutating operations are replaced with log messages. This is useful for validating configuration in production before enabling active supervision.

## Top-Level Options

| Option               | Type     | Required | Default                              | Description                                      |
|----------------------|----------|----------|--------------------------------------|--------------------------------------------------|
| `log_level`          | string   | No       | `info`                               | Log verbosity. One of: `debug`, `info`, `warn`, `error`. |
| `socket`             | string   | No       | `/var/run/systemd-supervisord.socket`  | Unix socket path for CLI-to-daemon communication. |
| `discovery_interval` | duration | No       | `30s`                                | How often to scan for new template unit instances. Minimum: `5s`. |
| `units`              | map      | Yes      | --                                   | Map of unit configurations keyed by `name.type`. At least one unit is required. |
| `notify`             | object   | No       | --                                   | Notification configuration.                      |

```yaml
log_level: info
socket: /var/run/systemd-supervisord.socket
discovery_interval: 30s
units: {}
notify: {}
```

## Unit Configuration

Each entry in the `units` map defines a systemd unit to supervise. The map key is the fully-qualified unit name in `name.type` format (e.g., `nginx.service`, `backup.timer`). Names ending with `@` before the suffix are auto-discovered as template units. Use `name@{regex}.type` to filter instances by pattern.

| Option          | Type     | Required | Default | Description                                                |
|-----------------|----------|----------|---------|------------------------------------------------------------|
| `enabled`       | bool     | No       | `true`  | Whether this unit is actively supervised.                  |
| `priority`      | int      | No       | `999`   | Startup ordering priority. Lower values start first. Minimum: `1`. |
| `depends_on`    | list     | No       | --      | Unit names that must start before this unit.               |
| `grace_period`  | duration | No       | `0s`    | Delay before health checks begin. Useful for slow-starting services. |
| `max_delay`     | duration | No       | --      | Maximum allowed time since last timer trigger. Timer-only. If exceeded, the timer is restarted. Minimum: `1s`. |
| `health_checks` | list     | No       | --      | List of health check configurations.                       |
| `restart`       | object   | No       | --      | Restart policy for unhealthy units.                        |

### Static Units

A static unit monitors a single systemd unit directly:

```yaml
units:
  nginx.service:
    enabled: true
```

This supervises `nginx.service`.

### Timer Units

Timer units monitor systemd timers. Since timers do not have network endpoints, health checks are not applicable. Use timer supervision to track state changes and receive notifications when a timer fails:

```yaml
units:
  certbot-renew.timer:
    enabled: true
  logrotate.timer:
    enabled: true
```

This supervises `certbot-renew.timer` and `logrotate.timer`.

### Timer Execution Monitoring

Use `max_delay` to ensure a timer has triggered within a required time window. The daemon periodically checks `LastTriggerUSec` from systemd. If the elapsed time since the last trigger exceeds `max_delay`, the timer is restarted:

```yaml
units:
  certbot-renew.timer:
    enabled: true
    max_delay: 48h
```

If `certbot-renew.timer` has not triggered in the last 48 hours, the daemon restarts it. The check interval follows the `discovery_interval` setting. Timers that have never triggered (zero last trigger) are skipped.

### Template Units

In systemd, template instances like `myapp@shard0.service` and `myapp@shard1.service` are independent units. Configure each as a separate entry:

```yaml
units:
  myapp@shard0.service:
    enabled: true
  myapp@shard1.service:
    enabled: true
```

### Auto-Discovery

Unit names ending with `@` are automatically treated as template units. The daemon discovers all running instances matching the template:

```yaml
units:
  worker@.service: {}
```

The daemon scans for running `worker@*.service` instances every `discovery_interval`. New instances are picked up automatically.

### Instance Pattern Filtering

Use `name@{regex}` to restrict discovery to instances matching a regular expression. The regex is matched against the full instance name:

```yaml
units:
  "runtime@{app-[a-z]+[0-9]+}.service": {}
```

This discovers only instances like `runtime@app-web1.service` or `runtime@app-api2.service`, ignoring `runtime@db-main.service`.

### Priority

Controls the order in which units are registered at startup. Lower values are registered first. Units with the same priority preserve their config file order. Default is `999`.

```yaml
units:
  database.service:
    priority: 1
  cache.service:
    priority: 10
  webapp.service:
    priority: 100
```

### Dependencies

Units can declare dependencies on other units. Dependencies are validated at config load time:

- All referenced units must exist in the configuration.
- Circular dependencies are detected and rejected.
- When a unit is restarted, all units that depend on it are cascade-restarted.

Dependencies can use bare names or fully-qualified names (`name.type`). Bare names resolve to the same type as the dependent unit. Use qualified names when you need to depend on a unit with a different type:

```yaml
units:
  mydb.service: {}

  elasticsearch.service:
    depends_on:
      - mydb

  backup.service: {}
  backup.timer: {}
  scheduler.service:
    depends_on:
      - backup.timer
```

### Priority and Dependencies

`priority` and `depends_on` serve different purposes and can be used together:

- **`priority`** controls the startup registration order. Units with lower priority are registered first.
- **`depends_on`** controls cascade restarts. When a dependency fails and is restarted, all units that depend on it are restarted too.

```yaml
units:
  mydb.service:
    priority: 1
  cache.service:
    priority: 10
  webapp.service:
    priority: 100
    depends_on:
      - mydb
      - cache
```

In this example, `mydb` is registered first (priority 1), then `cache` (10), then `webapp` (100). If `mydb` is restarted, `webapp` is cascade-restarted because it depends on `mydb`.

### Grace Period

Delays the start of health checks after unit registration. This is useful for services that take time to initialize:

```yaml
units:
  elasticsearch.service:
    enabled: true
    grace_period: 30s
```

Health checks for this unit will not begin until 30 seconds after registration.

## Health Checks

Each unit can have one or more health checks. A unit is considered healthy only when **all** its health checks pass. When any health check fails after exhausting retries, the unit is marked unhealthy.

Each health check has common options and a type-specific configuration block.

### Common Options

These options apply to all health check types:

| Option     | Type     | Required | Default | Description                                  |
|------------|----------|----------|---------|----------------------------------------------|
| `type`     | string   | Yes      | --      | One of: `tcp`, `http`, `unix`, `script`.     |
| `interval` | duration | Yes      | `10s`   | Time between health check attempts. Minimum: `1s`. |
| `timeout`  | duration | Yes      | `5s`    | Maximum time to wait for a check to complete. Minimum: `1s`. |
| `retries`  | int      | Yes      | `3`     | Number of consecutive failures before marking unhealthy. Minimum: `1`. |

Type-specific options are nested under a key matching the `type` value.

### TCP Health Check

Attempts a TCP connection to the specified address:

```yaml
health_checks:
  - type: tcp
    interval: 10s
    timeout: 5s
    retries: 3
    tcp:
      address: localhost:5432
```

| Option    | Type   | Required | Description             |
|-----------|--------|----------|-------------------------|
| `address` | string | Yes      | Host and port to connect to. |

### HTTP Health Check

Sends an HTTP request and validates the response:

```yaml
health_checks:
  - type: http
    interval: 15s
    timeout: 10s
    retries: 3
    http:
      address: http://localhost:9200/_cluster/health
      method: GET
      expected_status: 200
      response_match: '"status":"green"'
      headers:
        Authorization: "Basic ZWxhc3RpYzpjaGFuZ2VtZQ=="
        Accept: application/json
```

| Option            | Type            | Required | Default | Description                                                      |
|-------------------|-----------------|----------|---------|------------------------------------------------------------------|
| `address`         | string          | Yes      | --      | Full URL to request.                                             |
| `method`          | string          | No       | `GET`   | HTTP method. One of: `GET`, `HEAD`, `POST`, `PUT`.               |
| `expected_status` | int             | No       | --      | Expected HTTP status code (100-599). If omitted, any 2xx status is accepted. |
| `response_match`  | string          | No       | --      | Substring that must be present in the response body. Body is read up to 128 KB. |
| `headers`         | map[string]string | No     | --      | Custom HTTP headers to include in the request.                   |

### Unix Socket Health Check

Verifies that a process is listening on a Unix domain socket by attempting to connect:

```yaml
health_checks:
  - type: unix
    interval: 10s
    timeout: 5s
    retries: 3
    unix:
      address: /var/run/myapp.socket
```

| Option    | Type   | Required | Description                |
|-----------|--------|----------|----------------------------|
| `address` | string | Yes      | Path to the Unix socket.   |

### Script Health Check

Runs a shell command. Exit code 0 means healthy, non-zero means unhealthy:

```yaml
health_checks:
  - type: script
    interval: 15s
    timeout: 10s
    retries: 3
    script:
      command: "pg_isready -h localhost -p 5432"
```

| Option    | Type   | Required | Description                           |
|-----------|--------|----------|---------------------------------------|
| `command` | string | Yes      | Shell command to execute via `sh -c`. |

### Instance Placeholder

For discovered template units, use `{{instance}}` in health check addresses. It is replaced with the actual instance name:

```yaml
units:
  worker@.service:
    health_checks:
      - type: http
        interval: 10s
        timeout: 5s
        retries: 3
        http:
          address: "http://localhost:{{instance}}/health"
```

For a discovered instance `worker@8080.service`, the address becomes `http://localhost:8080/health`.

## Restart Policy

Controls automatic restart behavior for unhealthy units. Restart uses exponential backoff that doubles the delay on each consecutive failure, up to a maximum determined by the cooldown.

| Option     | Type     | Required | Default | Description                                             |
|------------|----------|----------|---------|---------------------------------------------------------|
| `enabled`  | bool     | No       | `true`  | Enable automatic restart.                               |
| `backoff`  | duration | No       | `5s`    | Initial delay between restart attempts. Minimum: `1s`.  |
| `cooldown` | duration | No       | `60s`   | Minimum time between restart cycles. Minimum: `1s`.     |

```yaml
restart:
  enabled: true
  backoff: 10s
  cooldown: 120s
```

## Notifications

The `notify` section configures how the daemon reports state and health changes. All notification types support an optional `events` filter.

### Variables

User-defined key-value pairs included in all notifications. Variables are added to webhook JSON payloads as a `variables` object and to scripts/exec actions as environment variables with the `SUPERVISORD_VAR_` prefix (keys are uppercased).

```yaml
notify:
  variables:
    hostname: web-01
    environment: production
```

This produces:
- Webhook JSON: `"variables": {"hostname": "web-01", "environment": "production"}`
- Environment variables: `SUPERVISORD_VAR_HOSTNAME=web-01`, `SUPERVISORD_VAR_ENVIRONMENT=production`

| Option      | Type            | Required | Description                          |
|-------------|-----------------|----------|--------------------------------------|
| `variables` | map[string]string | No     | Custom key-value pairs for notifications. |

### Event Types

| Event             | Description                                            |
|-------------------|--------------------------------------------------------|
| `state_changed`   | Unit active/sub state changed (e.g., active/running to failed/failed). |
| `health_changed`  | Unit health status changed (healthy to unhealthy or vice versa). |

If `events` is omitted, the notification fires on all event types.

### Webhooks

Sends an HTTP POST with a JSON payload to the configured URL:

```yaml
notify:
  webhooks:
    - url: http://alerting.example.com/webhook
      timeout: 10s
      events:
        - state_changed
        - health_changed
```

| Option    | Type     | Required | Default | Description                              |
|-----------|----------|----------|---------|------------------------------------------|
| `url`     | string   | Yes      | --      | Webhook endpoint URL. Must be a valid URL. |
| `timeout` | duration | No       | --      | Request timeout. Minimum: `1s`.          |
| `events`  | list     | No       | all     | Event types to notify on.                |

#### JSON Payload

The webhook sends a JSON object with the following fields:

| Field          | Type   | Description                                                              |
|----------------|--------|--------------------------------------------------------------------------|
| `event_type`   | string | `state_changed` or `health_changed`.                                     |
| `unit_name`    | string | Full unit name (e.g., `nginx.service`).                                  |
| `active_state` | string | systemd active state (e.g., `active`, `failed`). Present on `state_changed` events. |
| `sub_state`    | string | Detailed unit state (e.g., `running`, `dead`). Present on `state_changed` events. |
| `healthy`      | bool   | `true` or `false`. Present on `health_changed` events. Omitted if unit has no health checks. |
| `timestamp`    | string | Event timestamp in RFC 3339 format.                                      |
| `variables`    | object | Custom variables from `notify.variables`. Omitted if no variables are configured. |

Example `state_changed` payload:

```json
{
  "event_type": "state_changed",
  "unit_name": "nginx.service",
  "active_state": "failed",
  "sub_state": "failed",
  "timestamp": "2026-03-19T14:30:00Z",
  "variables": {
    "hostname": "web-01",
    "environment": "production"
  }
}
```

Example `health_changed` payload:

```json
{
  "event_type": "health_changed",
  "unit_name": "nginx.service",
  "healthy": false,
  "timestamp": "2026-03-19T14:30:05Z",
  "variables": {
    "hostname": "web-01",
    "environment": "production"
  }
}
```

### Scripts

Executes a script on events. The script receives event data via environment variables:

```yaml
notify:
  scripts:
    - path: /usr/local/bin/notify-on-failure.sh
      timeout: 30s
      events:
        - state_changed
```

| Option    | Type     | Required | Default | Description                             |
|-----------|----------|----------|---------|-----------------------------------------|
| `path`    | string   | Yes      | --      | Absolute path to the script.            |
| `timeout` | duration | No       | --      | Execution timeout. Minimum: `1s`.       |
| `events`  | list     | No       | all     | Event types to notify on.               |

### Exec Actions

Runs a shell command on events. The command is executed via `sh -c`:

```yaml
notify:
  execs:
    - command: "logger -t supervisord 'Unit $SUPERVISORD_UNIT_NAME state: $SUPERVISORD_ACTIVE_STATE'"
      timeout: 10s
      events:
        - state_changed
    - command: "/usr/local/bin/restart-hook.sh"
      timeout: 30s
```

| Option    | Type     | Required | Default | Description                              |
|-----------|----------|----------|---------|------------------------------------------|
| `command` | string   | Yes      | --      | Shell command to execute.                |
| `timeout` | duration | No       | --      | Execution timeout. Minimum: `1s`.        |
| `events`  | list     | No       | all     | Event types to notify on.                |

### Environment Variables

Scripts and exec actions receive the following environment variables:

| Variable                    | Description                                                  |
|-----------------------------|--------------------------------------------------------------|
| `SUPERVISORD_EVENT_TYPE`    | Event type: `state_changed` or `health_changed`.             |
| `SUPERVISORD_UNIT_NAME`     | Full unit name (e.g., `nginx.service`).                      |
| `SUPERVISORD_ACTIVE_STATE`  | systemd active state (e.g., `active`, `failed`).             |
| `SUPERVISORD_SUB_STATE`     | Detailed unit state: `running`, `dead`, `exited`, `waiting`, `listening`, `start-pre`, `auto-restart`, etc. |
| `SUPERVISORD_HEALTHY`       | Health status: `true` or `false`. Not set if unit has no health checks. |
| `SUPERVISORD_TIMESTAMP`     | Event timestamp in RFC 3339 format.                          |

## Socket Activation

systemd-supervisord ships with a systemd socket unit (`systemd-supervisord.socket`) that is installed to `/lib/systemd/system/`. When enabled, systemd creates and manages the socket, passing it to the daemon on startup. This enables on-demand startup and zero-downtime restarts.

### How It Works

On startup, the daemon checks for file descriptors passed by systemd via the `LISTEN_FDS` and `LISTEN_PID` environment variables. If a socket is available, the daemon uses it directly. If no systemd-provided socket is detected, the daemon falls back to creating the socket manually using the `socket` path from the configuration.

### Usage

Enable the socket unit:

```sh
systemctl enable --now systemd-supervisord.socket
systemctl start systemd-supervisord.service
```

The socket listens on `/var/run/systemd-supervisord.socket` with mode `0660`.

### Manual Socket Creation

If the socket unit is not enabled, the daemon creates the Unix socket at the path specified by the `socket` config option, removes any existing file at that path, and sets permissions to `0660`.

## Duration Format

All duration fields use Go duration syntax: `5s`, `10m`, `1h30m`, `500ms`.

Common values:

| Value   | Meaning       |
|---------|---------------|
| `1s`    | 1 second      |
| `5s`    | 5 seconds     |
| `30s`   | 30 seconds    |
| `1m`    | 1 minute      |
| `5m`    | 5 minutes     |
| `1h`    | 1 hour        |
