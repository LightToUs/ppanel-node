package dispatcher

import (
	"sync"
	"sync/atomic"
	"time"
)

type AbuseEvent struct {
	Category   string
	InboundTag string
	UserTag    string
	Protocol   string
	Target     string
	OccurredAt time.Time
}

var (
	abuseObserverID atomic.Uint64
	abuseObserverMu sync.RWMutex
	abuseObservers  = make(map[uint64]func(AbuseEvent))
)

func SetAbuseObserver(observer func(AbuseEvent)) func() {
	if observer == nil {
		return func() {}
	}
	id := abuseObserverID.Add(1)
	abuseObserverMu.Lock()
	abuseObservers[id] = observer
	abuseObserverMu.Unlock()
	return func() {
		abuseObserverMu.Lock()
		delete(abuseObservers, id)
		abuseObserverMu.Unlock()
	}
}

func notifyAbuseEvent(event AbuseEvent) {
	if event.UserTag == "" || event.InboundTag == "" || event.Category == "" {
		return
	}
	abuseObserverMu.RLock()
	for _, observer := range abuseObservers {
		observer(event)
	}
	abuseObserverMu.RUnlock()
}
