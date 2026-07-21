package core

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func newEventDaemon() *daemon {
	return &daemon{epoch: "cpe_test", subs: map[string]*subscription{}, watchers: map[string]*watcher{}}
}

func TestEventRingGapReplayOverflowAndCap(t *testing.T) {
	d := newEventDaemon()
	d.eventSeq = 4
	now := time.Now()
	d.eventRing = []eventSummary{{Seq: 3, Kind: "operation.completed", PaneRef: "p", Version: 3, At: now}, {Seq: 4, Kind: "operation.completed", PaneRef: "p", Version: 4, At: now}}
	gap, err := d.newSubscription(1, "", "")
	if err != nil || gap.(map[string]any)["resyncRequired"] != true {
		t.Fatalf("stale cursor did not require resync: %#v %v", gap, err)
	}
	v, err := d.newSubscription(2, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	ref := v.(map[string]any)["subscriptionRef"].(string)
	d.subMu.Lock()
	sub := d.subs[ref]
	d.subMu.Unlock()
	if got := <-sub.ch; got["params"].(map[string]any)["eventSeq"] != uint64(3) {
		t.Fatalf("replay was not cursor ordered: %#v", got)
	}
	for i := 0; i < 65; i++ {
		d.enqueueSubscription(sub, eventSummary{Seq: uint64(i + 10), Kind: "pane.version", PaneRef: "p"}, uint64(i+10))
	}
	found := false
	for len(sub.ch) > 0 {
		if got := <-sub.ch; got["params"].(map[string]any)["resyncRequired"] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("subscription overflow silently lost events")
	}
	for i := len(d.subs); i < 256; i++ {
		if _, err := d.newSubscription(4, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.newSubscription(4, "", ""); err == nil {
		t.Fatal("257th subscription was admitted")
	}
}

func TestSubscriptionOwnershipRemovalIsIdempotent(t *testing.T) {
	d := newEventDaemon()
	v, err := d.newSubscription(0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ref := v.(map[string]any)["subscriptionRef"].(string)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); d.removeSubscription(ref) }()
	}
	wg.Wait()
	d.subMu.Lock()
	n := len(d.subs)
	d.subMu.Unlock()
	if n != 0 {
		t.Fatalf("cleanup leaked subscription: %d", n)
	}
	_ = fmt.Sprint(ref)
}
