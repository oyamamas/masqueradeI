/*
Copyright © 2026 oyama
*/
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

type proxyConfig struct {
	ssh            []string
	port           int
	attach         []int
	startupTimeout time.Duration
	healthInterval time.Duration
}

func validPort(port int) bool { return port > 0 && port <= 65535 }

func (c proxyConfig) validate() error {
	if !validPort(c.port) {
		return fmt.Errorf("proxy port must be between 1 and 65535")
	}
	if len(c.ssh)+len(c.attach) == 0 {
		return fmt.Errorf("specify at least one --ssh or --attach tunnel")
	}
	if c.startupTimeout <= 0 || c.healthInterval <= 0 {
		return fmt.Errorf("startup timeout and health interval must be positive")
	}
	seen := map[int]bool{c.port: true}
	for _, port := range c.attach {
		if !validPort(port) {
			return fmt.Errorf("attached port %d must be between 1 and 65535", port)
		}
		if seen[port] {
			return fmt.Errorf("attached port %d duplicates another tunnel or the proxy port", port)
		}
		seen[port] = true
	}
	for _, address := range c.ssh {
		if _, _, err := parseSSHConnectionString(address); err != nil {
			return err
		}
	}
	return nil
}

// SSH writes diagnostics concurrently with the session's status messages.
type synchronizedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

func runProxy(ctx context.Context, cfg proxyConfig, output io.Writer) (err error) {
	if err := cfg.validate(); err != nil {
		return err
	}
	output = &synchronizedWriter{w: output}
	logger := log.New(output, "", log.LstdFlags)

	address := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: cfg.port}
	listener, err := net.ListenTCP("tcp4", address)
	if err != nil {
		return fmt.Errorf("listen on proxy port %d: %w", cfg.port, err)
	}

	relayCtx, stopRelay := context.WithCancel(ctx)
	relay := &tcpRelay{}
	relayDone := make(chan struct{})
	var relayErr error
	go func() {
		defer close(relayDone)
		relayErr = relay.serve(relayCtx, listener)
	}()

	var owned []*tunnel
	defer func() {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			err = nil
		}
		stopRelay()
		_ = listener.Close()
		<-relayDone
		for _, tun := range owned {
			err = errors.Join(err, tun.close())
		}
	}()

	reserved := map[int]bool{cfg.port: true}
	for _, port := range cfg.attach {
		reserved[port] = true
	}
	var active []*tunnel
	var failures []error
	for _, address := range cfg.ssh {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger.Printf("Starting SSH tunnel to %s", address)
		tun, startErr := startSSHTunnel(ctx, address, cfg.startupTimeout, reserved, output)
		if startErr != nil {
			failures = append(failures, startErr)
			logger.Printf("Skipping tunnel: %v", startErr)
			continue
		}
		owned = append(owned, tun)
		active = append(active, tun)
		reserved[tun.port] = true
	}
	for _, port := range cfg.attach {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if probeErr := probeSOCKS(ctx, port); probeErr != nil {
			attachErr := fmt.Errorf("attach 127.0.0.1:%d: %w", port, probeErr)
			failures = append(failures, attachErr)
			logger.Printf("Skipping tunnel: %v", attachErr)
			continue
		}
		active = append(active, &tunnel{port: port})
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Earlier tunnels may have exited while later tunnels were starting.
	active, failures = liveTunnels(active, failures, logger)
	if len(active) == 0 {
		return fmt.Errorf("no working SOCKS tunnels: %w", errors.Join(failures...))
	}
	relay.setPorts(tunnelPorts(active))
	logger.Printf("SOCKS proxy ready at 127.0.0.1:%d (%d tunnels)", cfg.port, len(active))

	// A process exit wakes the session immediately; attached tunnels and live
	// but unresponsive SOCKS listeners are also checked periodically.
	exited := make(chan *tunnel, len(owned))
	watchCtx, cancelWatch := context.WithCancel(ctx)
	defer cancelWatch()
	for _, tun := range owned {
		go func(tun *tunnel) {
			select {
			case <-tun.done:
				exited <- tun
			case <-watchCtx.Done():
			}
		}(tun)
	}
	ticker := time.NewTicker(cfg.healthInterval)
	defer ticker.Stop()
	for {
		var failed *tunnel
		select {
		case <-ctx.Done():
			return nil
		case <-relayDone:
			return relayErr
		case failed = <-exited:
		case <-ticker.C:
		}
		remaining := make([]*tunnel, 0, len(active))
		for _, tun := range active {
			var healthErr error
			switch {
			case tun == failed || tun.exited():
				healthErr = tun.exitError()
			case failed == nil:
				healthErr = probeSOCKS(ctx, tun.port)
			}
			if ctx.Err() != nil {
				return nil
			}
			if healthErr != nil {
				logger.Printf("Removing tunnel on port %d: %v", tun.port, healthErr)
				continue
			}
			remaining = append(remaining, tun)
		}
		if len(remaining) == len(active) {
			continue
		}
		if len(remaining) == 0 {
			return fmt.Errorf("all SOCKS tunnels stopped; proxy shutting down")
		}
		relay.setPorts(tunnelPorts(remaining))
		// Stop owned tunnels removed by a failed health check as well.
		for _, tun := range active {
			if !slices.Contains(remaining, tun) {
				if closeErr := tun.close(); closeErr != nil {
					return closeErr
				}
			}
		}
		active = remaining
		logger.Printf("Balancing across %d remaining tunnels", len(active))
	}
}

func liveTunnels(tunnels []*tunnel, failures []error, logger *log.Logger) ([]*tunnel, []error) {
	var live []*tunnel
	for _, tun := range tunnels {
		if tun.exited() {
			err := tun.exitError()
			failures = append(failures, err)
			logger.Printf("Skipping tunnel on port %d: %v", tun.port, err)
		} else {
			live = append(live, tun)
		}
	}
	return live, failures
}

func tunnelPorts(tunnels []*tunnel) []int {
	ports := make([]int, len(tunnels))
	for i, tun := range tunnels {
		ports[i] = tun.port
	}
	return ports
}

func newProxyCommand() *cobra.Command {
	cfg := proxyConfig{}
	cmd := &cobra.Command{
		Use: "proxy", Short: "Round-robin local SOCKS5 proxy over SSH tunnels",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.validate(); err != nil {
				return err
			}
			if len(cfg.ssh) > 0 {
				if _, err := exec.LookPath("ssh"); err != nil {
					return fmt.Errorf("required executable ssh: %w", err)
				}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return runProxy(ctx, cfg, cmd.ErrOrStderr())
		},
	}
	cmd.Flags().IntVarP(&cfg.port, "port", "p", 1337, "local IPv4 SOCKS5 proxy port")
	cmd.Flags().StringSliceVarP(&cfg.ssh, "ssh", "s", nil, "SSH destination: [user@]host[:port] (repeatable)")
	cmd.Flags().IntSliceVarP(&cfg.attach, "attach", "a", nil, "existing SOCKS5 port on 127.0.0.1 (repeatable)")
	cmd.Flags().DurationVar(&cfg.startupTimeout, "startup-timeout", 15*time.Second, "readiness timeout per SSH tunnel")
	cmd.Flags().DurationVar(&cfg.healthInterval, "health-interval", 2*time.Second, "interval between SOCKS health checks")
	return cmd
}

func init() { rootCmd.AddCommand(newProxyCommand()) }
