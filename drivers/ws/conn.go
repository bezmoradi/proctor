package ws

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type frame struct {
	binary  bool
	text    string
	json    any
	hasJSON bool
}

type socket struct {
	conn   *websocket.Conn
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	mu          sync.Mutex
	frames      []frame
	bytes       int
	overflow    bool
	closed      bool
	closeCode   int
	closeReason string
}

type snapshot struct {
	frames      []frame
	overflow    bool
	closed      bool
	closeCode   int
	closeReason string
}

func open(conn *websocket.Conn) *socket {
	conn.SetReadLimit(-1)
	ctx, cancel := context.WithCancel(context.Background())
	s := &socket{conn: conn, cancel: cancel, done: make(chan struct{})}
	go s.read(ctx)
	return s
}

func (s *socket) read(ctx context.Context) {
	defer close(s.done)
	for {
		typ, r, err := s.conn.Reader(ctx)
		if err != nil {
			s.finish(err)
			return
		}
		s.mu.Lock()
		room := maxBytes - s.bytes
		full := s.overflow || len(s.frames) >= maxFrames
		s.mu.Unlock()
		if full {
			room = 0
		}
		b, err := io.ReadAll(io.LimitReader(r, int64(room)+1))
		if err == nil && len(b) > room {
			_, err = io.Copy(io.Discard, r)
			b = nil
			full = true
		}
		if err != nil {
			s.finish(err)
			return
		}
		s.mu.Lock()
		if full {
			s.overflow = true
		} else {
			f := frame{binary: typ == websocket.MessageBinary}
			if !f.binary {
				f.text = string(b)
				f.json, f.hasJSON = parseJSON(b)
			}
			s.frames = append(s.frames, f)
			s.bytes += len(b)
		}
		s.mu.Unlock()
	}
}

func (s *socket) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		s.closeCode = int(ce.Code)
		s.closeReason = ce.Reason
		return
	}
	s.closeCode = int(websocket.StatusAbnormalClosure)
}

func (s *socket) snapshot() snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return snapshot{
		frames:      append([]frame(nil), s.frames...),
		overflow:    s.overflow,
		closed:      s.closed,
		closeCode:   s.closeCode,
		closeReason: s.closeReason,
	}
}

func (s *socket) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *socket) shut() {
	s.once.Do(func() {
		finished := make(chan struct{})
		go func() {
			_ = s.conn.Close(websocket.StatusNormalClosure, "")
			close(finished)
		}()
		select {
		case <-finished:
		case <-time.After(closeWait):
			_ = s.conn.CloseNow()
		}
		s.cancel()
		select {
		case <-s.done:
		case <-time.After(closeWait):
		}
	})
}
