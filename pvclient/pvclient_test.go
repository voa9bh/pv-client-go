package pvclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mockServer(t *testing.T, handler func(net.Conn, int) error) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 64)
	wg.Add(1)
	go func() {
		defer wg.Done()
		index := 0
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			i := index
			index++
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				if err := handler(conn, i); err != nil {
					failures <- err
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		wg.Wait()
		close(failures)
		for err := range failures {
			t.Errorf("mock server: %v", err)
		}
	})
	return listener.Addr().String()
}

func readMessage(conn net.Conn) (packet, error) {
	assembly := make(assembler)
	for {
		p, err := readPacket(conn, 16<<20)
		if err != nil {
			return packet{}, err
		}
		p, complete, err := assembly.add(p, 16<<20)
		if err != nil || complete {
			return p, err
		}
	}
}

func respond(conn net.Conn, request packet, text string) error {
	return writePacket(conn, packet{packetRSP, request.flags, telTXT, rspOK,
		statusSingle, request.id, textData(text)})
}

func initialize(conn net.Conn, big bool) error {
	commands := []string{"!context off\x00"}
	if big {
		commands = append(commands, "!big on\x00")
	}
	for _, command := range commands {
		p, err := readMessage(conn)
		if err != nil {
			return err
		}
		if p.kind != packetREQ || p.telegram != telTXT || p.response != telTXT ||
			p.id != 0 || p.flags != 0 || string(p.data) != command {
			return fmt.Errorf("incorrect initialization: %+v", p)
		}
		p.kind, p.response, p.data = packetRSP, 0, []byte("OK\x00")
		if err := writePacket(conn, p); err != nil {
			return err
		}
	}
	return nil
}

func testClient(t *testing.T, address string, options Options) *PVClient {
	t.Helper()
	if options.ConnectTimeout == 0 {
		options.ConnectTimeout = time.Second
	}
	if options.ReconnectInterval == 0 {
		options.ReconnectInterval = 5 * time.Millisecond
	}
	if options.RequestTimeout == 0 {
		options.RequestTimeout = 2 * time.Second
	}
	c, err := NewPVClient(address, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}

func await(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for synchronization")
	}
}

