package main

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type sessionState int

const (
	stateInit sessionState = iota
	stateReady
	statePlaying
)

type session struct {
	id          string
	state       sessionState
	rtpChannel  uint8
	rtcpChannel uint8
	position    int // next sample offset into the file
	ssrc        uint32
	seq         uint16
	rtptime     uint32 // timestamp of the next packet to send
}

type connHandler struct {
	srv     *server
	conn    net.Conn
	br      *bufio.Reader
	writeMu sync.Mutex // serializes RTSP responses and interleaved $ frames
	mu      sync.Mutex // guards sess
	sess    *session
	done    chan struct{}
}

func newConnHandler(srv *server, conn net.Conn) *connHandler {
	return &connHandler{
		srv:  srv,
		conn: conn,
		br:   bufio.NewReader(conn),
		done: make(chan struct{}),
	}
}

func (h *connHandler) run() {
	defer h.conn.Close()
	defer close(h.done)
	go h.streamLoop()
	for {
		req, err := readFrame(h.br, h.onInterleaved)
		if err != nil {
			return // disconnect or fatal parse error: release everything
		}
		if err := h.dispatch(req); err != nil {
			return // write timeout / broken connection
		}
	}
}

// onInterleaved consumes client binary frames; RTCP (receiver reports) is skipped.
func (h *connHandler) onInterleaved(channel uint8, payload []byte) {
	// Nothing to do: RTCP feedback is intentionally ignored.
}

// writeLocked serializes a write to the connection with a deadline.
func (h *connHandler) writeLocked(p []byte) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	h.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err := h.conn.Write(p)
	return err
}

func (h *connHandler) respond(req *request, code int, reason string, extra []string, body string) error {
	headers := []string{"CSeq: " + req.header("CSeq")}
	headers = append(headers, extra...)
	return h.writeLocked([]byte(buildResponse(code, reason, headers, body)))
}

func buildResponse(code int, reason string, headers []string, body string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "RTSP/1.0 %d %s\r\n", code, reason)
	for _, hdr := range headers {
		sb.WriteString(hdr)
		sb.WriteString("\r\n")
	}
	if body != "" {
		fmt.Fprintf(&sb, "Content-Type: application/sdp\r\nContent-Length: %d\r\n", len(body))
	}
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return sb.String()
}

func (h *connHandler) dispatch(req *request) error {
	if req.header("CSeq") == "" {
		return h.respond(req, 400, "Bad Request", nil, "")
	}
	switch req.method {
	case "OPTIONS":
		return h.respond(req, 200, "OK",
			[]string{"Public: OPTIONS, DESCRIBE, SETUP, PLAY, PAUSE, TEARDOWN"}, "")
	case "DESCRIBE":
		return h.respond(req, 200, "OK", nil, h.srv.sdp(req.uri))
	case "SETUP":
		return h.handleSetup(req)
	case "PLAY":
		return h.handlePlay(req)
	case "PAUSE":
		return h.handlePause(req)
	case "TEARDOWN":
		return h.handleTeardown(req)
	default:
		return h.respond(req, 405, "Method Not Allowed", nil, "")
	}
}

// findSession validates the Session header against this connection's session.
func (h *connHandler) findSession(req *request) (*session, string, bool) {
	id := req.header("Session")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sess == nil || id == "" || h.sess.id != id {
		return nil, "", false
	}
	return h.sess, id, true
}

