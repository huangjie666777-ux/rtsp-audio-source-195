package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

const (
	sampleRate     = 8000
	samplesPerTick = 160 // 20ms at 8kHz
	tickInterval   = 20 * time.Millisecond
	writeTimeout   = 5 * time.Second
)

type server struct {
	data     []byte
	duration float64 // seconds
}

func newServer(path string) (*server, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read media file: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("media file %s is empty", path)
	}
	return &server{
		data:     data,
		duration: float64(len(data)) / sampleRate,
	}, nil
}

func (s *server) listenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Printf("RTSP server listening on %s, resource /demo, duration %.3fs\n", addr, s.duration)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		h := newConnHandler(s, conn)
		go h.run()
	}
}

// sdp builds the session description: payload type 0 (PCMU), 8kHz mono,
// single track trackID=0, total duration announced via a=range.
func (s *server) sdp(uri string) string {
	return fmt.Sprintf(
		"v=0\r\n"+
			"o=- 0 0 IN IP4 127.0.0.1\r\n"+
			"s=RTSP PCMU Demo\r\n"+
			"c=IN IP4 0.0.0.0\r\n"+
			"t=0 0\r\n"+
			"a=range:npt=0-%.3f\r\n"+
			"a=control:*\r\n"+
			"m=audio 0 RTP/AVP 0\r\n"+
			"a=rtpmap:0 PCMU/8000/1\r\n"+
			"a=control:trackID=0\r\n",
		s.duration)
}
