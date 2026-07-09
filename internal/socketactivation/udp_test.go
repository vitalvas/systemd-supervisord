package socketactivation

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitalvas/systemd-supervisord/internal/config"
)

// udpEchoServer echoes every datagram back to its sender.
func udpEchoServer(t *testing.T) net.PacketConn {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}

			if _, werr := pc.WriteTo(buf[:n], addr); werr != nil {
				return
			}
		}
	}()

	t.Cleanup(func() { pc.Close() })

	return pc
}

func freeUDPAddr(t *testing.T) string {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := pc.LocalAddr().String()
	pc.Close()

	return addr
}

func udpConfig(listen, backend string) config.SocketActivationConfig {
	cfg := baseConfig(listen, backend)
	cfg.Protocol = []string{"udp"}

	return cfg
}

// roundTrip dials the listener over the given network, sends payload, and
// asserts it is echoed back within the deadline.
func roundTrip(t *testing.T, network, listen, payload string) {
	t.Helper()

	client, err := net.Dial(network, listen)
	require.NoError(t, err)
	defer client.Close()

	_, err = client.Write([]byte(payload))
	require.NoError(t, err)

	buf := make([]byte, len(payload))
	require.NoError(t, client.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, err := client.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, payload, string(buf[:n]))
}

func TestActivatorUDPProxies(t *testing.T) {
	backend := udpEchoServer(t)
	listen := freeUDPAddr(t)

	ctrl := &mockController{}
	probe := &fakeProbe{}
	probe.healthy.Store(true)

	cfg := udpConfig(listen, backend.LocalAddr().String())
	a, _ := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))

	roundTrip(t, "udp", listen, "query")
	assert.Equal(t, 1, ctrl.startCount())
}

func TestActivatorUDPWaitsUntilHealthy(t *testing.T) {
	backend := udpEchoServer(t)
	listen := freeUDPAddr(t)

	ctrl := &mockController{}
	probe := &fakeProbe{}

	cfg := udpConfig(listen, backend.LocalAddr().String())
	a, _ := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))

	go func() {
		time.Sleep(50 * time.Millisecond)
		probe.healthy.Store(true)
	}()

	client, err := net.Dial("udp", listen)
	require.NoError(t, err)
	defer client.Close()

	_, err = client.Write([]byte("dns!!"))
	require.NoError(t, err)

	buf := make([]byte, 16)
	require.NoError(t, client.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, err := client.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "dns!!", string(buf[:n]))
}

func TestActivatorUDPSessionTracking(t *testing.T) {
	backend := udpEchoServer(t)
	listen := freeUDPAddr(t)

	ctrl := &mockController{}
	probe := &fakeProbe{}
	probe.healthy.Store(true)

	cfg := udpConfig(listen, backend.LocalAddr().String())
	a, _ := newTestActivator(t, cfg, ctrl, probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))

	client, err := net.Dial("udp", listen)
	require.NoError(t, err)
	defer client.Close()

	_, err = client.Write([]byte("q"))
	require.NoError(t, err)

	buf := make([]byte, 16)
	require.NoError(t, client.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, err = client.Read(buf)
	require.NoError(t, err)

	// A live session counts as an active connection.
	require.Eventually(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()

		return a.active == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestActivatorUDPSharedLifecycleWithTCP(t *testing.T) {
	tcpBackend := echoServer(t)
	udpBackend := udpEchoServer(t)

	// Bind the listener on a shared host:port for both protocols.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	listen := pc.LocalAddr().String()
	pc.Close()

	ctrl := &mockController{}
	probe := &fakeProbe{}
	probe.healthy.Store(true)

	cfg := baseConfig(listen, tcpBackend.Addr().String())
	cfg.Protocol = []string{"tcp", "udp"}
	a, _ := newTestActivator(t, cfg, ctrl, probe)
	// UDP path proxies to the udp echo backend.
	a.udpDialer = func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer

		return d.DialContext(ctx, "udp", udpBackend.LocalAddr().String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))

	// TCP connection.
	tcpConn, err := net.Dial("tcp", listen)
	require.NoError(t, err)
	defer tcpConn.Close()

	_, err = tcpConn.Write([]byte("tcp"))
	require.NoError(t, err)

	tbuf := make([]byte, 3)
	require.NoError(t, tcpConn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, err = tcpConn.Read(tbuf)
	require.NoError(t, err)
	assert.Equal(t, "tcp", string(tbuf))

	// UDP datagram to the same address.
	udpConn, err := net.Dial("udp", listen)
	require.NoError(t, err)
	defer udpConn.Close()

	_, err = udpConn.Write([]byte("udp"))
	require.NoError(t, err)

	ubuf := make([]byte, 3)
	require.NoError(t, udpConn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, err = udpConn.Read(ubuf)
	require.NoError(t, err)
	assert.Equal(t, "udp", string(ubuf))

	// Both protocols share the single unit start.
	assert.Equal(t, 1, ctrl.startCount())
}

func TestActivatorUnsupportedProtocol(t *testing.T) {
	cfg := baseConfig(freeAddr(t), "127.0.0.1:1")
	cfg.Protocol = []string{"sctp"}

	a, _ := newTestActivator(t, cfg, &mockController{}, &fakeProbe{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := a.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported protocol")
}

func TestActivatorPartialBindFailureUnwinds(t *testing.T) {
	// Occupy a UDP port so the udp listener fails to bind after tcp succeeds.
	occupied, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer occupied.Close()

	cfg := baseConfig(occupied.LocalAddr().String(), "127.0.0.1:1")
	cfg.Protocol = []string{"tcp", "udp"}

	a, _ := newTestActivator(t, cfg, &mockController{}, &fakeProbe{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = a.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "udp")

	// The tcp listener bound first must have been closed on unwind, so the
	// address is free to bind again.
	ln, err := net.Listen("tcp", cfg.Listen)
	require.NoError(t, err)
	ln.Close()
}

func TestUDPProxyExpireIdle(t *testing.T) {
	backend := udpEchoServer(t)

	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	ctrl := &mockController{}
	probe := &fakeProbe{}
	probe.healthy.Store(true)

	cfg := udpConfig(listener.LocalAddr().String(), backend.LocalAddr().String())
	a, clk := newTestActivator(t, cfg, ctrl, probe)

	p := &udpProxy{
		activator: a,
		conn:      listener,
		dial:      a.udpDial,
		sessions:  make(map[string]*udpSession),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clientAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:12345")
	require.NoError(t, err)

	// Establish a session directly.
	_, err = p.session(ctx, clientAddr)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()

		return a.active == 1
	}, 2*time.Second, 10*time.Millisecond)

	// Not yet past TTL.
	p.expireIdle()
	a.mu.Lock()
	active := a.active
	a.mu.Unlock()
	assert.Equal(t, 1, active)

	// Advance past the session TTL and expire.
	clk.advance(udpSessionTTL + time.Second)
	p.expireIdle()

	require.Eventually(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()

		return a.active == 0
	}, 2*time.Second, 10*time.Millisecond)
}
