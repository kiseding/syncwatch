package stream

import (
	"sort"
	"sync"

	"github.com/pion/rtp"
)

// JitterBuffer provides ordered RTP packet delivery with configurable depth.
type JitterBuffer struct {
	packets   []*rtp.Packet
	target    int // target buffer size in packets
	lastSeq   uint16
	initialized bool
	mu        sync.Mutex
}

// NewJitterBuffer creates a buffer with the given target size (in milliseconds).
// At ~50 packets/sec for video, 200ms = ~10 packets.
func NewJitterBuffer(targetMs int) *JitterBuffer {
	// ~50 packets/sec for video, so targetMs/20 = approximate packet count
	packetCount := targetMs / 20
	if packetCount < 2 {
		packetCount = 2
	}
	if packetCount > 50 {
		packetCount = 50
	}
	return &JitterBuffer{
		packets: make([]*rtp.Packet, 0, packetCount+5),
		target:  packetCount,
	}
}

// Push adds a packet to the buffer, maintaining sequence order.
func (jb *JitterBuffer) Push(pkt *rtp.Packet) {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	if !jb.initialized {
		jb.lastSeq = pkt.Header.SequenceNumber
		jb.initialized = true
	}

	jb.packets = append(jb.packets, pkt)

	// Keep sorted by sequence number to handle out-of-order delivery
	sort.Slice(jb.packets, func(i, j int) bool {
		// Handle sequence number wrap-around
		si := int16(jb.packets[i].Header.SequenceNumber - jb.lastSeq)
		sj := int16(jb.packets[j].Header.SequenceNumber - jb.lastSeq)
		return si < sj
	})
}

// Pop removes and returns the oldest packet, or nil if buffer is empty.
func (jb *JitterBuffer) Pop() *rtp.Packet {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	if len(jb.packets) == 0 {
		return nil
	}

	pkt := jb.packets[0]
	jb.packets = jb.packets[1:]
	jb.lastSeq = pkt.Header.SequenceNumber

	return pkt
}

// Ready returns true when the buffer has reached target depth.
func (jb *JitterBuffer) Ready() bool {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	return len(jb.packets) >= jb.target
}

// Len returns the current number of buffered packets.
func (jb *JitterBuffer) Len() int {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	return len(jb.packets)
}

// Clear empties the buffer (used on seek).
func (jb *JitterBuffer) Clear() {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	jb.packets = jb.packets[:0]
	jb.initialized = false
}
