package engine

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"time"

	"socksrevivepc/internal/config"
)

const (
	payloadHTTPStatusPeekTimeoutMs = 1500
	payloadHTTPStatusLineLimit     = 4096
	maxPayloadHTTPResponsesToSkip  = 12
	maxPayloadHTTPHeaderLines      = 80
	maxPayloadHTTPBodyDiscardBytes = 1024 * 1024
)

type PayloadResult struct {
	StatusLine string
	StatusCode int
}

type httpResponseInfo struct {
	contentLength int64
	chunked       bool
}

type preloadedConn struct {
	net.Conn
	preloaded *bytes.Reader
}

func (c *preloadedConn) Read(p []byte) (int, error) {
	if c.preloaded != nil && c.preloaded.Len() > 0 {
		return c.preloaded.Read(p)
	}
	return c.Conn.Read(p)
}

func wrapConnWithPreloadedBytes(conn net.Conn, b []byte) net.Conn {
	if len(b) == 0 {
		return conn
	}
	return &preloadedConn{Conn: conn, preloaded: bytes.NewReader(b)}
}

func WritePayload(conn net.Conn, p config.Profile, targetHost string, targetPort int, logger *Logger) (PayloadResult, net.Conn, error) {
	payload := buildPayload(p.Payload.Text, targetHost, targetPort)
	parts, instant := splitPayload(payload)
	for i, part := range parts {
		if part == "" {
			continue
		}
		if _, err := io.WriteString(conn, part); err != nil {
			return PayloadResult{}, conn, err
		}
		if i < len(parts)-1 && !instant {
			time.Sleep(time.Duration(p.Payload.SplitDelayMs) * time.Millisecond)
		}
	}
	logger.Add("debug", "payload sent (%d bytes)", len(payload))

	if !p.Payload.WaitForResponse {
		return PayloadResult{}, conn, nil
	}

	return consumePayloadHTTPNegotiation(conn, p, payloadSourceLabel(p.Mode), logger)
}

func consumePayloadHTTPNegotiation(conn net.Conn, p config.Profile, source string, logger *Logger) (PayloadResult, net.Conn, error) {
	defer conn.SetReadDeadline(time.Time{})

	var last PayloadResult
	var captured *bytes.Buffer
	var sawSuccess bool

	for attempt := 0; attempt < maxPayloadHTTPResponsesToSkip; attempt++ {
		setPayloadReadDeadline(conn, p, attempt)
		captured = &bytes.Buffer{}
		line, err := readPayloadLinePreserveBytes(conn, captured, payloadHTTPStatusLineLimit)
		if err != nil {
			if isTimeoutErr(err) {
				if last.StatusCode >= 400 && !p.Payload.AcceptAnyStatus && !sawSuccess {
					return last, conn, fmt.Errorf("payload rejected with final status %d", last.StatusCode)
				}
				return last, wrapConnWithPreloadedBytes(conn, captured.Bytes()), nil
			}
			if err == io.EOF && captured.Len() > 0 {
				return last, wrapConnWithPreloadedBytes(conn, captured.Bytes()), nil
			}
			if last.StatusCode > 0 {
				return last, conn, nil
			}
			return PayloadResult{}, conn, fmt.Errorf("payload response read failed: %w", err)
		}

		cleanLine := strings.TrimSpace(line)
		if cleanLine == "" {
			return last, wrapConnWithPreloadedBytes(conn, captured.Bytes()), nil
		}

		if strings.HasPrefix(cleanLine, "SSH-") || !isHTTPStatusLine(cleanLine) {
			return last, wrapConnWithPreloadedBytes(conn, captured.Bytes()), nil
		}

		code := parseStatusCode(cleanLine)
		last = PayloadResult{StatusLine: cleanLine, StatusCode: code}
		logProxyStatus(logger, source, code, cleanLine)
		logHTTPCompatibilityStatus(logger, code, cleanLine)
		if code == 101 || (code >= 200 && code < 400) {
			sawSuccess = true
		}

		// The current bytes are confirmed HTTP/proxy negotiation bytes. Do not replay
		// them to the SSH transport. Only replay bytes when we detect SSH/non-HTTP
		// data or a partial line after timeout.
		captured = nil
		if err := consumePayloadHTTPHeadersAndBody(conn); err != nil {
			if isTimeoutErr(err) {
				return last, conn, nil
			}
			return last, conn, fmt.Errorf("payload response consume failed: %w", err)
		}

		// Keep peeking for another immediate HTTP status block. Some payload/proxy
		// chains return several statuses (for example 403 -> 403 -> 101). Returning
		// after the first status can make SSH read HTTP text instead of SSH-2.0.
	}

	if last.StatusCode >= 400 && !p.Payload.AcceptAnyStatus && !sawSuccess {
		return last, conn, fmt.Errorf("payload rejected with final status %d", last.StatusCode)
	}
	return last, conn, nil
}