func (h *connHandler) handleSetup(req *request) error {
	transport := req.header("Transport")
	rtpCh, rtcpCh, ok := parseTransport(transport)
	if !ok {
		return h.respond(req, 461, "Unsupported Transport", nil, "")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sess != nil && h.sess.state != stateInit {
		// SETUP again on an active session is not supported.
	}
	sess := &session{
		id:          newSessionID(),
		state:       stateReady,
		rtpChannel:  rtpCh,
		rtcpChannel: rtcpCh,
		ssrc:        randomUint32(),
		seq:         uint16(randomUint32()),
		rtptime:     randomUint32(),
	}
	h.sess = sess
	return h.respond(req, 200, "OK", []string{
		fmt.Sprintf("Transport: RTP/AVP/TCP;unicast;interleaved=%d-%d", rtpCh, rtcpCh),
		"Session: " + sess.id,
	}, "")
}

func (h *connHandler) handlePlay(req *request) error {
	sess, id, ok := h.findSession(req)
	if !ok {
		return h.respond(req, 454, "Session Not Found", nil, "")
	}
	h.mu.Lock()
	if sess.state == statePlaying {
		h.mu.Unlock()
		return h.respond(req, 455, "Method Not Valid in This State", nil, "")
	}
	if sess.state != stateReady {
		h.mu.Unlock()
		return h.respond(req, 455, "Method Not Valid in This State", nil, "")
	}
	// Optional Range: npt=<start>- seek. Start must be finite, non-negative
	// and less than the duration; it is aligned down to a 20ms boundary and
	// only moves the file cursor.
	if rng := req.header("Range"); rng != "" {
		start, err := parseRangeStart(rng)
		if err != nil || start < 0 || start >= h.srv.duration {
			h.mu.Unlock()
			return h.respond(req, 457, "Invalid Range", nil, "")
		}
		sample := int(start*sampleRate) / samplesPerTick * samplesPerTick
		if sample >= len(h.srv.data) {
			h.mu.Unlock()
			return h.respond(req, 457, "Invalid Range", nil, "")
		}
		sess.position = sample
	}
	startSec := float64(sess.position) / sampleRate
	rtpInfo := fmt.Sprintf("RTP-Info: url=%s/trackID=0;seq=%d;rtptime=%d",
		strings.TrimSuffix(req.uri, "/"), sess.seq, sess.rtptime)
	rangeHdr := fmt.Sprintf("Range: npt=%.3f-%.3f", startSec, h.srv.duration)
	// Respond first, then enable streaming so the first RTP packet is
	// guaranteed to follow the PLAY response.
	err := h.respond(req, 200, "OK",
		[]string{"Session: " + id, rangeHdr, rtpInfo}, "")
	if err != nil {
		h.mu.Unlock()
		return err
	}
	sess.state = statePlaying
	h.mu.Unlock()
	return nil
}

func (h *connHandler) handlePause(req *request) error {
	sess, id, ok := h.findSession(req)
	if !ok {
		return h.respond(req, 454, "Session Not Found", nil, "")
	}
	h.mu.Lock()
	if sess.state != statePlaying {
		h.mu.Unlock()
		return h.respond(req, 455, "Method Not Valid in This State", nil, "")
	}
	// Respond first, then stop: no packet may be sent after the PAUSE response.
	err := h.respond(req, 200, "OK", []string{"Session: " + id}, "")
	if err != nil {
		h.mu.Unlock()
		return err
	}
	sess.state = stateReady // position is kept for a gapless resume
	h.mu.Unlock()
	return nil
}

func (h *connHandler) handleTeardown(req *request) error {
	_, id, ok := h.findSession(req)
	if !ok {
		return h.respond(req, 454, "Session Not Found", nil, "")
	}
	err := h.respond(req, 200, "OK", []string{"Session: " + id}, "")
	h.mu.Lock()
	h.sess = nil
	h.mu.Unlock()
	return err
}

// parseTransport accepts only RTP/AVP/TCP with two distinct interleaved channels.
func parseTransport(header string) (rtpCh, rtcpCh uint8, ok bool) {
	if header == "" {
		return 0, 0, false
	}
	for _, spec := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(spec), ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "RTP/AVP/TCP") {
			continue
		}
		for _, p := range parts[1:] {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(strings.ToLower(p), "interleaved=") {
				vals := strings.SplitN(p[len("interleaved="):], "-", 2)
				if len(vals) != 2 {
					return 0, 0, false
				}
				a, err1 := strconv.Atoi(vals[0])
				b, err2 := strconv.Atoi(vals[1])
				if err1 != nil || err2 != nil || a == b ||
					a < 0 || a > 255 || b < 0 || b > 255 {
					return 0, 0, false
				}
				return uint8(a), uint8(b), true
			}
		}
	}
	return 0, 0, false
}

// parseRangeStart parses "npt=<seconds>-" or "npt=<seconds>-<end>".
func parseRangeStart(rng string) (float64, error) {
	rng = strings.TrimSpace(rng)
	if !strings.HasPrefix(rng, "npt=") {
		return 0, fmt.Errorf("unsupported range unit")
	}
	rng = rng[len("npt="):]
	start := rng
	if i := strings.Index(rng, "-"); i >= 0 {
		start = rng[:i]
	}
	if start == "" {
		return 0, nil // "npt=-" means resume from current position
	}
	v, err := strconv.ParseFloat(start, 64)
	if err != nil {
		return 0, err
	}
	return v, nil
}

func newSessionID() string {
	var b [8]byte
	rand.Read(b[:])
	return fmt.Sprintf("%016x", binary.BigEndian.Uint64(b[:]))
}

func randomUint32() uint32 {
	var b [4]byte
	rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}
