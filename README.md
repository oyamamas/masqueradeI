# Masquerade I

A local SOCKS5 proxy that distributes new TCP connections across SSH tunnels.

<img src="static/msqi.svg" width="200" align="right">

The original iptables-based idea comes from [TREVORproxy](https://github.com/blacklanternsecurity/TREVORproxy). This project implements it in Go using OpenSSH dynamic forwarding and a dedicated NAT chain.

## Requirements

- Linux, root privileges, and an IPv4 loopback interface.
- `iptables` and `iptables-restore` from the same installation/backend, with NAT and `statistic` support.
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
sudo env SSH_AUTH_SOCK="$SSH_AUTH_SOCK" ./build/masqueradeI proxy \
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
sudo ./build/masqueradeI proxy --port 1337 --attach 1444
```

`--ssh` and `--attach` can be repeated and combined. Attached proxies must accept SOCKS5 without authentication at `127.0.0.1:port`; a listener only on another interface or on `::1` will not work. The facade port must differ from every attached port.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--port`, `-p` | `1337` | Local IPv4 facade port |
| `--ssh`, `-s` | — | `[user@]host`, `[user@]host:port`, or `[user@][IPv6]:port` |
| `--attach`, `-a` | — | Existing local SOCKS5 port |
| `--startup-timeout` | `15s` | Readiness timeout per SSH tunnel |
| `--health-interval` | `2s` | Interval between local SOCKS health checks |

SSH runs non-interactively (`BatchMode=yes`), so configure keys or an agent before starting. A destination without an explicit port honors SSH configuration. Because the program runs under `sudo`, SSH normally reads root's configuration and known-hosts files. New host keys are accepted; changed known host keys are rejected (`StrictHostKeyChecking=accept-new`).

## Behavior and failure handling

- Owned SSH listeners bind explicitly to `127.0.0.1`, using available local ports. The facade port is reserved to prevent two instances using it simultaneously.
- A SOCKS5 handshake is required before publishing any tunnel. Failed startup attempts are reported and skipped; startup fails if no working tunnels remain.
- The NAT chain is populated before the OUTPUT jump is installed. Changes to the active destinations are committed together with `iptables-restore --noflush`, preserving unrelated chains.
- SSH process exits are detected immediately. Periodic checks detect closed or unresponsive local SOCKS listeners, including attached proxies. These checks verify the local SOCKS service, not connectivity to every destination; SSH keepalives also detect a broken SSH transport.
- Failed tunnels are removed from new-connection balancing. They are not automatically restarted or reattached. If all tunnels fail, the proxy exits and cleans up.
- SIGINT, SIGTERM, and startup errors trigger cleanup. Only owned SSH processes are stopped; attached proxies stay running. Firewall cleanup failures are reported. SIGKILL and machine crashes cannot run cleanup.

NAT chooses a tunnel for each **new TCP connection**, not each packet or HTTP request. An existing connection remains on its original tunnel. HTTP keep-alive and HTTP/2 can therefore reuse one exit IP for many requests. Connections already using a failed tunnel cannot migrate; clients must reconnect. Only local TCP traffic to `127.0.0.1:--port` is balanced; the facade is not exposed to other machines and does not support IPv6 or UDP.

## Development

```sh
go test -race ./...
go vet ./...
go build ./...
```

Unit tests use loopback sockets, mock SSH children, and a modeled firewall. They never change system firewall rules or connect to real SSH servers.

The Linux integration test exercises real NAT distribution, destination removal, transaction failure, cleanup, and preservation of an unrelated chain. Run it only in a separate network namespace:

```sh
go test -c -o /tmp/msqi-integration.test ./cmd
sudo unshare --net -- sh -c '
  ip link set lo up
  MSQI_IPTABLES_TEST=1 /tmp/msqi-integration.test -test.run=^TestLinuxFirewallIntegration$ -test.v
'
```

GitHub Actions runs both the unit checks and this isolated integration test.
