package main

import (
	"PVClient/pvclient"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestBrowserWithTCPClient(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverResult := make(chan error, 1)
	serverStopped := make(chan struct{})
	go func() {
		defer close(serverStopped)
		conn, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		script := []exchange{
			{command: "!context off\x00", reply: "OK\x00"},
			{command: "/pvServer.getNextChildObject(0)\x00", reply: "-1\x00"},
			{command: "#-1.name\x00", reply: "procs\x00"},
			{command: "/pvServer.getNextChildObject(-1)\x00", reply: "0\x00"},
			{command: "#-1.getNextChildObject(0)\x00", reply: "-2\x00"},
			{command: "#-2.name\x00", reply: "leaf\x00"},
			{command: "#-1.getNextChildObject(-2)\x00", reply: "0\x00"},
		}
		// Literal standard framing keeps this independent of private client helpers.
		for _, step := range script {
			var header [8]byte
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				serverResult <- err
				return
			}
			if header[0] != 1 || header[2] != 2 || header[3] != 2 || header[4] != 4 ||
				(header[1] != 0 && header[1] != 2) {
				serverResult <- fmt.Errorf("unexpected request header %v", header)
				return
			}
			payload := make([]byte, binary.LittleEndian.Uint16(header[6:]))
			if _, err := io.ReadFull(conn, payload); err != nil {
				serverResult <- err
				return
			}
			if string(payload) != step.command {
				serverResult <- fmt.Errorf("command %q, want %q", payload, step.command)
				return
			}
			header[0], header[3] = 2, 1
			binary.LittleEndian.PutUint16(header[6:], uint16(len(step.reply)))
			// Split replies into bytes to exercise incremental socket reads too.
			wire := append(header[:], []byte(step.reply)...)
			for _, b := range wire {
				if _, err := conn.Write([]byte{b}); err != nil {
					serverResult <- err
					return
				}
			}
		}
		serverResult <- nil
		io.Copy(io.Discard, conn)
	}()
	t.Cleanup(func() {
		listener.Close()
		select {
		case err := <-serverResult:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(6 * time.Second):
			t.Error("mock TCP server did not finish")
		}
		select {
		case <-serverStopped:
		case <-time.After(time.Second):
			t.Error("mock TCP connection did not close")
		}
	})
	client, err := pvclient.NewPVClient(listener.Addr().String(), pvclient.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	ui := startBrowser(t, client)
	ui.wait(t, func(s snapshot) bool { return s.states["/pvServer"] == loaded })
	ui.key(tcell.KeyDown, 0)
	ui.wait(t, func(s snapshot) bool { return s.selected == "#-1" })
	ui.key(tcell.KeyRight, 0)
	ui.wait(t, func(s snapshot) bool { return s.states["#-1"] == loaded })
	ui.key(tcell.KeyDown, 0)
	s := ui.wait(t, func(s snapshot) bool { return s.selected == "#-2" })
	if s.details != "Name: leaf\nAddress: #-2\nPath (informational): /pvServer/procs/leaf" {
		t.Fatalf("unexpected leaf details: %q", s.details)
	}
}
