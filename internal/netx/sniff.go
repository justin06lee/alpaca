// Package netx holds the networking primitives alpaca needs that the standard
// library does not provide: a protocol-sniffing listener, self-signed
// certificate management with fingerprint pinning, and local address discovery.
package netx

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// tlsHandshakeRecord is the ContentType byte that opens every TLS connection
// (RFC 8446 §5.1, "handshake" = 22). No HTTP request can begin with it: methods
// are ASCII uppercase letters, all well above 0x16. That one byte is therefore
// enough to tell the two protocols apart with no ambiguity.
const tlsHandshakeRecord = 0x16

// peekTimeout bounds how long a connection may sit silent before we classify
// it. Without this, a client that connects and never speaks pins a goroutine
// and a file descriptor forever.
const peekTimeout = 15 * time.Second

// Sniffer splits one listener into two by peeking at each connection's first
// byte.
//
// alpaca serves plain HTTP and TLS on a single port so that there is exactly
// one number for a user to remember, one firewall rule to open, and one port to
// forward. The LAN fast path then costs no handshake, while the same port still
// accepts pinned TLS from outside — without the client having to know in
// advance which it will get.
type Sniffer struct {
	inner net.Listener
	tls   *virtualListener
	plain *virtualListener

	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// Sniff starts classifying connections from inner. The caller must serve both
// TLS() and Plain(); connections to an unserved side apply backpressure and are
// eventually dropped when the Sniffer closes.
func Sniff(inner net.Listener) *Sniffer {
	s := &Sniffer{
		inner: inner,
		done:  make(chan struct{}),
	}
	s.tls = newVirtualListener(inner.Addr(), s.done)
	s.plain = newVirtualListener(inner.Addr(), s.done)
	go s.accept()
	return s
}

// TLS returns the listener receiving connections that began a TLS handshake.
func (s *Sniffer) TLS() net.Listener { return s.tls }

// Plain returns the listener receiving everything else.
func (s *Sniffer) Plain() net.Listener { return s.plain }

// Addr reports the underlying listener's address.
func (s *Sniffer) Addr() net.Addr { return s.inner.Addr() }

// Close shuts down the sniffer and the listener beneath it. Both virtual
// listeners then return net.ErrClosed from Accept.
func (s *Sniffer) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		s.closeErr = s.inner.Close()
	})
	return s.closeErr
}

func (s *Sniffer) accept() {
	defer s.Close()
	for {
		conn, err := s.inner.Accept()
		if err != nil {
			// The only reason to stop accepting is shutdown. Everything else —
			// fd exhaustion, ECONNABORTED, a half-open handshake — is transient,
			// and exiting on it would silently kill both HTTP and TLS for the
			// rest of the process's life. Back off briefly and keep going.
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-time.After(50 * time.Millisecond):
				continue
			case <-s.done:
				return
			}
		}
		// Classify off the accept loop: peeking blocks on the client, and a
		// single slow peer must not stall every other pending connection.
		go s.classify(conn)
	}
}

func (s *Sniffer) classify(conn net.Conn) {
	if err := conn.SetReadDeadline(time.Now().Add(peekTimeout)); err != nil {
		conn.Close()
		return
	}
	prefix := make([]byte, 1)
	if _, err := io.ReadFull(conn, prefix); err != nil {
		conn.Close()
		return
	}
	// Hand the connection on with a clean deadline; the HTTP server owns
	// timeouts from here.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		conn.Close()
		return
	}

	target := s.plain
	if prefix[0] == tlsHandshakeRecord {
		target = s.tls
	}

	select {
	case target.conns <- &prefixConn{Conn: conn, prefix: prefix}:
	case <-target.closed:
		// Nobody is serving this protocol any more.
		conn.Close()
	case <-s.done:
		conn.Close()
	}
}

// virtualListener is one of the two halves a Sniffer feeds.
type virtualListener struct {
	addr  net.Addr
	conns chan net.Conn
	// done is the sniffer-wide shutdown signal, shared by both halves.
	done chan struct{}
	// closed shuts down this half alone.
	closed    chan struct{}
	closeOnce sync.Once
}

func newVirtualListener(addr net.Addr, done chan struct{}) *virtualListener {
	return &virtualListener{
		addr:   addr,
		conns:  make(chan net.Conn),
		done:   done,
		closed: make(chan struct{}),
	}
}

func (v *virtualListener) Accept() (net.Conn, error) {
	select {
	case conn := <-v.conns:
		return conn, nil
	case <-v.closed:
		return nil, net.ErrClosed
	case <-v.done:
		return nil, net.ErrClosed
	}
}

// Close detaches this half, leaving the shared socket and the other protocol
// untouched.
//
// It must genuinely unblock Accept: http.Server.Close closes its listener and
// then waits for Serve to return, so a Close that did nothing would deadlock
// shutdown. Equally, it must not close the real listener, or shutting down the
// plain server would silently kill TLS along with it.
func (v *virtualListener) Close() error {
	v.closeOnce.Do(func() { close(v.closed) })
	return nil
}

func (v *virtualListener) Addr() net.Addr { return v.addr }

// prefixConn replays the bytes consumed while sniffing, so the protocol handler
// sees an untouched stream.
type prefixConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}
