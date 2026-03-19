# systemd-supervisord

A supervisor daemon for systemd services and timers. It monitors unit health, automatically restarts failed services with exponential backoff, and sends notifications on state changes.

## Features

- **Health Checks** -- TCP, HTTP, Unix socket, and script-based health checks with configurable intervals, timeouts, and retries
- **HTTP Health Check Options** -- Custom method, expected status code, response body matching, and custom headers
- **Automatic Restart** -- Restart unhealthy services with exponential backoff and cooldown periods
- **Template Units** -- Support for systemd template/instanced units (`name@instance.service`) with explicit instances or auto-discovery
- **Auto-Discovery** -- Automatically detect running instances of template units at a configurable interval
- **Service Dependencies** -- Define startup ordering with `depends_on`, circular dependency detection, and cascade restart of dependents
- **Grace Period** -- Delay health check start for services with slow startup
- **Notifications** -- Webhooks, scripts, and exec actions triggered on state and health changes
- **Systemd Integration** -- sd_notify protocol support with watchdog, D-Bus API with systemctl fallback
- **CLI Control** -- List, status, start, stop, and restart units via Unix socket IPC

## Installation

Build from source:

```sh
go build -o systemd-supervisord .
sudo cp systemd-supervisord /usr/local/bin/
```

Install the systemd service:

```sh
sudo cp dist/systemd-supervisord.service /etc/systemd/system/
sudo systemctl daemon-reload
```

## Configuration

Create the configuration file at `/etc/systemd-supervisord/config.yaml`. See [dist/config.example.yaml](dist/config.example.yaml) for a full example.

### Top-level options

| Option               | Description                                    | Default                            |
|----------------------|------------------------------------------------|------------------------------------|
| `log_level`          | Log level (`debug`, `info`, `warn`, `error`)   | `info`                             |
| `socket`             | Unix socket path for CLI communication         | `/var/run/systemd-supervisord.sock`|
| `discovery_interval` | How often to discover new template instances    | `30s`                              |

### Unit configuration

```yaml
units:
  - name: nginx
    type: service          # service or timer
    enabled: true
    grace_period: 30s      # delay before health checks start
    depends_on:            # units that must be started first
      - mydb
    health_checks:
      - type: http         # tcp, http, unix, or script
        interval: 10s
        timeout: 5s
        retries: 3
        http:                             # type-specific config
          address: http://localhost:80
          method: GET                     # GET, HEAD, POST, PUT
          expected_status: 200            # specific HTTP status code
          response_match: '"status":"ok"' # substring match in body
          headers:                        # custom request headers
            Authorization: "Bearer token"
    restart:
      enabled: true
      backoff: 5s          # initial backoff between restarts
      cooldown: 60s        # minimum time between restart cycles
```

### Template units

In systemd, template instances are independent units. Configure each as a separate entry:

```yaml
- name: myapp@shard0
  type: service
  enabled: true
- name: myapp@shard1
  type: service
  enabled: true
```

For dynamic instances, use auto-discovery on a template name (ending with `@`):

```yaml
- name: worker@
  type: service
  enabled: true
  discover: true
```

Discovered instances can use `{{instance}}` in health check addresses:

```yaml
health_checks:
  - type: http
    interval: 10s
    timeout: 5s
    retries: 3
    http:
      address: "http://localhost:{{instance}}/health"
```

### Notifications

```yaml
notify:
  variables:              # custom key-value pairs for all notifications
    hostname: web-01
    environment: production
  webhooks:
    - url: http://alerting.example.com/webhook
      timeout: 10s
  scripts:
    - path: /usr/local/bin/notify-on-failure.sh
      timeout: 30s
  execs:
    - command: "logger -t supervisord 'Unit $SUPERVISORD_UNIT_NAME state: $SUPERVISORD_ACTIVE_STATE'"
      timeout: 10s
      events:             # optional filter: state_changed, health_changed
        - state_changed
```

Scripts and exec actions receive the following environment variables:

| Variable                    | Description                                                  |
|-----------------------------|--------------------------------------------------------------|
| `SUPERVISORD_EVENT_TYPE`    | Event type: `state_changed` or `health_changed`.             |
| `SUPERVISORD_UNIT_NAME`     | Full unit name (e.g., `nginx.service`).                      |
| `SUPERVISORD_ACTIVE_STATE`  | systemd active state (e.g., `active`, `failed`).             |
| `SUPERVISORD_SUB_STATE`     | Detailed unit state: `running`, `dead`, `exited`, `waiting`, `listening`, `start-pre`, `auto-restart`, etc. |
| `SUPERVISORD_HEALTHY`       | Health status: `true` or `false`. Not set if unit has no health checks. |
| `SUPERVISORD_TIMESTAMP`     | Event timestamp in RFC 3339 format.                          |
| `SUPERVISORD_VAR_<KEY>`     | Custom variables from `notify.variables`. Keys are uppercased. |

## CLI Usage

```
systemd-supervisord [command]

Commands:
  run         Start the supervisor daemon
  check       Validate configuration file
  list        List supervised units
  status      Show status of supervised units
  start       Start a supervised unit
  stop        Stop a supervised unit
  restart     Restart a supervised unit
  version     Print version information

Flags:
  -c, --config string   config file path (default "/etc/systemd-supervisord/config.yaml")
  -s, --socket string   daemon socket path (default "/var/run/systemd-supervisord.sock")
```

## Systemd Service

The daemon runs as a systemd service with:

- `Type=notify` -- reports readiness via sd_notify
- `WatchdogSec=30s` -- periodic watchdog keepalive
- `ConditionPathExists` -- skips start if config file is missing
- `ExecStartPre` -- validates config before starting
- `Restart=on-failure` -- systemd restarts the daemon itself on failure

