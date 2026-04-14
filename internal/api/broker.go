package api

import (
	"sync"
	"time"
)

// Broker manages SSE client channels.
// Call Notify() after new data is inserted; all connected clients receive an "update" event.
type Broker struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
	last    time.Time
	notifyN int64
}

func NewBroker() *Broker {
	return &Broker{clients: make(map[chan struct{}]struct{})}
}

func (b *Broker) Subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broker) Unsubscribe(ch chan struct{}) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
	close(ch)
}

func (b *Broker) Notify() {
	b.mu.Lock()
	b.last = time.Now()
	b.notifyN++
	for ch := range b.clients {
		select {
		case ch <- struct{}{}:
		default: // slow client: skip, don't block
		}
	}
	b.mu.Unlock()
}

func (b *Broker) ClientCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients)
}

func (b *Broker) LastNotifyUnix() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last.IsZero() {
		return 0
	}
	return b.last.Unix()
}

func (b *Broker) NotifyCount() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.notifyN
}
