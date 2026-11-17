package pvclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

var (
	ErrClosed       = errors.New("PVClient is closed")
	ErrNotConnected = errors.New("PVClient has not connected")
	ErrDisconnected = errors.New("PVClient connection lost")
)

type Options struct {
	ConnectTimeout       time.Duration
	RequestTimeout       time.Duration
	ReconnectInterval    time.Duration
	MaxMessageSize       int
	BigData              bool
	ResendAfterReconnect bool
}

func defaultOptions(o Options) (Options, error) {
	if o.ConnectTimeout == 0 {
		o.ConnectTimeout = 2500 * time.Millisecond
	}
	if o.RequestTimeout == 0 {
		o.RequestTimeout = 30 * time.Second
	}
	if o.ReconnectInterval == 0 {
		o.ReconnectInterval = 2500 * time.Millisecond
	}
	if o.MaxMessageSize == 0 {
		o.MaxMessageSize = 16 << 20
	}
	if o.ConnectTimeout < 0 || o.RequestTimeout < 0 || o.ReconnectInterval < 0 ||
		o.MaxMessageSize < len("!context off\x00") || o.MaxMessageSize > maxJavaPayload {
		return o, errors.New("invalid PVClient timeouts or message size")
	}
	return o, nil
}

// Future represents one logical request, including any reconnect replays.
type Future struct {
	id     byte
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	text   string
	value  string
	err    error
	sent   bool // owned by the client's run goroutine
}

func (f *Future) FutureID() byte { return f.id }

func (f *Future) finish(value string, err error) {
	f.once.Do(func() {
		f.value, f.err = value, err
		close(f.done)
		f.cancel()
	})
}

func (f *Future) finished() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

// Wait can be called more than once, including concurrently.
func (f *Future) Wait() (string, error) {
	<-f.done
	return f.value, f.err
}

func (f *Future) Cancel() { f.cancel() }

type control struct {
	reconnect bool
	ctx       context.Context
	reply     chan error
}

type session struct {
	net.Conn
	done chan struct{}
	once sync.Once
	stop func() bool
}

func (s *session) close() {
	s.once.Do(func() {
		close(s.done)
		s.Conn.Close()
		if s.stop != nil {
			s.stop()
		}
	})
}

type incoming struct {
	session *session
	message packet
	err     error
}

type PVClient struct {
	address  string
	options  Options
	ctx      context.Context
	cancel   context.CancelFunc
	stopped  chan struct{}
	ids      chan byte
	submit   chan *Future
	controls chan control
	incoming chan incoming
	wake     chan struct{}
	readers  sync.WaitGroup
}

func NewPVClient(address string, options Options) (*PVClient, error) {
	if _, _, err := net.SplitHostPort(address); err != nil {
		return nil, fmt.Errorf("PVClient address: %w", err)
	}
	options, err := defaultOptions(options)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &PVClient{address: address, options: options, ctx: ctx, cancel: cancel,
		stopped: make(chan struct{}), ids: make(chan byte, 32),
		submit: make(chan *Future), controls: make(chan control),
		// Read all outstanding replies even while the owner is writing a request.
		incoming: make(chan incoming, 32), wake: make(chan struct{}, 1)}
	for id := byte(1); id <= 32; id++ {
		c.ids <- id
	}
	go c.run()
	return c, nil
}

func (c *PVClient) Connect(ctx context.Context) error   { return c.control(ctx, false) }
func (c *PVClient) Reconnect(ctx context.Context) error { return c.control(ctx, true) }