func setPayloadReadDeadline(conn net.Conn, p config.Profile, attempt int) {
	timeoutMs := payloadHTTPStatusPeekTimeoutMs
	if attempt == 0 && p.Payload.ResponseTimeoutMs > 0 {
		timeoutMs = p.Payload.ResponseTimeoutMs
		if timeoutMs < payloadHTTPStatusPeekTimeoutMs {
			timeoutMs = payloadHTTPStatusPeekTimeoutMs
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Duration(timeoutMs) * time.Millisecond))
}

func readPayloadLinePreserveBytes(conn net.Conn, captured *bytes.Buffer, limit int) (string, error) {
	var line bytes.Buffer
	buf := make([]byte, 1)
	for line.Len() < limit {
		n, err := conn.Read(buf)
		if n > 0 {
			b := buf[0]
			_ = captured.WriteByte(b)
			_ = line.WriteByte(b)
			if b == '\n' {
				break
			}
		}
		if err != nil {
			if line.Len() > 0 && err == io.EOF {
				return line.String(), nil
			}
			return "", err
		}
		if n == 0 {
			continue
		}
	}
	if line.Len() == 0 {
		return "", io.EOF
	}
	return line.String(), nil
}

func consumePayloadHTTPHeadersAndBody(conn net.Conn) error {
	info := httpResponseInfo{contentLength: -1}
	for i := 0; i < maxPayloadHTTPHeaderLines; i++ {
		ignored := &bytes.Buffer{}
		line, err := readPayloadLinePreserveBytes(conn, ignored, payloadHTTPStatusLineLimit)
		if err != nil {
			return err
		}
		clean := strings.TrimSpace(line)
		if clean == "" {
			break
		}
		lower := strings.ToLower(clean)
		if strings.HasPrefix(lower, "content-length:") {
			if n, err := strconv.ParseInt(strings.TrimSpace(clean[strings.Index(clean, ":")+1:]), 10, 64); err == nil {
				info.contentLength = n
			}
		} else if strings.HasPrefix(lower, "transfer-encoding:") && strings.Contains(lower, "chunked") {
			info.chunked = true
		}
	}
	if info.chunked {
		return discardPayloadChunkedBody(conn)
	}
	if info.contentLength > 0 {
		return discardPayloadFixedLengthBody(conn, info.contentLength)
	}
	return nil
}

