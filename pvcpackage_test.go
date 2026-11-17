package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(p []byte) (int, error) {
	return w.Buffer.Write(p[:min(1, len(p))])
}

func TestJavaWireBytes(t *testing.T) {
	for _, big := range []bool{false, true} {
		w := new(shortWriter)
		if err := writeText(w, "hi", 7, big); err != nil {
			t.Fatal(err)
		}
		want := []byte{1, 2, 2, 2, 4, 7, 3, 0, 'h', 'i', 0}
		if big {
			want = []byte{1, 3, 2, 2, 4, 7, 3, 0, 0, 0, 'h', 'i', 0}
		}
		if !bytes.Equal(w.Bytes(), want) {
			t.Fatalf("wire %v, want %v", w.Bytes(), want)
		}
		// Literal bytes independently verify decoding, not just a round trip.
		p, err := readPacket(bytes.NewReader(want), 1024)
		if err != nil || p.id != 7 || string(p.data) != "hi\x00" {
			t.Fatalf("decode: %+v, %v", p, err)
		}
	}
}

func TestTextFragmentation(t *testing.T) {
	for _, text := range []string{
		"", "already terminated\x00", "  whitespace  ", "Grüße 世界",
		strings.Repeat("x", 1007), strings.Repeat("x", 1008),
		strings.Repeat("x", 1007) + "世界" + strings.Repeat("z", 1010),
	} {
		t.Run(stringLengthName(text), func(t *testing.T) {
			var wire bytes.Buffer
			if err := writeText(&wire, text, 32, false); err != nil {
				t.Fatal(err)
			}
			assembly := make(assembler)
			count := 0
			for wire.Len() > 0 {
				p, err := readPacket(&wire, 4096)
				if err != nil {
					t.Fatal(err)
				}
				count++
				if p.id != 32 || len(p.data) > 1008 {
					t.Fatalf("bad fragment: %+v", p)
				}
				result, complete, err := assembly.add(p, 4096)
				if err != nil {
					t.Fatal(err)
				}
				if complete && !bytes.Equal(result.data, textData(text)) {
					t.Fatalf("reassembled %q, want %q", result.data, textData(text))
				}
				if complete != (wire.Len() == 0) {
					t.Fatal("incorrect completion boundary")
				}
			}
			want := (len(textData(text)) + 1007) / 1008
			if count != want {
				t.Fatalf("%d packets, want %d", count, want)
			}
		})
	}
}

func stringLengthName(s string) string {
	return strings.ReplaceAll(s[:min(len(s), 10)], "\x00", "NUL")
}

func TestBigDataLength(t *testing.T) {
	var wire bytes.Buffer
	text := strings.Repeat("x", 70000)
	if err := writeText(&wire, text, 3, true); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire.Bytes()[:10], []byte{1, 3, 2, 2, 4, 3, 0x71, 0x11, 1, 0}) {
		t.Fatalf("incorrect 32-bit little-endian length: %v", wire.Bytes()[:10])
	}
	p, err := readPacket(&wire, 100000)
	if err != nil || len(p.data) != 70001 {
		t.Fatalf("BigData decode: %d bytes, %v", len(p.data), err)
	}
}

func TestInvalidPacketHeaders(t *testing.T) {
	valid := []byte{2, 2, 2, 1, 4, 1, 1, 0, 'x'}
	for _, test := range []struct {
		name  string
		index int
		value byte
	}{
		{"packet type", 0, 0}, {"unknown flags", 1, 0x80},
		{"missing future flag", 1, 0}, {"telegram", 2, 0},
		{"response", 3, 4}, {"status", 4, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := bytes.Clone(valid)
			wire[test.index] = test.value
			_, err := readPacket(bytes.NewReader(wire), 4096)
			var malformed *protocolError
			if !errors.As(err, &malformed) {
				t.Fatalf("expected protocol error, got %v", err)
			}
		})
	}
	for _, size := range []uint32{1009, 1 << 31, 0xffffffff} {
		flags := byte(2)
		header := []byte{2, flags, 2, 1, 4, 1, 0, 0}
		if size > 1009 {
			header[1] |= flagBig
			header = append(header, 0, 0)
			binary.LittleEndian.PutUint32(header[6:], size)
		} else {
			binary.LittleEndian.PutUint16(header[6:], uint16(size))
		}
		if _, err := readPacket(bytes.NewReader(header), 4096); err == nil {
			t.Fatalf("accepted excessive size %d", size)
		}
	}
	if _, err := readPacket(bytes.NewReader(valid), 0); err == nil {
		t.Fatal("ignored configured allocation limit")
	}
	if _, err := readPacket(bytes.NewReader(valid[:8]), 1024); !errors.Is(err, io.EOF) {
		t.Fatalf("expected missing payload error, got %v", err)
	}
}

func TestInvalidFragmentSequences(t *testing.T) {
	start := packet{packetRSP, flagFuture, telTXT, rspOK, statusStart, 1, []byte("a")}
	end := start
	end.status = statusEnd
	tests := [][]packet{
		{end}, {start, start},
		{start, {packetRSP, flagFuture, telTXT, rspERR, statusEnd, 1, nil}},
		{start, {packetRSP, flagFuture, 1, rspOK, statusEnd, 1, nil}},
		{start, {packetRSP, flagFuture | flagBig, telTXT, rspOK, statusEnd, 1, nil}},
		{start, {packetRSP, flagFuture, telTXT, rspOK, statusSingle, 1, nil}},
		{start, {packetRSP, flagFuture, telTXT, rspOK, statusEnd, 1, []byte("too large")}},
	}
	for i, packets := range tests {
		assembly := make(assembler)
		var err error
		for _, p := range packets {
			_, _, err = assembly.add(p, 2)
			if err != nil {
				break
			}
		}
		if err == nil {
			t.Fatalf("accepted invalid sequence %d", i)
		}
	}
}

func TestInterleavedFragments(t *testing.T) {
	assembly := make(assembler)
	for _, p := range []packet{
		{packetRSP, flagFuture, telTXT, rspOK, statusStart, 1, []byte("first")},
		{packetRSP, flagFuture, telTXT, rspOK, statusStart, 2, []byte("second")},
		{packetRSP, flagFuture, telTXT, rspOK, statusEnd, 2, []byte(" reply")},
		{packetRSP, flagFuture, telTXT, rspOK, statusEnd, 1, []byte(" reply")},
	} {
		result, complete, err := assembly.add(p, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if complete {
			want := "first reply"
			if p.id == 2 {
				want = "second reply"
			}
			if string(result.data) != want {
				t.Fatalf("interleaved reply: %q", result.data)
			}
		}
	}
	if len(assembly) != 0 {
		t.Fatal("completed assemblies not released")
	}
}

func TestTextResponse(t *testing.T) {
	p := packet{kind: packetRSP, telegram: telTXT, response: 0, data: []byte("  ok  \x00")}
	value, err := textResponse(p)
	if err != nil || value != "  ok  " {
		t.Fatalf("legacy response: %q, %v", value, err)
	}
	p.data = []byte("before\x00after\x00")
	if value, err := textResponse(p); err != nil || value != "before" {
		t.Fatalf("C-style termination: %q, %v", value, err)
	}
	for _, text := range []string{"42", "#42;detail", "non-numeric error"} {
		p.response, p.data = rspERR, textData(text)
		_, err := textResponse(p)
		var remote *RemoteError
		if !errors.As(err, &remote) || remote.Text != text {
			t.Fatalf("error text lost: %v", err)
		}
		if text != "non-numeric error" && remote.Code != 42 {
			t.Fatalf("error code lost: %+v", remote)
		}
	}
}
