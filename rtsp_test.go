package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadRequestPipelined(t *testing.T) {
	raw := "OPTIONS rtsp://x/demo RTSP/1.0\r\nCSeq: 1\r\n\r\n" +
		"DESCRIBE rtsp://x/demo RTSP/1.0\r\nCSeq: 2\r\nContent-Length: 4\r\n\r\nabcd"
	br := bufio.NewReader(strings.NewReader(raw))
	r1, err := readFrame(br, nil)
	if err != nil || r1.method != "OPTIONS" || r1.header("CSeq") != "1" {
		t.Fatalf("r1: %+v err=%v", r1, err)
	}
	r2, err := readFrame(br, nil)
	if err != nil || r2.method != "DESCRIBE" || string(r2.body) != "abcd" {
		t.Fatalf("r2: %+v body=%q err=%v", r2, r2.body, err)
	}
}

func TestReadFrameSkipsInterleaved(t *testing.T) {
	raw := "$\x01\x00\x04RTCP" +
		"PLAY rtsp://x/demo RTSP/1.0\r\nCSeq: 3\r\n\r\n"
	var got []byte
	br := bufio.NewReader(strings.NewReader(raw))
	r, err := readFrame(br, func(ch uint8, p []byte) { got = p })
	if err != nil || r.method != "PLAY" {
		t.Fatalf("req: %+v err=%v", r, err)
	}
	if string(got) != "RTCP" {
		t.Fatalf("interleaved payload: %q", got)
	}
}

func TestReadRequestTooLarge(t *testing.T) {
	raw := "OPTIONS rtsp://x RTSP/1.0\r\nCSeq: 1\r\nContent-Length: 99999999\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(raw))
	if _, err := readFrame(br, nil); err != errRequestTooLarge {
		t.Fatalf("want errRequestTooLarge, got %v", err)
	}
}

func TestParseTransport(t *testing.T) {
	a, b, ok := parseTransport("RTP/AVP/TCP;unicast;interleaved=0-1")
	if !ok || a != 0 || b != 1 {
		t.Fatalf("tcp: %d %d %v", a, b, ok)
	}
	if _, _, ok := parseTransport("RTP/AVP;unicast"); ok {
		t.Fatal("udp transport must be rejected")
	}
	if _, _, ok := parseTransport("RTP/AVP/TCP;interleaved=2-2"); ok {
		t.Fatal("identical channels must be rejected")
	}
}

func TestParseRangeStart(t *testing.T) {
	v, err := parseRangeStart("npt=1.5-")
	if err != nil || v != 1.5 {
		t.Fatalf("v=%v err=%v", v, err)
	}
	if _, err := parseRangeStart("npt=abc-"); err == nil {
		t.Fatal("invalid npt must fail")
	}
	if _, err := parseRangeStart("smpte=0-"); err == nil {
		t.Fatal("unsupported unit must fail")
	}
}
