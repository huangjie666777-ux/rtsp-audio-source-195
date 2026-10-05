// Command demo is a TCP RTSP client that exercises the server: OPTIONS,
// DESCRIBE, SETUP, PLAY, PAUSE, resume, seek via Range, and TEARDOWN, while
// counting the interleaved RTP packets it actually receives.
package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/pion/rtp"
)

var cseq int

func main() {
	addr := flag.String("addr", "127.0.0.1:8554", "server address")
	flag.Parse()

	conn, err := net.Dial("tcp", *addr)
	must(err)
	defer conn.Close()
	br := bufio.NewReader(conn)

	base := "rtsp://" + *addr + "/demo"

	must(sendReq(conn, "OPTIONS", base, nil))
	printResp(mustReadResp(br))

	must(sendReq(conn, "DESCRIBE", base, nil))
	resp := mustReadResp(br)
	printResp(resp)

	must(sendReq(conn, "SETUP", base+"/trackID=0", []string{
		"Transport: RTP/AVP/TCP;unicast;interleaved=0-1",
	}))
	resp = mustReadResp(br)
	printResp(resp)
	session := headerOf(resp, "Session")
	fmt.Println(">> Session:", session)

	// PLAY from the beginning, receive ~1s of audio.
	must(sendReq(conn, "PLAY", base, []string{"Session: " + session}))
	printResp(mustReadResp(br))
	n1, last1 := collect(conn, br, 1*time.Second)
	fmt.Printf(">> PLAY: received %d RTP packets, last seq=%d ts=%d\n",
		n1, last1.SequenceNumber, last1.Timestamp)

	// PAUSE: no packets may follow the response.
	must(sendReq(conn, "PAUSE", base, []string{"Session: " + session}))
	printResp(mustReadResp(br))
	nPause, _ := collect(conn, br, 200*time.Millisecond)
	fmt.Printf(">> PAUSE: received %d RTP packets during 200ms pause\n", nPause)

	// Resume without Range: continues where it stopped, no gaps.
	must(sendReq(conn, "PLAY", base, []string{"Session: " + session}))
	resp = mustReadResp(br)
	printResp(resp)
	n2, last2 := collect(conn, br, 500*time.Millisecond)
	fmt.Printf(">> resume: received %d RTP packets, first seq continues at %s\n",
		n2, headerOf(resp, "RTP-Info"))

	// Seek to npt=2.0s via Range.
	must(sendReq(conn, "PAUSE", base, []string{"Session: " + session}))
	mustReadResp(br)
	must(sendReq(conn, "PLAY", base, []string{
		"Session: " + session,
		"Range: npt=2.0-",
	}))
	resp = mustReadResp(br)
	printResp(resp)
	fmt.Println(">> seek: server reports", headerOf(resp, "Range"))
	n3, last3 := collect(conn, br, 500*time.Millisecond)
	fmt.Printf(">> seek: received %d RTP packets from npt=2.0 (last seq=%d ts=%d)\n",
		n3, last3.SequenceNumber, last3.Timestamp)

	must(sendReq(conn, "TEARDOWN", base, []string{"Session: " + session}))
	printResp(mustReadResp(br))
	fmt.Printf(">> TEARDOWN done. Total RTP packets received: %d\n", n1+n2+n3)
	_ = last2
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func sendReq(w io.Writer, method, uri string, extra []string) error {
	cseq++
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s RTSP/1.0\r\nCSeq: %d\r\n", method, uri, cseq)
	for _, h := range extra {
		sb.WriteString(h + "\r\n")
	}
	sb.WriteString("\r\n")
	_, err := io.WriteString(w, sb.String())
	return err
}

func mustReadResp(br *bufio.Reader) string {
	var sb strings.Builder
	for {
		line, err := br.ReadString('\n')
		must(err)
		sb.WriteString(line)
		if line == "\r\n" {
			return sb.String()
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			var n int
			fmt.Sscanf(line, "Content-Length: %d", &n)
			body := make([]byte, n)
			// read rest of headers first
			for {
				l, err := br.ReadString('\n')
				must(err)
				sb.WriteString(l)
				if l == "\r\n" {
					break
				}
			}
			_, err = io.ReadFull(br, body)
			must(err)
			sb.Write(body)
			return sb.String()
		}
	}
}

func printResp(resp string) {
	first, _, _ := strings.Cut(resp, "\r\n")
	fmt.Println("<<", first)
}

func headerOf(resp, name string) string {
	for _, line := range strings.Split(resp, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(name)+":") {
			return strings.TrimSpace(line[len(name)+1:])
		}
	}
	return ""
}

// collect reads interleaved RTP frames for the given duration (via read
// deadlines) and returns the count and the last parsed packet.
func collect(conn net.Conn, br *bufio.Reader, d time.Duration) (int, *rtp.Packet) {
	deadline := time.Now().Add(d)
	count := 0
	var last *rtp.Packet
	for {
		conn.SetReadDeadline(deadline)
		hdr := make([]byte, 4)
		if _, err := io.ReadFull(br, hdr); err != nil {
			conn.SetReadDeadline(time.Time{})
			return count, last
		}
		if hdr[0] != '$' {
			conn.SetReadDeadline(time.Time{})
			fmt.Println("!! expected interleaved frame, got", hdr)
			return count, last
		}
		payload := make([]byte, binary.BigEndian.Uint16(hdr[2:4]))
		if _, err := io.ReadFull(br, payload); err != nil {
			conn.SetReadDeadline(time.Time{})
			return count, last
		}
		if hdr[1] != 0 { // skip RTCP channel
			continue
		}
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(payload); err != nil {
			continue
		}
		count++
		last = pkt
	}
}
