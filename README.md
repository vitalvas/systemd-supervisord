# systemd-supervisord

A supervisor daemon for systemd services and timers. It monitors unit health, automatically restarts failed services with exponential backoff, and sends notifications on state changes.

## Features

- **Health Checks** -- TCP, HTTP, Unix socket, and script-based health checks with configurable intervals, timeouts, and retries
- **HTTP Health Check Options** -- Custom method, expected status code, response body matching, and custom headers
- **Automatic Restart** -- Restart unhealthy services with exponential backoff and cooldown periods
- **Template Units** -- Support for systemd template/instanced units (`name@instance.service`) with explicit instances or auto-discovery
- **Timer Execution Monitoring** -- Detect overdue timers via `max_delay` and automatically restart them
- **Auto-Discovery** -- Automatically detect running instances of template units at a configurable interval
- **Service Dependencies** -- Define startup ordering with `depends_on`, circular dependency detection, and cascade restart of dependents
- **Grace Period** -- Delay health check start for services with slow startup
- **Notifications** -- Webhooks, scripts, and exec actions triggered on state and health changes
- **Systemd Integration** -- sd_notify protocol support with watchdog, D-Bus API with systemctl fallback
- **CLI Control** -- List, status, start, stop, and restart units via Unix socket IPC
- **HTTP Health Endpoint** -- Optional HTTP server exposing `/health`, `/ready`, `/live` for external probes (e.g. AWS ASG, Kubernetes)
- **Socket Activation** -- Listen on a public/local port, start a systemd unit on the first connection, wait until the backend is healthy, proxy traffic, and stop the unit after an idle timeout

## Configuration

Create the configuration file at `/etc/systemd-supervisord/config.yaml`.

### Top-level options

| Option               | Description                                    | Default                            |
|----------------------|------------------------------------------------|------------------------------------|
| `log_level`          | Log level (`debug`, `info`, `warn`, `error`)   | `info`                             |
| `socket`             | Unix socket path for CLI communication         | `/var/run/systemd-supervisord.socket`|
| `discovery_interval` | How often to discover new template instances    | `30s`                              |
| `http`               | HTTP health endpoint configuration (see below) | disabled                           |
| `socket_activation`  | On-demand unit activation with a TCP/UDP proxy, keyed by unit name (see below) | disabled |

### Unit configuration

```yaml
units:
  nginx.service:
    enabled: true
    critical: true         # include in HTTP /health aggregate
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

### Timer units

```yaml
units:
  certbot-renew.timer:
    enabled: true
    max_delay: 48h       # restart if not triggered within 48 hours
```

### Template units

In systemd, template instances are independent units. Configure each as a separate entry:

```yaml
units:
  myapp@shard0.service:
    enabled: true
  myapp@shard1.service:
    enabled: true
```

Names ending with `@` before the suffix are automatically discovered as template units. Use `name@{regex}.type` to filter instances by pattern:

```yaml
units:
  worker@.service: {}
  "runtime@{app-[a-z]+[0-9]+}.service": {}
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

### HTTP health endpoint

Expose health information over HTTP for external probes such as AWS ASG, ELB, or Kubernetes. Disabled by default; enable by setting `listen`.

```yaml
http:
  listen: 0.0.0.0          # host[:port] to bind; empty disables the server. Port defaults to 9999 if omitted.
  read_timeout: 5s         # HTTP read timeout (default 5s)
  write_timeout: 5s        # HTTP write timeout (default 5s)
  shutdown_timeout: 5s     # graceful shutdown timeout (default 5s)
```

Endpoints:

| Path     | Status   | Description                                                            |
|----------|----------|------------------------------------------------------------------------|
| `/health` | 200/503 | Aggregate of all units marked `critical: true`. Returns 503 if any critical unit is not `active` or has a failing health check. Returns 200 if no critical units are configured. |
| `/ready`  | 200/503 | Returns 200 once the daemon has finished initial unit registration. 503 during startup. |
| `/live`   | 200     | Always returns 200 while the server is serving. Use for liveness probes. |

All endpoints accept `GET` and `HEAD` and return a JSON body with `status`, `ready`, `timestamp`, and (for `/health`) a `units` list with current state.

Mark services you want included in the aggregate with `critical: true` in the unit config. When set on a template, all discovered instances inherit the flag.

### Socket activation

Lazily start a systemd unit on demand and proxy connections to it. Each entry is keyed by the systemd unit name. The supervisor listens on `listen`, and when the first client connects it starts the unit, waits until the backend is healthy, and relays traffic to `backend`. When there are no active connections and no traffic for `idle_timeout`, the unit is stopped again. This is useful for GPU-heavy or otherwise expensive backends (for example LLM runners) that should only run while in use.

