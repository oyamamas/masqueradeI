package cmd

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testConfig(t *testing.T) proxyConfig {
	t.Helper()
	port, err := allocateTunnelPort(nil)
	if err != nil {
		t.Fatal(err)
	}
	return proxyConfig{port: port, startupTimeout: 3 * time.Second, healthInterval: 50 * time.Millisecond}
}

type proxyLogs chan string

func (logs proxyLogs) Write(p []byte) (int, error) {
	logs <- string(p)
	return len(p), nil
}

func waitProxyLog(t *testing.T, logs proxyLogs, message string) {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case line := <-logs:
			if strings.Contains(line, message) {
				return
			}
		case <-timeout.C:
			t.Fatalf("timed out waiting for %q", message)
		}
	}
}

func waitProxy(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not shut down")
		return nil
	}
}

func TestProxyConfigValidation(t *testing.T) {
	base := proxyConfig{port: 1337, attach: []int{1444}, startupTimeout: time.Second, healthInterval: time.Second}
	for _, tc := range []struct {
		name   string
		change func(*proxyConfig)
	}{
		{"port zero", func(c *proxyConfig) { c.port = 0 }},
		{"port overflow", func(c *proxyConfig) { c.port = 65536 }},
		{"no tunnels", func(c *proxyConfig) { c.attach = nil }},
		{"attach facade", func(c *proxyConfig) { c.attach = []int{1337} }},
		{"duplicate attach", func(c *proxyConfig) { c.attach = []int{1444, 1444} }},
		{"bad attach", func(c *proxyConfig) { c.attach = []int{-1} }},
		{"bad SSH", func(c *proxyConfig) { c.ssh = []string{"user@host:abc"} }},
		{"timeout", func(c *proxyConfig) { c.startupTimeout = 0 }},
		{"interval", func(c *proxyConfig) { c.healthInterval = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.change(&cfg)
			if err := cfg.validate(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
}

func relayTestBackend(t *testing.T, name string) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				var greeting [3]byte
				if _, err := io.ReadFull(conn, greeting[:]); err != nil || greeting != [3]byte{5, 1, 0} {
					return
				}
				if _, err := conn.Write([]byte{5, 0}); err != nil {
					return
				}
				// Reply only after EOF so this also exercises TCP half-close.
				if payload, err := io.ReadAll(conn); err == nil && len(payload) > 0 {
					_, _ = io.WriteString(conn, name+":"+string(payload))
				}
			}()
		}
	}()
	return listener
}

func connectTestProxy(t *testing.T, port int) *net.TCPConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil || reply != [2]byte{5, 0} {
		t.Fatalf("SOCKS greeting failed: %v, %v", reply, err)
	}
	return conn.(*net.TCPConn)
}

func assertRelayedTo(t *testing.T, conn *net.TCPConn, backend string) {
	t.Helper()
	defer conn.Close()
	const payload = "proxy payload"
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(conn)
	if err != nil || string(reply) != backend+":"+payload {
		t.Fatalf("want reply from %s, got %q, %v", backend, reply, err)
	}
}

func TestProxyBalancesAndRemovesFailedAttachedTunnel(t *testing.T) {
	first := relayTestBackend(t, "first")
	second := relayTestBackend(t, "second")
	cfg := testConfig(t)
	cfg.attach = []int{listenerPort(first), listenerPort(second)}
	logs := make(proxyLogs, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runProxy(ctx, cfg, logs) }()
	waitProxyLog(t, logs, "(2 tunnels)")
	for _, backend := range []string{"first", "second", "first", "second"} {
		assertRelayedTo(t, connectTestProxy(t, cfg.port), backend)
	}
	established := connectTestProxy(t, cfg.port)
	_ = first.Close()
	waitProxyLog(t, logs, "Balancing across 1 remaining tunnels")
	assertRelayedTo(t, established, "first")
	for range 3 {
		assertRelayedTo(t, connectTestProxy(t, cfg.port), "second")
	}
	idle := connectTestProxy(t, cfg.port)
	cancel()
	if err := waitProxy(t, done); err != nil {
		t.Fatal(err)
	}
	if _, err := idle.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("expected idle session to close on shutdown, got %v", err)
	}
	if err := probeSOCKS(context.Background(), cfg.attach[1]); err != nil {
		t.Fatalf("attached tunnel was stopped: %v", err)
	}
}

func TestProxyAllAttachedTunnelsFail(t *testing.T) {
	listener := socksListener(t, [2]byte{5, 0})
	cfg := testConfig(t)
	cfg.attach = []int{listenerPort(listener)}
	logs := make(proxyLogs, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runProxy(ctx, cfg, logs) }()
	waitProxyLog(t, logs, "SOCKS proxy ready")
	_ = listener.Close()
	if err := waitProxy(t, done); err == nil || !strings.Contains(err.Error(), "all SOCKS tunnels stopped") {
		t.Fatalf("unexpected result: %v", err)
	}
}

func TestProxyDoesNotPublishFailedSSH(t *testing.T) {
	mockSSH(t)
	cfg := testConfig(t)
	cfg.ssh = []string{"fail.test"}
	if err := runProxy(context.Background(), cfg, io.Discard); err == nil || !strings.Contains(err.Error(), "no working SOCKS tunnels") {
		t.Fatal("expected no working tunnels error")
	}
}

func TestProxyRejectsNonSOCKSAttachedPort(t *testing.T) {
	listener := socksListener(t, [2]byte{'H', 'T'})
	cfg := testConfig(t)
	cfg.attach = []int{listenerPort(listener)}
	if err := runProxy(context.Background(), cfg, io.Discard); err == nil {
		t.Fatal("accepted non-SOCKS tunnel")
	}
}

func TestProxyRejectsOccupiedFacade(t *testing.T) {
	listener := socksListener(t, [2]byte{5, 0})
	cfg := testConfig(t)
	cfg.port = listenerPort(listener)
	cfg.ssh = []string{"host"}
	if err := runProxy(context.Background(), cfg, io.Discard); err == nil || !strings.Contains(err.Error(), "listen on proxy port") {
		t.Fatal("accepted an occupied facade port")
	}
}

func TestProxySSHExitRemovesDestination(t *testing.T) {
	dir := mockSSH(t)
	cfg := testConfig(t)
	cfg.ssh = []string{"first.test", "second.test"}
	cfg.healthInterval = time.Hour
	logs := make(proxyLogs, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runProxy(ctx, cfg, logs) }()
	waitProxyLog(t, logs, "(2 tunnels)")
	data, err := os.ReadFile(filepath.Join(dir, "first.test.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Release()
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitProxyLog(t, logs, "Balancing across 1 remaining tunnels")
	for range 3 {
		if err := probeSOCKS(ctx, cfg.port); err != nil {
			t.Fatalf("proxy failed to use remaining SSH tunnel: %v", err)
		}
	}
	cancel()
	if err := waitProxy(t, done); err != nil {
		t.Fatal(err)
	}
	assertChildStopped(t, dir, "first.test")
	assertChildStopped(t, dir, "second.test")
}

func TestProxyCancellationDuringStartup(t *testing.T) {
	dir := mockSSH(t)
	cfg := testConfig(t)
	cfg.ssh = []string{"hang.test"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runProxy(ctx, cfg, io.Discard) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "hang.test.pid")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SSH did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := waitProxy(t, done); err != nil {
		t.Fatal(err)
	}
	assertChildStopped(t, dir, "hang.test")
}
