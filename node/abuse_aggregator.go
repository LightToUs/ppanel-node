package node

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/core/app/dispatcher"
)

const (
	abuseEventQueueCapacity = 1024
	abuseAggregateKeyLimit  = 2048
	abuseReportBatchLimit   = 200
	abuseTargetLengthLimit  = 255
	abuseAggregateCountMax  = 1_000_000_000
)

type abuseAggregateKey struct {
	uid        int
	category   string
	bucketUnix int64
}

type abuseAggregateValue struct {
	count        uint64
	firstSeenAt  int64
	lastSeenAt   int64
	sampleTarget string
}

type normalizedAbuseEvent struct {
	key          abuseAggregateKey
	occurredAt   int64
	sampleTarget string
}

type abuseTakeRequest struct {
	limit    int
	response chan []panel.AbuseAggregate
}

type abuseAggregator struct {
	inboundTag string
	protocol   string

	events chan normalizedAbuseEvent
	take   chan abuseTakeRequest
	stop   chan struct{}
	done   chan struct{}

	usersMu sync.RWMutex
	users   map[string]int
	dropped atomic.Uint64
}

func newAbuseAggregator(inboundTag, protocol string, users []panel.UserInfo) *abuseAggregator {
	aggregator := &abuseAggregator{
		inboundTag: inboundTag,
		protocol:   strings.ToLower(strings.TrimSpace(protocol)),
		events:     make(chan normalizedAbuseEvent, abuseEventQueueCapacity),
		take:       make(chan abuseTakeRequest),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	aggregator.UpdateUsers(users)
	go aggregator.run()
	return aggregator
}

func (a *abuseAggregator) UpdateUsers(users []panel.UserInfo) {
	if a == nil {
		return
	}
	next := make(map[string]int, len(users))
	for _, user := range users {
		if user.Id > 0 && user.Uuid != "" {
			next[user.Uuid] = user.Id
		}
	}
	a.usersMu.Lock()
	a.users = next
	a.usersMu.Unlock()
}

func (a *abuseAggregator) Observe(event dispatcher.AbuseEvent) {
	if a == nil || event.InboundTag != a.inboundTag || !validAbuseCategory(event.Category) {
		return
	}
	prefix := a.inboundTag + "|"
	if !strings.HasPrefix(event.UserTag, prefix) {
		return
	}
	userUUID := strings.TrimPrefix(event.UserTag, prefix)
	a.usersMu.RLock()
	uid, exists := a.users[userUUID]
	a.usersMu.RUnlock()
	if !exists {
		return
	}
	occurredAt := event.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	bucketUnix := occurredAt.UTC().Truncate(time.Hour).Unix()
	normalized := normalizedAbuseEvent{
		key: abuseAggregateKey{
			uid:        uid,
			category:   event.Category,
			bucketUnix: bucketUnix,
		},
		occurredAt:   occurredAt.UnixMilli(),
		sampleTarget: truncateUTF8(strings.TrimSpace(event.Target), abuseTargetLengthLimit),
	}
	select {
	case a.events <- normalized:
	default:
		a.dropped.Add(1)
	}
}

func (a *abuseAggregator) Take(limit int) []panel.AbuseAggregate {
	if a == nil || limit <= 0 {
		return nil
	}
	response := make(chan []panel.AbuseAggregate, 1)
	request := abuseTakeRequest{limit: min(limit, abuseReportBatchLimit), response: response}
	select {
	case a.take <- request:
	case <-a.done:
		return nil
	}
	select {
	case aggregates := <-response:
		return aggregates
	case <-a.done:
		return nil
	}
}

func (a *abuseAggregator) Dropped() uint64 {
	if a == nil {
		return 0
	}
	return a.dropped.Swap(0)
}

func (a *abuseAggregator) Close() {
	if a == nil {
		return
	}
	select {
	case <-a.done:
		return
	default:
		close(a.stop)
		<-a.done
	}
}

func (a *abuseAggregator) run() {
	defer close(a.done)
	aggregates := make(map[abuseAggregateKey]abuseAggregateValue)
	for {
		select {
		case event := <-a.events:
			value, exists := aggregates[event.key]
			if !exists && len(aggregates) >= abuseAggregateKeyLimit {
				a.dropped.Add(1)
				continue
			}
			if value.count < abuseAggregateCountMax {
				value.count++
			}
			if value.firstSeenAt == 0 || event.occurredAt < value.firstSeenAt {
				value.firstSeenAt = event.occurredAt
			}
			if event.occurredAt > value.lastSeenAt {
				value.lastSeenAt = event.occurredAt
			}
			if value.sampleTarget == "" {
				value.sampleTarget = event.sampleTarget
			}
			aggregates[event.key] = value
		case request := <-a.take:
			result := make([]panel.AbuseAggregate, 0, min(request.limit, len(aggregates)))
			for key, value := range aggregates {
				result = append(result, panel.AbuseAggregate{
					EventID:      uuid.NewString(),
					UID:          key.uid,
					Category:     key.category,
					Protocol:     a.protocol,
					Count:        value.count,
					BucketStart:  key.bucketUnix * 1000,
					FirstSeenAt:  value.firstSeenAt,
					LastSeenAt:   value.lastSeenAt,
					SampleTarget: value.sampleTarget,
				})
				delete(aggregates, key)
				if len(result) >= request.limit {
					break
				}
			}
			request.response <- result
		case <-a.stop:
			return
		}
	}
}

func validAbuseCategory(category string) bool {
	switch category {
	case "private-network", "smtp", "bittorrent":
		return true
	default:
		return false
	}
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
