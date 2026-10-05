package main

import (
	"encoding/binary"
	"time"

	"github.com/pion/rtp"
)

// streamLoop sends one RTP packet (160 PCMU samples) every 20ms while the
// session is in the playing state. It stops when the connection closes, the
// session is torn down, a write fails/times out, or the file ends (which
// pauses the session at the end of the stream).
func (h *connHandler) streamLoop() {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
		}
		pkt, ok := h.nextPacket()
		if !ok {
			continue
		}
		if err := h.writeLocked(pkt); err != nil {
			h.conn.Close() // write timeout or broken pipe: stop everything
			return
		}
	}
}

// nextPacket builds the next interleaved RTP frame if the session is playing.
func (h *connHandler) nextPacket() ([]byte, bool) {
	h.mu.Lock()
	sess := h.sess
	if sess == nil || sess.state != statePlaying {
		h.mu.Unlock()
		return nil, false
	}
	end := sess.position + samplesPerTick
	if end > len(h.srv.data) {
		end = len(h.srv.data)
	}
	payload := h.srv.data[sess.position:end]
	n := len(payload)
	packet := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    0, // PCMU
			SequenceNumber: sess.seq,
			Timestamp:      sess.rtptime,
			SSRC:           sess.ssrc,
		},
		Payload: payload,
	}
	sess.seq++
	sess.rtptime += uint32(n)
	sess.position = end
	if sess.position >= len(h.srv.data) {
		sess.state = stateReady // end of stream: pause at the end
	}
	h.mu.Unlock()

	raw, err := packet.Marshal()
	if err != nil {
		return nil, false
	}
	frame := make([]byte, 4+len(raw))
	frame[0] = '$'
	frame[1] = sess.rtpChannel
	binary.BigEndian.PutUint16(frame[2:4], uint16(len(raw)))
	copy(frame[4:], raw)
	return frame, true
}