func TestConcurrentFuturesOutOfOrder(t *testing.T) {
	address := mockServer(t, func(conn net.Conn, _ int) error {
		if err := initialize(conn, false); err != nil {
			return err
		}
		requests := make([]packet, 8)
		for i := range requests {
			p, err := readMessage(conn)
			if err != nil {
				return err
			}
			requests[i] = p
		}
		// Coalesce seven responses and split the first header into single-byte writes.
		var wire bytes.Buffer
		for i := len(requests) - 1; i >= 0; i-- {
			p := requests[i]
			p.kind, p.response = packetRSP, rspOK
			if err := writePacket(&wire, p); err != nil {
				return err
			}
		}
		data := wire.Bytes()
		for _, b := range data[:8] {
			if err := writeAll(conn, []byte{b}); err != nil {
				return err
			}
		}
		if err := writeAll(conn, data[8:]); err != nil {
			return err
		}
		_, err := io.Copy(io.Discard, conn)
		return err
	})
	c := testClient(t, address, Options{})
	var futures []*Future
	for i := 0; i < 8; i++ {
		f, err := c.AskTxtAsync(context.Background(), fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
		futures = append(futures, f)
	}
	for i, f := range futures {
		value, err := f.Wait()
		if err != nil || value != fmt.Sprint(i) {
			t.Fatalf("future %d: %q, %v", f.FutureID(), value, err)
		}
		again, err := f.Wait()
		if err != nil || again != value {
			t.Fatal("future not repeatable")
		}
	}
}

func TestFragmentedAndBigDataRequests(t *testing.T) {
	for _, big := range []bool{false, true} {
		t.Run(fmt.Sprint(big), func(t *testing.T) {
			text := strings.Repeat("x", 1007) + "世界" + strings.Repeat("z", 70000)
			address := mockServer(t, func(conn net.Conn, _ int) error {
				if err := initialize(conn, big); err != nil {
					return err
				}
				p, err := readMessage(conn)
				if err != nil {
					return err
				}
				if string(p.data) != text+"\x00" || (p.flags&flagBig != 0) != big {
					return errors.New("bad request data/BigData flag")
				}
				// Echo as fragmented standard packets, even when requests use BigData.
				var wire bytes.Buffer
				if err := writeText(&wire, text, p.id, false); err != nil {
					return err
				}
				reader := bytes.NewReader(wire.Bytes())
				for reader.Len() > 0 {
					fragment, err := readPacket(reader, 100000)
					if err != nil {
						return err
					}
					fragment.kind, fragment.response = packetRSP, rspOK
					if err := writePacket(conn, fragment); err != nil {
						return err
					}
				}
				_, err = io.Copy(io.Discard, conn)
				return err
			})
			c := testClient(t, address, Options{BigData: big})
			value, err := c.AskTxt(text)
			if err != nil || value != text {
				t.Fatalf("echo mismatch: %d bytes, %v", len(value), err)
			}
		})
	}
}

func TestReconnectResendPolicy(t *testing.T) {
	for _, mode := range []struct{ resend, manual bool }{
		{false, false}, {true, false}, {false, true}, {true, true},
	} {
		t.Run(fmt.Sprintf("resend=%t/manual=%t", mode.resend, mode.manual), func(t *testing.T) {
			resend := mode.resend
			reconnected := make(chan struct{})
			received := make(chan struct{})
			var originalID atomic.Uint32
			address := mockServer(t, func(conn net.Conn, index int) error {
				if err := initialize(conn, true); err != nil {
					return err
				}
				if index == 0 {
					p, err := readMessage(conn)
					originalID.Store(uint32(p.id))
					close(received)
					if err == nil && mode.manual {
						_, err = io.Copy(io.Discard, conn)
					}
					return err // Close without answering.
				}
				if index != 1 {
					return errors.New("unexpected additional connection")
				}
				close(reconnected)
				p, err := readMessage(conn)
				if err != nil {
					return err
				}
				if resend && uint32(p.id) != originalID.Load() {
					return errors.New("futureID changed across replay")
				}
				if err := respond(conn, p, "reconnected"); err != nil {
					return err
				}
				_, err = io.Copy(io.Discard, conn)
				return err
			})
			c := testClient(t, address, Options{BigData: true, ResendAfterReconnect: resend})
			future, err := c.AskTxtAsync(context.Background(), "request")
			if err != nil {
				t.Fatal(err)
			}
			await(t, received)
			if mode.manual {
				if err := c.Reconnect(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			value, err := future.Wait()
			if resend {
				if err != nil || value != "reconnected" {
					t.Fatalf("replay: %q, %v", value, err)
				}
			} else if !errors.Is(err, ErrDisconnected) {
				t.Fatalf("expected disconnect, got %v", err)
			}
			await(t, reconnected)
			if !resend {
				if value, err := c.AskTxt("new request"); err != nil || value != "reconnected" {
					t.Fatalf("new connection: %q, %v", value, err)
				}
			}
		})
	}
}

func TestCancellationSlotExhaustionAndLateResponse(t *testing.T) {
	full := make(chan struct{})
	drain := make(chan struct{})
	address := mockServer(t, func(conn net.Conn, _ int) error {
		if err := initialize(conn, false); err != nil {
			return err
		}
		var first packet
		for i := 0; i < 32; i++ {
			p, err := readMessage(conn)
			if err != nil {
				return err
			}
			if i == 0 {
				first = p
			}
		}
		close(full)
		select {
		case <-drain:
		case <-time.After(3 * time.Second):
			return errors.New("test did not release late response")
		}
		if err := respond(conn, first, "late"); err != nil {
			return err
		}
		p, err := readMessage(conn)
		if err != nil {
			return err
		}
		if p.id != first.id {
			return fmt.Errorf("expected recycled ID %d, got %d", first.id, p.id)
		}
		if err := respond(conn, p, "fresh"); err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, conn)
		return err
	})
	c := testClient(t, address, Options{})
	var futures []*Future
	for i := 0; i < 32; i++ {
		f, err := c.AskTxtAsync(context.Background(), "request")
		if err != nil {
			t.Fatal(err)
		}
		futures = append(futures, f)
	}
	await(t, full)
	futures[0].Cancel()
	if _, err := futures[0].Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := c.AskTxtAsync(ctx, "cannot reuse canceled ID yet"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected slot wait deadline, got %v", err)
	}
	close(drain)
	value, err := c.AskTxt("fresh request")
	if err != nil || value != "fresh" {
		t.Fatalf("late response misrouted: %q, %v", value, err)
	}
	c.Close()
	for _, f := range futures[1:] {
		if _, err := f.Wait(); !errors.Is(err, ErrClosed) {
			t.Fatalf("close: %v", err)
		}
	}
}

func TestRemoteErrorAndAsyncIsolation(t *testing.T) {
	address := mockServer(t, func(conn net.Conn, _ int) error {
		if err := initialize(conn, false); err != nil {
			return err
		}
		p, err := readMessage(conn)
		if err != nil {
			return err
		}
		if err := writePacket(conn, packet{packetASY, flagFuture, telTXT, 0, statusSingle, p.id, []byte("event")}); err != nil {
			return err
		}
		p.kind, p.response, p.data = packetRSP, rspERR, []byte("#42;failure\x00")
		if err := writePacket(conn, p); err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, conn)
		return err
	})
	c := testClient(t, address, Options{ResendAfterReconnect: true})
	_, err := c.AskTxt("fails")
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code != 42 || remote.Text != "#42;failure" {
		t.Fatalf("expected server error, got %v", err)
	}
}

func TestManualReconnectAndStaleReader(t *testing.T) {
	address := mockServer(t, func(conn net.Conn, index int) error {
		if err := initialize(conn, false); err != nil {
			return err
		}
		if index == 0 {
			_, err := io.Copy(io.Discard, conn)
			return err
		}
		p, err := readMessage(conn)
		if err != nil {
			return err
		}
		if err := respond(conn, p, "new session"); err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, conn)
		return err
	})
	c := testClient(t, address, Options{})
	if err := c.Reconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case c.incoming <- incoming{session: &session{}, err: io.EOF}:
	case <-time.After(time.Second):
		t.Fatal("manager did not accept stale reader event")
	}
	value, err := c.AskTxt("request")
	if err != nil || value != "new session" {
		t.Fatalf("stale reader affected new socket: %q, %v", value, err)
	}
}

