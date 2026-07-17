package api

import "testing"

func TestBrokerBroadcastsAllSource(t *testing.T) {
	broker := NewBroker()
	ch := broker.Subscribe()
	defer broker.Unsubscribe(ch)
	broker.NotifySource("all")
	if got := <-ch; got != "all" {
		t.Fatalf("tag = %q, want all", got)
	}
}
