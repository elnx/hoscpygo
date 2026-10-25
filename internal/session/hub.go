package session

import (
	"encoding/json"
	"sync"
)

// Message is either a binary video/image frame or a JSON control message.
type Message struct {
	Binary bool
	Data   []byte
}

// Hub fans frames out to WebSocket subscribers.
type Hub struct {
	mu      sync.Mutex
	clients map[*Subscriber]struct{}
	closed  bool
}

func NewHub() *Hub { return &Hub{clients: map[*Subscriber]struct{}{}} }

// Subscriber is one browser connection.
type Subscriber struct {
	C    chan Message
	done chan struct{}
	hub  *Hub
}

func (h *Hub) Subscribe() *Subscriber {
	s := &Subscriber{C: make(chan Message, 32), done: make(chan struct{}), hub: h}
	h.mu.Lock()
	if h.closed {
		close(s.done)
	} else {
		h.clients[s] = struct{}{}
	}
	h.mu.Unlock()
	return s
}

// SendJSON queues a message to this subscriber only.
func (s *Subscriber) SendJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case s.C <- Message{Data: b}:
	default:
	}
}

func (s *Subscriber) Messages() <-chan Message { return s.C }
func (s *Subscriber) Done() <-chan struct{}    { return s.done }

func (s *Subscriber) Close() {
	s.hub.mu.Lock()
	if _, ok := s.hub.clients[s]; ok {
		delete(s.hub.clients, s)
		close(s.done)
	}
	s.hub.mu.Unlock()
}

func (h *Hub) BroadcastBinary(b []byte) {
	h.broadcast(Message{Binary: true, Data: b}, true)
}

func (h *Hub) BroadcastJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.broadcast(Message{Data: b}, false)
}

func (h *Hub) broadcast(m Message, droppable bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for c := range h.clients {
		select {
		case c.C <- m:
		default:
			if droppable {
				select {
				case <-c.C:
				default:
				}
				select {
				case c.C <- m:
				default:
				}
			}
		}
	}
}

func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for c := range h.clients {
		close(c.done)
		delete(h.clients, c)
	}
}