func TestBigDataNegotiationFailure(t *testing.T) {
	address := mockServer(t, func(conn net.Conn, _ int) error {
		if err := initialize(conn, false); err != nil {
			return err
		}
		p, err := readMessage(conn)
		if err != nil {
			return err
		}
		if string(p.data) != "!big on\x00" {
			return errors.New("missing negotiation")
		}
		p.kind, p.response, p.data = packetRSP, rspERR, []byte("#1;unsupported\x00")
		return writePacket(conn, p)
	})
	c, err := NewPVClient(address, Options{BigData: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var remote *RemoteError
	if err := c.Connect(context.Background()); !errors.As(err, &remote) {
		t.Fatalf("negotiation should fail explicitly: %v", err)
	}
}

func TestCloseUnblocksInitialization(t *testing.T) {
	received := make(chan struct{})
	address := mockServer(t, func(conn net.Conn, _ int) error {
		if _, err := readMessage(conn); err != nil {
			return err
		}
		close(received)
		_, err := io.Copy(io.Discard, conn)
		return err
	})
	c, err := NewPVClient(address, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	result := make(chan error, 1)
	go func() { result <- c.Connect(context.Background()) }()
	await(t, received)
	c.Close()
	if err := <-result; !errors.Is(err, ErrClosed) {
		t.Fatalf("close during initialization: %v", err)
	}
}

func TestRequestTimeoutAndMalformedResponse(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			address := mockServer(t, func(conn net.Conn, _ int) error {
				if err := initialize(conn, false); err != nil {
					return err
				}
				p, err := readMessage(conn)
				if err != nil {
					return err
				}
				if malformed {
					if err := writeAll(conn, []byte{2, 2, 2, 1, 0, p.id, 0, 0}); err != nil {
						return err
					}
				}
				_, err = io.Copy(io.Discard, conn)
				return err
			})
			// A long reconnect interval avoids another session before assertions/cleanup.
			c := testClient(t, address, Options{RequestTimeout: 40 * time.Millisecond,
				ReconnectInterval: time.Hour, ResendAfterReconnect: true})
			_, err := c.AskTxt("request")
			if malformed {
				var protocol *protocolError
				if !errors.As(err, &protocol) {
					t.Fatalf("expected protocol error without replay, got %v", err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected request timeout, got %v", err)
			}
		})
	}
}

func TestReadsContinueWhileWriting(t *testing.T) {
	firstTwo := make(chan struct{})
	thirdSubmitted := make(chan struct{})
	reply := strings.Repeat("r", 2<<20)
	largeRequest := strings.Repeat("q", 8<<20)
	address := mockServer(t, func(conn net.Conn, _ int) error {
		if err := initialize(conn, true); err != nil {
			return err
		}
		first, err := readMessage(conn)
		if err != nil {
			return err
		}
		second, err := readMessage(conn)
		if err != nil {
			return err
		}
		close(firstTwo)
		select {
		case <-thirdSubmitted:
		case <-time.After(3 * time.Second):
			return errors.New("third request not submitted")
		}
		// The client is writing a large third request, while the server writes
		// two large earlier replies before reading that third request.
		if err := respond(conn, first, reply); err != nil {
			return err
		}
		if err := respond(conn, second, reply); err != nil {
			return err
		}
		third, err := readMessage(conn)
		if err != nil {
			return err
		}
		if string(third.data) != largeRequest+"\x00" {
			return errors.New("large request corrupted")
		}
		if err := respond(conn, third, "third"); err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, conn)
		return err
	})
	c := testClient(t, address, Options{BigData: true, RequestTimeout: 4 * time.Second})
	first, err := c.AskTxtAsync(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.AskTxtAsync(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	await(t, firstTwo)
	third, err := c.AskTxtAsync(context.Background(), largeRequest)
	if err != nil {
		t.Fatal(err)
	}
	close(thirdSubmitted)
	for _, f := range []*Future{first, second} {
		if value, err := f.Wait(); err != nil || value != reply {
			t.Fatalf("earlier reply: %d bytes, %v", len(value), err)
		}
	}
	if value, err := third.Wait(); err != nil || value != "third" {
		t.Fatalf("third reply: %q, %v", value, err)
	}
}

func TestCloseUnblocksWriterAndSubmitter(t *testing.T) {
	receivedHeader := make(chan struct{})
	drain := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(drain) })
	address := mockServer(t, func(conn net.Conn, _ int) error {
		if err := initialize(conn, true); err != nil {
			return err
		}
		if err := conn.(*net.TCPConn).SetReadBuffer(1024); err != nil {
			return err
		}
		var header [10]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return err
		}
		close(receivedHeader)
		select {
		case <-drain:
		case <-time.After(3 * time.Second):
			return errors.New("Close did not interrupt the writer")
		}
		return nil
	})
	c := testClient(t, address, Options{BigData: true})
	f, err := c.AskTxtAsync(context.Background(), strings.Repeat("x", 8<<20))
	if err != nil {
		t.Fatal(err)
	}
	await(t, receivedHeader)
	submission := make(chan error, 1)
	go func() {
		f, err := c.AskTxtAsync(context.Background(), "another")
		if err == nil {
			_, err = f.Wait()
		}
		submission <- err
	}()
	c.Close()
	release.Do(func() { close(drain) })
	if _, err := f.Wait(); !errors.Is(err, ErrClosed) {
		t.Fatalf("pending request during Close: %v", err)
	}
	if err := <-submission; !errors.Is(err, ErrClosed) {
		t.Fatalf("blocked submitter during Close: %v", err)
	}
}

