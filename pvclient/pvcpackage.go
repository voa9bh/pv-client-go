package pvclient

import (
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	packetREQ      byte = 1
	packetRSP      byte = 2
	packetASY      byte = 3
	flagBig        byte = 1
	flagFuture     byte = 2
	telTXT         byte = 2
	rspOK          byte = 1
	rspERR         byte = 2
	statusStart    byte = 1
	statusMiddle   byte = 2
	statusEnd      byte = 3
	statusSingle   byte = 4
	maxPayload          = 1008
	maxJavaPayload      = 1<<31 - 1 - 10
)

type protocolError struct{ message string }

func (e *protocolError) Error() string { return "PV protocol: " + e.message }

func invalid(format string, args ...any) error {
	return &protocolError{fmt.Sprintf(format, args...)}
}

type packet struct {
	kind, flags, telegram, response, status, id byte
	data                                        []byte
}

func (p packet) validate() error {
	if p.kind < packetREQ || p.kind > packetASY {
		return invalid("unknown packet type %d", p.kind)
	}
	if p.flags & ^byte(0x0f) != 0 || (p.id != 0 && p.flags&flagFuture == 0) {
		return invalid("invalid flags %#x for futureID %d", p.flags, p.id)
	}
	if p.telegram < 1 || p.telegram > 3 || p.status < statusStart || p.status > statusSingle {
		return invalid("invalid telegram type %d or status %d", p.telegram, p.status)
	}
	if (p.kind == packetREQ && (p.response < 1 || p.response > 3)) ||
		(p.kind == packetRSP && p.response > rspERR) ||
		(p.kind == packetASY && p.response > rspERR && p.response < 128) {
		return invalid("invalid response type %d", p.response)
	}
	return nil
}

func readPacket(r io.Reader, limit int) (packet, error) {
	var header [10]byte
	if _, err := io.ReadFull(r, header[:8]); err != nil {
		return packet{}, err
	}
	p := packet{kind: header[0], flags: header[1], telegram: header[2],
		response: header[3], status: header[4], id: header[5]}
	if err := p.validate(); err != nil {
		return packet{}, err
	}
	size := uint32(binary.LittleEndian.Uint16(header[6:8]))
	if p.flags&flagBig != 0 {
		if _, err := io.ReadFull(r, header[8:10]); err != nil {
			return packet{}, err
		}
		size = binary.LittleEndian.Uint32(header[6:10])
	} else if size > maxPayload {
		return packet{}, invalid("standard payload exceeds %d bytes", maxPayload)
	}
	if size > maxJavaPayload || uint64(size) > uint64(limit) {
		return packet{}, invalid("payload size %d exceeds limit %d", size, limit)
	}
	p.data = make([]byte, int(size))
	_, err := io.ReadFull(r, p.data)
	return p, err
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func writePacket(w io.Writer, p packet) error {
	if err := p.validate(); err != nil {
		return err
	}
	if len(p.data) > maxJavaPayload || (p.flags&flagBig == 0 && len(p.data) > maxPayload) {
		return invalid("invalid outgoing payload size %d", len(p.data))
	}
	header := []byte{p.kind, p.flags, p.telegram, p.response, p.status, p.id, 0, 0}
	if p.flags&flagBig != 0 {
		header = append(header, 0, 0)
		binary.LittleEndian.PutUint32(header[6:], uint32(len(p.data)))
	} else {
		binary.LittleEndian.PutUint16(header[6:], uint16(len(p.data)))
	}
	if err := writeAll(w, header); err != nil {
		return err
	}
	return writeAll(w, p.data)
}

func textData(text string) []byte {
	if !strings.HasSuffix(text, "\x00") {
		text += "\x00"
	}
	return []byte(text)
}

func writeText(w io.Writer, text string, id byte, big bool) error {
	data := textData(text)
	flags := byte(0)
	if id != 0 {
		flags |= flagFuture
	}
	chunk := maxPayload
	if big {
		flags |= flagBig
		chunk = len(data)
	}
	for offset := 0; offset < len(data); offset += chunk {
		end := min(offset+chunk, len(data))
		status := statusMiddle
		switch {
		case len(data) <= chunk:
			status = statusSingle
		case offset == 0:
			status = statusStart
		case end == len(data):
			status = statusEnd
		}
		if err := writePacket(w, packet{packetREQ, flags, telTXT, telTXT, status, id, data[offset:end]}); err != nil {
			return err
		}
	}
	return nil
}

type messageKey struct{ kind, id byte }
type assembler map[messageKey]packet

func (a assembler) add(p packet, limit int) (packet, bool, error) {
	key := messageKey{p.kind, p.id}
	prev, exists := a[key]
	switch p.status {
	case statusSingle, statusStart:
		if exists {
			return packet{}, false, invalid("new message before previous message ended")
		}
		if len(p.data) > limit {
			return packet{}, false, invalid("message exceeds %d bytes", limit)
		}
		if p.status == statusSingle {
			return p, true, nil
		}
		a[key] = p
		return packet{}, false, nil
	case statusMiddle, statusEnd:
		if !exists {
			return packet{}, false, invalid("continuation without START")
		}
		if prev.flags != p.flags || prev.telegram != p.telegram || prev.response != p.response {
			return packet{}, false, invalid("fragment metadata changed")
		}
		if len(p.data) > limit-len(prev.data) {
			return packet{}, false, invalid("assembled message exceeds %d bytes", limit)
		}
		prev.data = append(prev.data, p.data...)
		if p.status == statusEnd {
			delete(a, key)
			return prev, true, nil
		}
		a[key] = prev
		return packet{}, false, nil
	default:
		return packet{}, false, invalid("unknown fragment status %d", p.status)
	}
}

// RemoteError retains the server's text, even when it is not a numeric error.
type RemoteError struct {
	Code int
	Text string
}

func (e *RemoteError) Error() string { return "PV server: " + e.Text }

func textResponse(p packet) (string, error) {
	if p.kind != packetRSP || p.telegram != telTXT {
		return "", invalid("expected text response, got packet type %d, telegram %d", p.kind, p.telegram)
	}
	text, _, _ := strings.Cut(string(p.data), "\x00")
	if p.response == rspERR {
		number, _, _ := strings.Cut(strings.TrimPrefix(text, "#"), ";")
		code, err := strconv.Atoi(strings.TrimSpace(number))
		if err != nil {
			code = -1
		}
		return "", &RemoteError{Code: code, Text: text}
	}
	return text, nil
}
