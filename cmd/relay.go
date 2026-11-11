package cmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

type tcpRelay struct {
	mu    sync.Mutex
	ports []int
	next  int
}

func (r *tcpRelay) setPorts(ports []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ports = ports
	r.next = 0
}

func (r *tcpRelay) nextPort() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.ports) == 0 {
		return 0
	}
	port := r.ports[r.next]
	r.next = (r.next + 1) % len(r.ports)
	return port
}

func (r *tcpRelay) serve(ctx context.Context, listener *net.TCPListener) error {
	ctx, cancel := context.WithCancel(ctx)
	var sessions sync.WaitGroup
	defer func() {
		cancel()
		sessions.Wait()
	}()
	for {
		client, err := listener.AcceptTCP()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept proxy connection: %w", err)
		}
		port := r.nextPort()
		if port == 0 {
			// Reject connections while tunnels are starting.
			_ = client.Close()
			continue
		}
		sessions.Go(func() { relayConnection(ctx, client, port) })
	}
}

func relayConnection(ctx context.Context, client *net.TCPConn, port int) {
	defer client.Close()
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return
	}
	upstream := conn.(*net.TCPConn)
	defer upstream.Close()
	stop := context.AfterFunc(ctx, func() {
		_ = client.Close()
		_ = upstream.Close()
	})
	defer stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		copyTCP(upstream, client)
	}()
	copyTCP(client, upstream)
	<-done
}

func copyTCP(dst, src *net.TCPConn) {
	if _, err := io.Copy(dst, src); err == nil && dst.CloseWrite() == nil {
		return
	}
	// Unblock the other direction if the connection fails.
	_ = dst.Close()
	_ = src.Close()
}