func TestConnectCancellationAndFailure(t *testing.T) {
	received := make(chan struct{})
	address := mockServer(t, func(conn net.Conn, _ int) error {
		if _, err := readMessage(conn); err != nil {
			return err
		}
		close(received)
		_, err := io.Copy(io.Discard, conn)
		return err
	})
	c, err := NewPVClient(address, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- c.Connect(ctx) }()
	await(t, received)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled initialization: %v", err)
	}
	if _, err := c.AskTxt("still not connected"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("failed initial Connect activated retries: %v", err)
	}
}
func TestDefaultsAndInvalidOptions(t *testing.T) {
	o, err := defaultOptions(Options{})
	if err != nil || o.ConnectTimeout != 2500*time.Millisecond ||
		o.ReconnectInterval != 2500*time.Millisecond || o.RequestTimeout != 30*time.Second ||
		o.MaxMessageSize != 16<<20 || o.ResendAfterReconnect || o.BigData {
		t.Fatalf("unexpected defaults: %+v, %v", o, err)
	}
	for _, options := range []Options{
		{ConnectTimeout: -1}, {RequestTimeout: -1}, {ReconnectInterval: -1}, {MaxMessageSize: -1},
	} {
		if _, err := NewPVClient("localhost:5002", options); err == nil {
			t.Fatal("accepted invalid options")
		}
	}
	if _, err := NewPVClient("missing port", Options{}); err == nil {
		t.Fatal("accepted invalid address")
	}
	c, err := NewPVClient("localhost:5002", Options{MaxMessageSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AskTxt("not connected"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("before connect: %v", err)
	}
	if _, err := c.AskTxt(strings.Repeat("x", 65)); err == nil {
		t.Fatal("accepted oversized request")
	}
	c.Close()
	if err := c.Connect(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("connect after close: %v", err)
	}
	if _, err := c.AskTxt("closed"); !errors.Is(err, ErrClosed) {
		t.Fatalf("request after close: %v", err)
	}
}

func TestReconnectRetriesInitialization(t *testing.T) {
	var attempts atomic.Int32
	address := mockServer(t, func(conn net.Conn, index int) error {
		attempts.Add(1)
		if index == 1 {
			p, err := readMessage(conn)
			if err != nil {
				return err
			}
			p.kind, p.response, p.data = packetRSP, rspERR, []byte("#7;not ready\x00")
			return writePacket(conn, p)
		}
		if err := initialize(conn, false); err != nil {
			return err
		}
		p, err := readMessage(conn)
		if err != nil {
			return err
		}
		if index == 0 {
			return nil
		}
		if err := respond(conn, p, "ready"); err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, conn)
		return err
	})
	c := testClient(t, address, Options{ResendAfterReconnect: true})
	value, err := c.AskTxt("retry")
	if err != nil || value != "ready" || attempts.Load() != 3 {
		t.Fatalf("retry initialization: %q, %v, %d connections", value, err, attempts.Load())
	}
}

func TestRequestExpiresDuringReconnect(t *testing.T) {
	firstLost := make(chan struct{})
	address := mockServer(t, func(conn net.Conn, index int) error {
		if err := initialize(conn, false); err != nil {
			return err
		}
		p, err := readMessage(conn)
		if err != nil {
			return err
		}
		if index == 0 {
			close(firstLost)
			return nil
		}
		if string(p.data) != "new\x00" {
			return errors.New("expired request was replayed")
		}
		if err := respond(conn, p, "ready"); err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, conn)
		return err
	})
	c := testClient(t, address, Options{ReconnectInterval: 100 * time.Millisecond})
	if _, err := c.AskTxt("lose connection"); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("first disconnect: %v", err)
	}
	await(t, firstLost)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.AskTxtContext(ctx, "expired"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline while reconnecting: %v", err)
	}
	if value, err := c.AskTxt("new"); err != nil || value != "ready" {
		t.Fatalf("request queued during reconnect: %q, %v", value, err)
	}
}