```yaml
socket_activation:
  llama@gemma-4-e4b.service:                   # map key: systemd unit started on demand
    listen: "127.0.0.1:4101"                  # host:port the supervisor listens on
    protocol: [tcp]                            # tcp, udp, or both; default [tcp]
    backend: "127.0.0.1:5101"                 # host:port to proxy traffic to
    startup_timeout: 10m                       # max time to wait for the backend to become healthy (default 30s)
    idle_timeout: 15m                          # stop the unit after this much idle time (default 5m)
    health_checks:                             # optional readiness + liveness checks; same schema as unit health_checks
      - type: http
        timeout: 2s
        http:
          address: "http://127.0.0.1:5101/health"
    restart:                                   # optional; restart policy for the running backend (defaults to enabled)
      enabled: true
      backoff: 5s
      cooldown: 60s
```

Behavior:

- The map key is the systemd unit started on demand. A short log identifier is derived from it (the template instance for `name@instance.service`, otherwise the unit's base name).
- The proxy operates at layer 4 and is protocol-agnostic, so any backend that speaks over TCP or UDP works (HTTP, gRPC, DNS, and others).
- `protocol` selects which listeners to bind: `[tcp]` (default), `[udp]`, or `[tcp, udp]`. Each protocol binds its own listener on the same `listen` address, and all listeners share one on-demand unit start and one idle lifecycle.
- Readiness is detected by running `health_checks` (the same `tcp`/`http`/`unix`/`script` schema as unit health checks); the backend is considered ready only when every check passes. The startup poll cadence is the smallest configured check `interval`. When `health_checks` is omitted, the backend is treated as ready as soon as the unit is started.
- While the backend is running, `health_checks` keep running as liveness checks through the same monitoring and restart pipeline used for regular units: if a check exceeds its `retries`, the backend is restarted per the entry's `restart` policy (backoff and cooldown). Monitoring is active only while the backend is up; it is torn down when the unit is stopped for idleness, so the restarter never fights the idle-stop. When `health_checks` is omitted there is nothing to monitor and the backend simply runs until idle.
- The `restart` block is optional and uses the same schema as a unit `restart` policy; it defaults to enabled with `5s` backoff and `60s` cooldown when `health_checks` are configured.
- Concurrent connections that arrive during startup share a single start attempt; the client connection blocks until the backend is healthy or `startup_timeout` elapses.
- Idle is measured by both active connections and last byte transferred: the unit is stopped only when there are no active connections (or UDP sessions) and no traffic in either direction for `idle_timeout`.
- If the backend does not become healthy within `startup_timeout`, the pending client connection is closed and the next connection retries.

UDP notes:

- UDP has no connection to close, so each client source address becomes a short-lived session with its own backend socket. Replies are routed back to the client while the session is live. Sessions expire after a period of inactivity and are re-established automatically on the next datagram.
- The first datagram that wakes the unit is not answered if the backend is not yet healthy; the client's normal retry (as DNS resolvers do) lands on the now-ready backend.

Each unit key must be unique, and each `protocol`/`listen` pair must be unique across entries (the same address may serve both `tcp` and `udp`).

Example: on-demand DNS resolver serving UDP and TCP on port 53:

```yaml
socket_activation:
  coredns.service:
    listen: "127.0.0.1:53"
    protocol: [udp, tcp]
    backend: "127.0.0.1:5353"
    startup_timeout: 30s
    idle_timeout: 15m
```

Inspect the live state of all listeners with the `sockets` CLI command:

```
$ systemd-supervisord sockets
UNIT                       LISTEN            PROTOCOL  BACKEND           STATE    CONNS  IDLE
coredns.service            127.0.0.1:53      udp,tcp   127.0.0.1:5353    running  0      42s
llama@gemma-4-e4b.service  127.0.0.1:4101    tcp       127.0.0.1:5101    stopped  0      -
```

`STATE` is `running` when the backend unit is active or `stopped` when idle. `CONNS` is the current number of active connections (TCP) or sessions (UDP). `IDLE` is the time since the last traffic while running and idle, or `-` when a connection is active or the unit is stopped.

## CLI Usage

```
systemd-supervisord [command]

Commands:
  run         Start the supervisor daemon
  check       Validate configuration file
  list        List supervised units
  status      Show status of supervised units
  sockets     Show socket-activation listeners and their state
  start       Start a supervised unit
  stop        Stop a supervised unit
  restart     Restart a supervised unit
  version     Print version information

Flags:
  -c, --config string   config file path (default "/etc/systemd-supervisord/config.yaml")
  -s, --socket string   daemon socket path (default "/var/run/systemd-supervisord.socket")
```

## Systemd Service

The daemon runs as a systemd service with:

- `Type=notify` -- reports readiness via sd_notify
- `WatchdogSec=30s` -- periodic watchdog keepalive
- `ConditionPathExists` -- skips start if config file is missing
- `ExecStartPre` -- validates config before starting
- `Restart=on-failure` -- systemd restarts the daemon itself on failure

