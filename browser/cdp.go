package browser

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// CallTimeout bounds how long Call waits for the browser to answer.
const CallTimeout = 15 * time.Second

// Conn speaks the DevTools protocol over the browser's debugging pipe.
// Each message is one JSON object followed by a NUL byte.
type Conn struct {
	w   io.WriteCloser
	wmu sync.Mutex

	mu      sync.Mutex
	next    int64
	pending map[int64]chan reply
	subs    map[string]chan json.RawMessage
	err     error
	done    chan struct{}
}

type reply struct {
	result json.RawMessage
	err    *Error
}

// Error is an error returned by the browser for one call.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("devtools: %s (%d)", e.Message, e.Code) }

type envelope struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}

// NewConn starts reading from r and returns a connection that writes to w.
func NewConn(r io.Reader, w io.WriteCloser) *Conn {
	c := &Conn{
		w:       w,
		pending: map[int64]chan reply{},
		subs:    map[string]chan json.RawMessage{},
		done:    make(chan struct{}),
	}
	go c.read(r)
	return c
}

func (c *Conn) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		msg, err := br.ReadBytes(0)
		if err != nil {
			c.fail(fmt.Errorf("devtools pipe closed: %w", err))
			return
		}
		var env envelope
		if err := json.Unmarshal(msg[:len(msg)-1], &env); err != nil {
			continue
		}
		c.mu.Lock()
		if env.Method != "" {
			ch := c.subs[env.SessionID+"|"+env.Method]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- env.Params:
				default: // the subscriber is behind; drop the event
				}
			}
			continue
		}
		ch := c.pending[env.ID]
		delete(c.pending, env.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- reply{env.Result, env.Error}
		}
	}
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	close(c.done)
}

func (c *Conn) send(session, method string, params any, ch chan reply) error {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.next++
	id := c.next
	if ch != nil {
		c.pending[id] = ch
	}
	c.mu.Unlock()

	env := struct {
		ID        int64  `json:"id"`
		SessionID string `json:"sessionId,omitempty"`
		Method    string `json:"method"`
		Params    any    `json:"params,omitempty"`
	}{id, session, method, params}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	b = append(b, 0)
	c.wmu.Lock()
	_, err = c.w.Write(b)
	c.wmu.Unlock()
	if err != nil && ch != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}
	return err
}

// Call runs one method and decodes its result into result, which may be nil.
// session is empty for browser-level methods.
func (c *Conn) Call(session, method string, params, result any) error {
	ch := make(chan reply, 1)
	if err := c.send(session, method, params, ch); err != nil {
		return err
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("%s: %w", method, r.err)
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(r.result, result)
	case <-c.done:
		return c.err
	case <-time.After(CallTimeout):
		return errors.New(method + ": no answer from the browser")
	}
}

// Send runs one method and discards its answer.
func (c *Conn) Send(session, method string, params any) error {
	return c.send(session, method, params, nil)
}

// Subscribe returns a channel of the params of one event. Events are dropped
// while the channel is full. Subscribe before enabling the event's domain.
func (c *Conn) Subscribe(session, method string, buffer int) <-chan json.RawMessage {
	ch := make(chan json.RawMessage, buffer)
	c.mu.Lock()
	c.subs[session+"|"+method] = ch
	c.mu.Unlock()
	return ch
}

// Close closes the write side of the pipe.
func (c *Conn) Close() error { return c.w.Close() }
