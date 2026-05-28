package sshpool

import (
	"context"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Pool manages a collection of active SSH clients to enable connection reuse.
type Pool struct {
	mu      sync.Mutex
	clients map[string]*ssh.Client
}

// NewPool creates a new SSH connection pool.
func NewPool() *Pool {
	return &Pool{
		clients: make(map[string]*ssh.Client),
	}
}

// GetClient returns an existing ssh.Client for the host or dials a new one.
func (p *Pool) GetClient(ctx context.Context, host string, config *ssh.ClientConfig) (*ssh.Client, error) {
	p.mu.Lock()
	client, ok := p.clients[host]
	if ok {
		// Quick health check via a global request
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		if err == nil {
			p.mu.Unlock()
			return client, nil
		}
		// Connection is dead
		client.Close()
		delete(p.clients, host)
	}
	p.mu.Unlock()

	// Dials a new connection if none exists or the existing one is dead.
	// We dial outside the lock to avoid blocking other requests.
	addr := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		addr = net.JoinHostPort(host, "22")
	}

	d := net.Dialer{Timeout: config.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		return nil, err
	}
	client = ssh.NewClient(sshConn, chans, reqs)

	p.mu.Lock()
	p.clients[host] = client
	p.mu.Unlock()

	return client, nil
}

// Close closes all active connections in the pool.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for host, client := range p.clients {
		client.Close()
		delete(p.clients, host)
	}
	return nil
}
