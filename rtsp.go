package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	maxRequestLine = 1024
	maxHeaderBytes = 8192
	maxBodyBytes   = 65536
)

type request struct {
	method  string
	uri     string
	proto   string
	headers map[string]string
	body    []byte
}

func (r *request) header(name string) string {
	return r.headers[strings.ToLower(name)]
}

var (
	errRequestTooLarge = errors.New("rtsp: request too large")
	errBadRequest      = errors.New("rtsp: malformed request")
)

// readFrame reads one RTSP request from the connection. Interleaved binary
// frames ('$') are consumed and reported via onInterleaved; RTCP frames are
// skipped. Message boundaries are preserved: exactly one request is returned
// per call and any buffered bytes remain for the next call.
func readFrame(br *bufio.Reader, onInterleaved func(channel uint8, payload []byte)) (*request, error) {
	for {
		b, err := br.Peek(1)
		if err != nil {
			return nil, err
		}
		if b[0] == '$' {
			header := make([]byte, 4)
			if _, err := io.ReadFull(br, header); err != nil {
				return nil, err
			}
			length := int(header[2])<<8 | int(header[3])
			payload := make([]byte, length)
			if _, err := io.ReadFull(br, payload); err != nil {
				return nil, err
			}
			if onInterleaved != nil {
				onInterleaved(header[1], payload)
			}
			continue
		}
		return readRequest(br)
	}
}

// readLine reads a single CRLF-terminated line with a hard size limit.
func readLine(br *bufio.Reader, limit int) (string, error) {
	var sb strings.Builder
	for {
		frag, err := br.ReadString('\n')
		sb.WriteString(frag)
		if sb.Len() > limit {
			return "", errRequestTooLarge
		}
		if err != nil {
			return "", err
		}
		line := sb.String()
		if strings.HasSuffix(line, "\r\n") {
			return strings.TrimSuffix(line, "\r\n"), nil
		}
		return "", errBadRequest
	}
}

func readRequest(br *bufio.Reader) (*request, error) {
	line, err := readLine(br, maxRequestLine)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 || !strings.HasPrefix(parts[2], "RTSP/") {
		return nil, errBadRequest
	}
	req := &request{
		method:  parts[0],
		uri:     parts[1],
		proto:   parts[2],
		headers: make(map[string]string),
	}
	total := 0
	for {
		hline, err := readLine(br, maxHeaderBytes)
		if err != nil {
			return nil, err
		}
		if hline == "" {
			break
		}
		total += len(hline) + 2
		if total > maxHeaderBytes {
			return nil, errRequestTooLarge
		}
		kv := strings.SplitN(hline, ":", 2)
		if len(kv) != 2 {
			return nil, errBadRequest
		}
		req.headers[strings.ToLower(strings.TrimSpace(kv[0]))] = strings.TrimSpace(kv[1])
	}
	if cl := req.header("Content-Length"); cl != "" {
		n, err := strconv.Atoi(cl)
		if err != nil || n < 0 || n > maxBodyBytes {
			return nil, errRequestTooLarge
		}
		req.body = make([]byte, n)
		if _, err := io.ReadFull(br, req.body); err != nil {
			return nil, err
		}
	}
	return req, nil
}

func writeResponse(w io.Writer, code int, reason string, headers []string, body string) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "RTSP/1.0 %d %s\r\n", code, reason)
	for _, h := range headers {
		sb.WriteString(h)
		sb.WriteString("\r\n")
	}
	if body != "" {
		fmt.Fprintf(&sb, "Content-Type: application/sdp\r\nContent-Length: %d\r\n", len(body))
	}
	sb.WriteString("\r\n")
	sb.WriteString(body)
	_, err := io.WriteString(w, sb.String())
	return err
}