func discardPayloadFixedLengthBody(conn net.Conn, contentLength int64) error {
	remaining := contentLength
	if remaining > maxPayloadHTTPBodyDiscardBytes {
		remaining = maxPayloadHTTPBodyDiscardBytes
	}
	buf := make([]byte, 4096)
	for remaining > 0 {
		toRead := int64(len(buf))
		if remaining < toRead {
			toRead = remaining
		}
		n, err := conn.Read(buf[:int(toRead)])
		if n > 0 {
			remaining -= int64(n)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func discardPayloadChunkedBody(conn net.Conn) error {
	for i := 0; i < maxPayloadHTTPHeaderLines; i++ {
		ignored := &bytes.Buffer{}
		sizeLine, err := readPayloadLinePreserveBytes(conn, ignored, payloadHTTPStatusLineLimit)
		if err != nil {
			return err
		}
		cleanSize := strings.TrimSpace(sizeLine)
		if semi := strings.Index(cleanSize, ";"); semi >= 0 {
			cleanSize = strings.TrimSpace(cleanSize[:semi])
		}
		chunkSize, err := strconv.ParseInt(cleanSize, 16, 64)
		if err != nil {
			return nil
		}
		if chunkSize == 0 {
			return consumePayloadTrailingHeaders(conn)
		}
		if err := discardPayloadFixedLengthBody(conn, chunkSize); err != nil {
			return err
		}
		crlf := &bytes.Buffer{}
		_, _ = readPayloadLinePreserveBytes(conn, crlf, payloadHTTPStatusLineLimit)
	}
	return nil
}

func consumePayloadTrailingHeaders(conn net.Conn) error {
	for i := 0; i < maxPayloadHTTPHeaderLines; i++ {
		ignored := &bytes.Buffer{}
		line, err := readPayloadLinePreserveBytes(conn, ignored, payloadHTTPStatusLineLimit)
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) == "" {
			return nil
		}
	}
	return nil
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true
	}
	return false
}

func isHTTPStatusLine(statusLine string) bool {
	clean := strings.ToUpper(strings.TrimSpace(statusLine))
	return strings.HasPrefix(clean, "HTTP/1.") || strings.HasPrefix(clean, "HTTP/2") || strings.HasPrefix(clean, "HTTP/3")
}

func logProxyStatus(logger *Logger, source string, responseCode int, statusLine string) {
	cleanLine := strings.TrimSpace(statusLine)
	if cleanLine == "" {
		return
	}
	if source == "" {
		source = "PROXY"
	}
	logger.Add("info", "Proxy Status [%s]: %s", source, cleanLine)
}

func logHTTPCompatibilityStatus(logger *Logger, responseCode int, firstLine string) {
	switch responseCode {
	case 200:
		logger.Add("info", "Status: 200 (Connection established) Successful")
	case 101:
		logger.Add("info", "replace 200 OK")
		logger.Add("info", "HTTP/1.1 101 Websocket")
	case 100:
		logger.Add("info", "HTTP/1.1 100 Continue")
	case 301, 302, 400, 401, 403, 404, 407, 429, 500, 502, 503, 504:
		if strings.TrimSpace(firstLine) != "" {
			logger.Add("info", "%s", strings.TrimSpace(firstLine))
		} else {
			logger.Add("info", "HTTP/1.1 %d", responseCode)
		}
		logger.Add("info", "replace 200 OK")
		logger.Add("info", "Dragon Try!")
	}
}

func payloadSourceLabel(mode config.Mode) string {
	switch mode {
	case config.ModePayload:
		return "HTTP_PROXY"
	case config.ModePayloadSSL:
		return "SSL_PAYLOAD"
	default:
		return "PROXY"
	}
}

func buildPayload(tpl, host string, port int) string {
	portStr := strconv.Itoa(port)
	repl := map[string]string{
		"[host]":      host,
		"[port]":      portStr,
		"[host_port]": net.JoinHostPort(host, portStr),
		"[crlf]":      "\r\n",
		"[cr]":        "\r",
		"[lf]":        "\n",
		"[protocol]":  "HTTP/1.1",
		"[method]":    "CONNECT",
	}
	out := tpl
	for k, v := range repl {
		out = strings.ReplaceAll(out, k, v)
	}
	out = replaceRotate(out)
	return out
}

func replaceRotate(s string) string {
	for {
		start := strings.Index(s, "[rotate=")
		if start < 0 {
			return s
		}
		end := strings.Index(s[start:], "]")
		if end < 0 {
			return s
		}
		end += start
		body := strings.TrimPrefix(s[start:end+1], "[rotate=")
		body = strings.TrimSuffix(body, "]")
		choices := splitRotateChoices(body)
		choice := ""
		if len(choices) > 0 {
			choice = strings.TrimSpace(choices[rand.Intn(len(choices))])
		}
		s = s[:start] + choice + s[end+1:]
	}
}

func splitRotateChoices(body string) []string {
	return strings.FieldsFunc(body, func(r rune) bool {
		switch r {
		case ';', '#', ',', '\n', '\r', '\t':
			return true
		default:
			return false
		}
	})
}

func splitPayload(s string) ([]string, bool) {
	instant := strings.Contains(s, "[instant_split]")
	s = strings.ReplaceAll(s, "[instant_split]", "[split]")
	parts := strings.Split(s, "[split]")
	return parts, instant
}

func parseStatusCode(status string) int {
	fields := strings.Fields(status)
	if len(fields) < 2 {
		return 0
	}
	code, _ := strconv.Atoi(fields[1])
	return code
}
