package api

import (
	"sync"
	"time"
)

// Broker manages SSE client channels.
//
// Each Notify call broadcasts a "source" tag (`claude` or `codex`) so the web
// UI can selectively refresh only the active tab. Notify() (no args) is kept
// for backward compatibility and tags the event as "claude".
type Broker struct {
	mu         sync.Mutex
	clients    map[chan string]struct{}
	last       time.Time
	lastSource string
	notifyN    int64
}

func NewBroker() *Broker {
	return &Broker{clients: make(map[chan string]struct{})}
}

func (b *Broker) Subscribe() chan string {
	ch := make(chan string, 1)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broker) Unsubscribe(ch chan string) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
	close(ch)
}

// Notify broadcasts a Claude-tagged update. Equivalent to NotifySource("claude").
func (b *Broker) Notify() {
	b.NotifySource("claude")
}

// NotifySource broadcasts an update tagged with the given source.
func (b *Broker) NotifySource(source string) {
	if source == "" {
		source = "claude"
	}
	b.mu.Lock()
	b.last = time.Now()
	b.lastSource = source
	b.notifyN++
	for ch := range b.clients {
		select {
		case ch <- source:
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

// LastSource returns the source tag of the most recent broadcast, or "" if no
// broadcast has happened yet.
func (b *Broker) LastSource() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastSource
}
