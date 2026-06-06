package stream

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/pion/rtp"
)

// Reader reads RTP packets from a UDP socket.
type Reader struct {
	conn     *net.UDPConn
	buffer   *JitterBuffer
	running  atomic.Bool
	done     chan struct{}
	mu       sync.Mutex

	// Stats
	packetsReceived uint64
	bytesReceived   uint64
	ssrc            uint32
}

// NewReader creates a new RTP reader listening on the given port.
func NewReader(port int, jitterMs int) (*Reader, error) {
	addr := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: port,
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen udp %s: %w", addr.String(), err)
	}

	// Set buffer sizes for UDP socket
	conn.SetReadBuffer(2 * 1024 * 1024) // 2MB

	return &Reader{
		conn:   conn,
		buffer: NewJitterBuffer(jitterMs),
		done:   make(chan struct{}),
	}, nil
}

// Start begins reading RTP packets from the UDP socket.
func (r *Reader) Start() {
	r.running.Store(true)
	buf := make([]byte, 1500) // Typical MTU size

	go func() {
		for r.running.Load() {
			n, _, err := r.conn.ReadFromUDP(buf)
			if err != nil {
				if r.running.Load() {
					// Log error but continue
					continue
				}
				return
			}

			pkt := &rtp.Packet{}
			if err := pkt.Unmarshal(buf[:n]); err != nil {
				continue // Skip malformed packets
			}

			atomic.AddUint64(&r.packetsReceived, 1)
			atomic.AddUint64(&r.bytesReceived, uint64(n))

			r.mu.Lock()
			if r.ssrc == 0 {
				r.ssrc = pkt.Header.SSRC
			}
			r.mu.Unlock()

			r.buffer.Push(pkt)
		}
	}()
}

// Stop stops reading and closes the UDP socket.
func (r *Reader) Stop() {
	r.running.Store(false)
	close(r.done)
	if r.conn != nil {
		r.conn.Close()
	}
}

// ReadPacket returns the next available RTP packet, blocking until one arrives.
func (r *Reader) ReadPacket() (*rtp.Packet, error) {
	for {
		if !r.running.Load() {
			return nil, fmt.Errorf("reader stopped")
		}

		pkt := r.buffer.Pop()
		if pkt != nil {
			return pkt, nil
		}

		// Busy-wait is fine for this real-time scenario
		// (UDP packets arrive every ~20-50ms)
		select {
		case <-r.done:
			return nil, fmt.Errorf("reader stopped")
		default:
		}
	}
}

// ReadPacketNonBlocking returns a packet without blocking, or nil.
func (r *Reader) ReadPacketNonBlocking() *rtp.Packet {
	return r.buffer.Pop()
}

// ClearBuffer clears the jitter buffer (used on seek).
func (r *Reader) ClearBuffer() {
	r.buffer.Clear()
}

// Stats returns reader statistics.
func (r *Reader) Stats() (packets, bytes uint64) {
	return atomic.LoadUint64(&r.packetsReceived), atomic.LoadUint64(&r.bytesReceived)
}

// SSRC returns the SSRC of the stream.
func (r *Reader) SSRC() uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ssrc
}
