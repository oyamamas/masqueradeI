# Masquerade I

A local SOCKS5 proxy that distributes new TCP connections across SSH tunnels.

<img src="static/msqi.svg" width="200" align="right">

The original idea comes from [TREVORproxy](https://github.com/blacklanternsecurity/TREVORproxy). Masquerade I uses a Go TCP relay and OpenSSH dynamic forwarding.

## Requirements

- macOS or Linux with an IPv4 loopback interface.
- OpenSSH client for `--ssh`. Existing SOCKS5 proxies can be used with `--attach` without an SSH client.
- Go 1.25.5 or later to build.

## Installation

```sh
go install github.com/oyamamas/masqueradeI@latest
```

The executable is named `masqueradeI` and is installed in `$(go env GOPATH)/bin` unless `GOBIN` is set. To build from a checkout:

```sh
go build -o build/masqueradeI .
```

## Usage

Start two tunnels, using an SSH agent for authentication:

```sh
./build/masqueradeI proxy \
  --port 1337 \
  --ssh user@proxy1 \
  --ssh user@proxy2:2222
```

Connect a client to the facade port. Use remote DNS resolution where supported:

```sh
curl --proxy socks5h://127.0.0.1:1337 https://example.com
```

Attach an existing proxy, for example one started separately with `tsh ssh -D 1444`:

```sh
./build/masqueradeI proxy --port 1337 --attach 1444
```

`--ssh` and `--attach` can be repeated and combined. Attached proxies must accept SOCKS5 without authentication at `127.0.0.1:port`; a listener only on another interface or on `::1` will not work. The facade port must differ from every attached port.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--port`, `-p` | `1337` | Local IPv4 facade port |
| `--ssh`, `-s` | — | `[user@]host`, `[user@]host:port`, or `[user@][IPv6]:port` |
| `--attach`, `-a` | — | Existing local SOCKS5 port |
| `--startup-timeout` | `15s` | Readiness timeout per SSH tunnel |
| `--health-interval` | `2s` | Interval between local SOCKS health checks |

SSH runs non-interactively (`BatchMode=yes`), so configure keys or an agent before starting. A destination without an explicit port honors SSH configuration. SSH uses the invoking user's configuration, agent, and known-hosts files. With `StrictHostKeyChecking=accept-new`, SSH accepts new host keys and rejects changed known host keys.

## Behavior and failure handling

- The TCP relay and its owned SSH listeners bind to `127.0.0.1`. The default relay port `1337` works without `sudo`.
- A SOCKS5 handshake is required before publishing any tunnel. Failed startup attempts are reported and skipped; startup fails if no working tunnels remain.
- SSH process exits are detected immediately. Periodic checks detect closed or unresponsive local SOCKS listeners, including attached proxies. These checks verify the local SOCKS service, not connectivity to every destination; SSH keepalives also detect a broken SSH transport.
- Failed tunnels are removed from new-connection balancing. They are not automatically restarted or reattached. If all tunnels fail, the proxy exits and cleans up.
- SIGINT, SIGTERM, and startup errors trigger cleanup. The relay closes its sockets and stops owned SSH processes. Attached proxies stay running. SIGKILL and machine crashes cannot run process cleanup.

The relay chooses a tunnel in round-robin order for each **new TCP connection** and forwards SOCKS traffic as a byte stream. An existing connection remains on its original tunnel. HTTP keep-alive and HTTP/2 can therefore reuse one exit IP for many requests. Connections already using a failed tunnel must reconnect. The relay serves local clients over IPv4 TCP at `127.0.0.1:--port`.

## Development

```sh
go test -race ./...
go vet ./...
go build ./...
```

Tests use loopback sockets and mock SSH children to exercise distribution, failed-tunnel removal, TCP half-close, and shutdown. GitHub Actions runs the checks on macOS and Linux.
