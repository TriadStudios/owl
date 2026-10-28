package ui

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA

	closeUnsupported = 1003
	closeBadData     = 1007
	closePolicy      = 1008
	closeTooBig      = 1009

	wsGUID       = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	writeTimeout = 10 * time.Second
)

var (
	errProtocol = errors.New("websocket protocol error")
	errTooBig   = errors.New("websocket message too big")
)

type wsConn struct {
	conn net.Conn
	r    *bufio.Reader
	mu   sync.Mutex
}

func upgrade(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	raw, err := base64.StdEncoding.DecodeString(key)
	if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		!headerHas(r.Header, "Connection", "upgrade") || r.Header.Get("Sec-WebSocket-Version") != "13" || err != nil || len(raw) != 16 {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return nil, errors.New("not a websocket upgrade")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket unsupported", http.StatusInternalServerError)
		return nil, errors.New("connection cannot be hijacked")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}
	return &wsConn{conn: conn, r: rw.Reader}, nil
}

func headerHas(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func (c *wsConn) read(limit int, idle time.Duration) (byte, []byte, error) {
	var op byte
	var msg []byte
	for {
		if err := c.conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
			return 0, nil, err
		}
		var h [2]byte
		if _, err := io.ReadFull(c.r, h[:]); err != nil {
			return 0, nil, err
		}
		fin, opcode, masked := h[0]&0x80 != 0, h[0]&0x0F, h[1]&0x80 != 0
		if h[0]&0x70 != 0 || !masked {
			return 0, nil, errProtocol
		}
		n := uint64(h[1] & 0x7F)
		switch n {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(c.r, ext[:]); err != nil {
				return 0, nil, err
			}
			n = uint64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(c.r, ext[:]); err != nil {
				return 0, nil, err
			}
			n = binary.BigEndian.Uint64(ext[:])
		}
		control := opcode >= opClose
		if control && (n > 125 || !fin) {
			return 0, nil, errProtocol
		}
		if !control && n > uint64(limit-len(msg)) {
			return 0, nil, errTooBig
		}
		var mask [4]byte
		if _, err := io.ReadFull(c.r, mask[:]); err != nil {
			return 0, nil, err
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(c.r, payload); err != nil {
			return 0, nil, err
		}
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
		switch opcode {
		case opPing:
			if err := c.write(opPong, payload); err != nil {
				return 0, nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			if err := c.write(opClose, payload[:min(2, len(payload))]); err != nil {
				return 0, nil, err
			}
			return 0, nil, io.EOF
		case opContinuation:
			if op == 0 {
				return 0, nil, errProtocol
			}
		case opText, opBinary:
			if op != 0 {
				return 0, nil, errProtocol
			}
			op = opcode
		default:
			return 0, nil, errProtocol
		}
		msg = append(msg, payload...)
		if fin {
			return op, msg, nil
		}
	}
}

func (c *wsConn) write(op byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	frame := []byte{0x80 | op}
	switch n := len(payload); {
	case n < 126:
		frame = append(frame, byte(n))
	case n <= 0xFFFF:
		frame = binary.BigEndian.AppendUint16(append(frame, 126), uint16(n))
	default:
		frame = binary.BigEndian.AppendUint64(append(frame, 127), uint64(n))
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	_, err := c.conn.Write(append(frame, payload...))
	return err
}

func (c *wsConn) close(code uint16, reason string) error {
	if len(reason) > 123 {
		reason = reason[:123]
	}
	return errors.Join(c.write(opClose, append(binary.BigEndian.AppendUint16(nil, code), reason...)), c.conn.Close())
}
