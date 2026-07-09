package socketactivation

import (
	"io"
	"net"
	"sync"
)

// relay performs a bidirectional L4 copy between a client connection and a
// backend connection. Every byte moved in either direction invokes onTraffic so
// the activator can track last-activity time. It returns after both directions
// have finished (either side closed).
func relay(client, backend net.Conn, onTraffic func()) {
	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()

		copyWithTraffic(backend, client, onTraffic)
		closeWrite(backend)
	}()

	go func() {
		defer wg.Done()

		copyWithTraffic(client, backend, onTraffic)
		closeWrite(client)
	}()

	wg.Wait()
}

// copyWithTraffic copies from src to dst, calling onTraffic after each chunk of
// bytes is transferred. It mirrors io.Copy but reports progress.
func copyWithTraffic(dst io.Writer, src io.Reader, onTraffic func()) {
	buf := make([]byte, 32*1024)

	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return
			}

			if onTraffic != nil {
				onTraffic()
			}
		}

		if readErr != nil {
			return
		}
	}
}

// closeWrite half-closes the write side of a TCP connection when supported so
// the peer observes EOF, otherwise it is a no-op.
func closeWrite(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