func (c *PVClient) control(ctx context.Context, reconnect bool) error {
	request := control{reconnect, ctx, make(chan error, 1)}
	select {
	case <-c.ctx.Done():
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case c.controls <- request:
	}
	select {
	case <-c.ctx.Done():
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case err := <-request.reply:
		if c.ctx.Err() != nil {
			return ErrClosed
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
}

func (c *PVClient) Close() error {
	c.cancel()
	<-c.stopped
	return nil
}

func (c *PVClient) AskTxt(text string) (string, error) {
	return c.AskTxtContext(context.Background(), text)
}

func (c *PVClient) AskTxtContext(ctx context.Context, text string) (string, error) {
	f, err := c.AskTxtAsync(ctx, text)
	if err != nil {
		return "", err
	}
	return f.Wait()
}

func (c *PVClient) AskTxtAsync(ctx context.Context, text string) (*Future, error) {
	size := len(text)
	if len(text) == 0 || text[len(text)-1] != 0 {
		size++
	}
	if size > c.options.MaxMessageSize {
		return nil, fmt.Errorf("request exceeds %d bytes", c.options.MaxMessageSize)
	}
	ctx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
	var id byte
	select {
	case <-c.ctx.Done():
		cancel()
		return nil, ErrClosed
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case id = <-c.ids:
	}
	f := &Future{id: id, ctx: ctx, cancel: cancel, done: make(chan struct{}), text: text}
	context.AfterFunc(ctx, func() {
		f.finish("", ctx.Err())
		select {
		case c.wake <- struct{}{}:
		default:
		}
	})
	select {
	case <-c.ctx.Done():
		f.finish("", ErrClosed)
		c.ids <- id
		return nil, ErrClosed
	case <-ctx.Done():
		f.finish("", ctx.Err())
		c.ids <- id
		return nil, ctx.Err()
	case c.submit <- f:
		return f, nil
	}
}

func (c *PVClient) dial(parent context.Context) (*session, error) {
	ctx, cancel := context.WithTimeout(parent, c.options.ConnectTimeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", c.address)
	if err != nil {
		return nil, err
	}
	s := &session{Conn: conn, done: make(chan struct{})}
	// This hook also interrupts initialization reads when the caller cancels.
	interrupt := context.AfterFunc(ctx, func() { conn.Close() })
	ok := false
	defer func() {
		interrupt()
		if !ok {
			s.close()
		}
	}()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	commands := []string{"!context off"}
	if c.options.BigData {
		commands = append(commands, "!big on")
	}
	for _, command := range commands {
		if err := writeText(conn, command, 0, false); err != nil {
			return nil, err
		}
		assembly := make(assembler)
		for {
			p, err := readPacket(conn, c.options.MaxMessageSize)
			if err != nil {
				return nil, err
			}
			p, complete, err := assembly.add(p, c.options.MaxMessageSize)
			if err != nil {
				return nil, err
			}
			if !complete {
				continue
			}
			if p.kind == packetASY {
				slog.Warn("ignoring unsolicited PV message during initialization")
				continue
			}
			if p.id != 0 {
				return nil, invalid("initialization response has futureID %d", p.id)
			}
			if _, err := textResponse(p); err != nil {
				return nil, fmt.Errorf("%s: %w", command, err)
			}
			break
		}
	}
	if !interrupt() && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	s.stop = context.AfterFunc(c.ctx, func() { conn.Close() })
	ok = true
	return s, nil
}

func (c *PVClient) read(s *session) {
	defer c.readers.Done()
	assembly := make(assembler)
	for {
		p, err := readPacket(s, c.options.MaxMessageSize)
		complete := false
		if err == nil {
			p, complete, err = assembly.add(p, c.options.MaxMessageSize)
		}
		if err == nil && !complete {
			continue
		}
		if err == nil && p.kind == packetASY {
			slog.Warn("ignoring unsolicited PV message", "responseType", int8(p.response))
			continue
		}
		select {
		case <-c.ctx.Done():
			return
		case <-s.done:
			return
		case c.incoming <- incoming{s, p, err}:
		}
		if err != nil {
			return
		}
	}
}

func (c *PVClient) run() {
	var current *session
	active := false
	pending := make(map[byte]*Future)
	var retry <-chan time.Time
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer func() {
		timer.Stop()
		if current != nil {
			current.close()
		}
		for _, f := range pending {
			f.finish("", ErrClosed)
		}
		c.readers.Wait()
		close(c.stopped)
	}()
	release := func(id byte) {
		delete(pending, id)
		c.ids <- id
	}
	schedule := func() {
		timer.Reset(c.options.ReconnectInterval)
		retry = timer.C
	}
	disconnect := func(cause error, replay bool) {
		if current != nil {
			current.close()
			current = nil
		}
		for id, f := range pending {
			if c.ctx.Err() != nil {
				f.finish("", ErrClosed)
				release(id)
			} else if f.finished() {
				release(id)
			} else if f.sent && (!replay || !c.options.ResendAfterReconnect) {
				f.finish("", fmt.Errorf("%w: %w", ErrDisconnected, cause))
				release(id)
			} else {
				f.sent = false
			}
		}
		if active {
			schedule()
		}
	}
	send := func(f *Future) error {
		if f.finished() {
			return nil
		}
		deadline, _ := f.ctx.Deadline()
		if err := current.SetWriteDeadline(deadline); err != nil {
			return err
		}
		conn := current.Conn
		interrupted := make(chan struct{})
		stop := context.AfterFunc(f.ctx, func() {
			conn.SetWriteDeadline(time.Now())
			close(interrupted)
		})
		f.sent = true
		err := writeText(conn, f.text, f.id, c.options.BigData)
		if !stop() {
			<-interrupted
		}
		if err != nil {
			return err
		}
		return conn.SetWriteDeadline(time.Time{})
	}
	establish := func(ctx context.Context) error {
		s, err := c.dial(ctx)
		if err != nil {
			return err
		}
		current, active = s, true
		timer.Stop()
		retry = nil
		c.readers.Add(1)
		go c.read(s)
		for id, f := range pending {
			if f.finished() {
				release(id)
				continue
			}
			if err := send(f); err != nil {
				disconnect(err, true)
				return err
			}
		}
		return nil
	}
	for {
		select {
		case <-c.ctx.Done():
			return
		case request := <-c.controls:
			if err := request.ctx.Err(); err != nil {
				request.reply <- err
				continue
			}
			if request.reconnect {
				disconnect(errors.New("manual reconnect"), true)
			}
			if current != nil {
				request.reply <- nil
				continue
			}
			err := establish(request.ctx)
			if err != nil && active {
				schedule()
			}
			request.reply <- err
		case <-retry:
			retry = nil
			if err := establish(c.ctx); err != nil {
				slog.Warn("PVClient reconnect failed", "error", err)
				schedule()
			}
		case f := <-c.submit:
			if f.finished() {
				c.ids <- f.id
				continue
			}
			if !active {
				f.finish("", ErrNotConnected)
				c.ids <- f.id
				continue
			}
			pending[f.id] = f
			if current != nil {
				if err := send(f); err != nil {
					disconnect(err, true)
				}
			}
		case event := <-c.incoming:
			if event.session != current {
				continue
			}
			if event.err != nil {
				var malformed *protocolError
				disconnect(event.err, !errors.As(event.err, &malformed))
				continue
			}
			p := event.message
			f, exists := pending[p.id]
			if !exists || !f.sent {
				disconnect(invalid("unexpected response futureID %d", p.id), false)
				continue
			}
			value, err := textResponse(p)
			var malformed *protocolError
			if errors.As(err, &malformed) {
				disconnect(err, false)
				continue
			}
			f.finish(value, err)
			release(p.id)
		case <-c.wake:
			for id, f := range pending {
				// Sent requests retain their IDs until the old response is drained.
				if f.finished() && !f.sent {
					release(id)
				}
			}
		}
	}
}
