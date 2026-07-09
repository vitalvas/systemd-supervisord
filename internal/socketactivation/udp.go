package socketactivation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// udpSessionTTL bounds how long an idle UDP session (a client source address
// with a live backend socket) is kept before it is torn down. UDP has no
// connection close, so replies are routed back only while the session lives.
const udpSessionTTL = 30 * time.Second

// UDPDialer establishes a packet connection to the backend. Abstracted for
// testing.
type UDPDialer func(ctx context.Context, address string) (net.Conn, error)

func defaultUDPDialer(ctx context.Context, address string) (net.Conn, error) {
	var d net.Dialer

	return d.DialContext(ctx, "udp", address)
}

// udpSession tracks one client source address and its dedicated backend socket.
type udpSession struct {
	backend  net.Conn
	lastSeen time.Time
}

// udpProxy relays datagrams between clients on a shared listener socket and the
// backend, keeping one backend socket per client source address so replies can
// be routed back to the correct client.
type udpProxy struct {
	activator *Activator
	conn      net.PacketConn
	dial      UDPDialer

	mu       sync.Mutex
	sessions map[string]*udpSession
}

func (a *Activator) startUDP(ctx context.Context) (func(), error) {
	pc, err := net.ListenPacket("udp", a.cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("listening on udp %s: %w", a.cfg.Listen, err)
	}

	p := &udpProxy{
		activator: a,
		conn:      pc,
		dial:      a.udpDial,
		sessions:  make(map[string]*udpSession),
	}

	go func() {
		<-ctx.Done()
		_ = pc.Close()
	}()

	go p.readLoop(ctx)
	go p.expireLoop(ctx)

	a.logger.Info("socket activation listener started",
		"protocol", "udp",
		"listen", a.cfg.Listen,
		"unit", a.cfg.Unit,
		"backend", a.cfg.Backend,
	)

	return func() { _ = pc.Close() }, nil
}

func (p *udpProxy) readLoop(ctx context.Context) {
	buf := make([]byte, 64*1024)

	for {
		n, addr, err := p.conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}

			p.activator.logger.Error("udp read failed", "error", err)

			continue
		}

		payload := make([]byte, n)
		copy(payload, buf[:n])

		p.handleDatagram(ctx, addr, payload)
	}
}

func (p *udpProxy) handleDatagram(ctx context.Context, addr net.Addr, payload []byte) {
	session, err := p.session(ctx, addr)
	if err != nil {
		p.activator.logger.Error("udp backend not available", "error", err)

		return
	}

	p.activator.markTraffic()

	if _, err := session.backend.Write(payload); err != nil {
		p.activator.logger.Error("udp write to backend failed", "backend", p.activator.cfg.Backend, "error", err)
		p.closeSession(addr.String())
	}
}

// session returns the backend socket for a client address, creating one on the
// first datagram. Creating a session ensures the unit is running and increments
// the active counter so the idle stopper does not fire mid-session.
func (p *udpProxy) session(ctx context.Context, addr net.Addr) (*udpSession, error) {
	key := addr.String()

	p.mu.Lock()
	if s, ok := p.sessions[key]; ok {
		s.lastSeen = p.activator.clock.Now()
		p.mu.Unlock()

		return s, nil
	}
	p.mu.Unlock()

	if err := p.activator.ensureRunning(ctx); err != nil {
		return nil, err
	}

	backend, err := p.dial(ctx, p.activator.cfg.Backend)
	if err != nil {
		return nil, fmt.Errorf("dialing udp backend %s: %w", p.activator.cfg.Backend, err)
	}

	s := &udpSession{backend: backend, lastSeen: p.activator.clock.Now()}

	p.mu.Lock()
	// Another datagram may have created the session while we were dialing.
	if existing, ok := p.sessions[key]; ok {
		p.mu.Unlock()
		_ = backend.Close()

		return existing, nil
	}

	p.sessions[key] = s
	p.mu.Unlock()

	p.activator.connOpened()

	go p.replyLoop(ctx, addr, s)

	return s, nil
}

// replyLoop copies datagrams from the backend socket back to the client until
// the session is closed.
func (p *udpProxy) replyLoop(ctx context.Context, addr net.Addr, s *udpSession) {
	buf := make([]byte, 64*1024)

	for {
		if ctx.Err() != nil {
			return
		}

		// The socket deadline must use wall-clock time; the injectable clock is
		// only for idle bookkeeping in tests.
		_ = s.backend.SetReadDeadline(time.Now().Add(udpSessionTTL))

		n, err := s.backend.Read(buf)
		if n > 0 {
			p.activator.markTraffic()

			p.mu.Lock()
			s.lastSeen = p.activator.clock.Now()
			p.mu.Unlock()

			if _, werr := p.conn.WriteTo(buf[:n], addr); werr != nil {
				p.closeSession(addr.String())

				return
			}
		}

		if err != nil {
			// A read timeout ends the session; the client will re-establish on
			// its next datagram.
			p.closeSession(addr.String())

			return
		}
	}
}

func (p *udpProxy) closeSession(key string) {
	p.mu.Lock()
	s, ok := p.sessions[key]
	if ok {
		delete(p.sessions, key)
	}
	p.mu.Unlock()

	if !ok {
		return
	}

	_ = s.backend.Close()
	p.activator.connClosed()
}

// expireLoop periodically tears down sessions that have seen no traffic within
// the session TTL, releasing their backend sockets.
func (p *udpProxy) expireLoop(ctx context.Context) {
	ticker := time.NewTicker(udpSessionTTL)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.expireIdle()
		}
	}
}

func (p *udpProxy) expireIdle() {
	now := p.activator.clock.Now()

	p.mu.Lock()
	stale := make([]string, 0)
	for key, s := range p.sessions {
		if now.Sub(s.lastSeen) >= udpSessionTTL {
			stale = append(stale, key)
		}
	}
	p.mu.Unlock()

	for _, key := range stale {
		p.closeSession(key)
	}
}

func (a *Activator) udpDial(ctx context.Context, address string) (net.Conn, error) {
	if a.udpDialer != nil {
		return a.udpDialer(ctx, address)
	}

	return defaultUDPDialer(ctx, address)
}
