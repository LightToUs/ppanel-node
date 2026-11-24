package node

import (
	"strings"
	"testing"
	"time"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/core/app/dispatcher"
)

func TestAbuseAggregatorAggregatesByHour(t *testing.T) {
	aggregator := newAbuseAggregator("node-tag", "Trojan", []panel.UserInfo{{
		Id: 42, Uuid: "user-uuid",
	}})
	defer aggregator.Close()

	first := time.Date(2026, 11, 24, 10, 5, 0, 0, time.UTC)
	second := first.Add(10 * time.Minute)
	for _, occurredAt := range []time.Time{first, second} {
		aggregator.Observe(dispatcher.AbuseEvent{
			Category:   "smtp",
			InboundTag: "node-tag",
			UserTag:    "node-tag|user-uuid",
			Target:     "tcp:mail.example.com:25",
			OccurredAt: occurredAt,
		})
	}

	aggregates := takeAbuseCountEventually(t, aggregator, 2)
	var total uint64
	firstSeenAt := second.UnixMilli()
	var lastSeenAt int64
	for _, got := range aggregates {
		if got.UID != 42 || got.Category != "smtp" || got.Protocol != "trojan" {
			t.Fatalf("unexpected aggregate: %+v", got)
		}
		if got.BucketStart != first.Truncate(time.Hour).UnixMilli() {
			t.Fatalf("unexpected bucket start: %+v", got)
		}
		total += got.Count
		firstSeenAt = min(firstSeenAt, got.FirstSeenAt)
		lastSeenAt = max(lastSeenAt, got.LastSeenAt)
	}
	if total != 2 || firstSeenAt != first.UnixMilli() || lastSeenAt != second.UnixMilli() {
		t.Fatalf(
			"aggregate total/times = %d/%d/%d, want 2/%d/%d",
			total,
			firstSeenAt,
			lastSeenAt,
			first.UnixMilli(),
			second.UnixMilli(),
		)
	}
}

func TestAbuseAggregatorRejectsUnknownIdentityAndCategory(t *testing.T) {
	aggregator := newAbuseAggregator("node-tag", "vless", []panel.UserInfo{{
		Id: 1, Uuid: "known",
	}})
	defer aggregator.Close()

	for _, event := range []dispatcher.AbuseEvent{
		{Category: "dns_failure", InboundTag: "node-tag", UserTag: "node-tag|known"},
		{Category: "smtp", InboundTag: "other-tag", UserTag: "node-tag|known"},
		{Category: "smtp", InboundTag: "node-tag", UserTag: "node-tag|unknown"},
	} {
		aggregator.Observe(event)
	}
	if got := aggregator.Take(10); len(got) != 0 {
		t.Fatalf("unexpected aggregates: %+v", got)
	}
}

func TestAbuseAggregatorQueueFullDropsWithoutBlocking(t *testing.T) {
	aggregator := &abuseAggregator{
		inboundTag: "node-tag",
		events:     make(chan normalizedAbuseEvent, 1),
		users:      map[string]int{"known": 1},
	}
	event := dispatcher.AbuseEvent{
		Category:   "smtp",
		InboundTag: "node-tag",
		UserTag:    "node-tag|known",
		OccurredAt: time.Now(),
	}
	aggregator.Observe(event)
	aggregator.Observe(event)
	if got := aggregator.Dropped(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
}

func TestAbuseAggregatorLimitsBatchAndTarget(t *testing.T) {
	users := make([]panel.UserInfo, abuseReportBatchLimit+10)
	aggregator := newAbuseAggregator("node-tag", "vless", users)
	defer aggregator.Close()
	for index := range users {
		users[index] = panel.UserInfo{Id: index + 1, Uuid: strings.Repeat("u", index%5+1) + "-" + string(rune(index+1000))}
	}
	aggregator.UpdateUsers(users)
	target := strings.Repeat("界", 200)
	for _, user := range users {
		aggregator.Observe(dispatcher.AbuseEvent{
			Category:   "private-network",
			InboundTag: "node-tag",
			UserTag:    "node-tag|" + user.Uuid,
			Target:     target,
			OccurredAt: time.Now(),
		})
	}
	aggregates := takeAbuseAggregatesEventually(t, aggregator, abuseReportBatchLimit)
	if len(aggregates) != abuseReportBatchLimit {
		t.Fatalf("batch count = %d, want %d", len(aggregates), abuseReportBatchLimit)
	}
	for _, aggregate := range aggregates {
		if len(aggregate.SampleTarget) > abuseTargetLengthLimit {
			t.Fatalf("sample target length = %d, want <= %d", len(aggregate.SampleTarget), abuseTargetLengthLimit)
		}
	}
}

func takeAbuseAggregatesEventually(
	t *testing.T,
	aggregator *abuseAggregator,
	want int,
) []panel.AbuseAggregate {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var result []panel.AbuseAggregate
	for len(result) < want && time.Now().Before(deadline) {
		result = append(result, aggregator.Take(want-len(result))...)
		if len(result) < want {
			time.Sleep(time.Millisecond)
		}
	}
	return result
}

func takeAbuseCountEventually(
	t *testing.T,
	aggregator *abuseAggregator,
	want uint64,
) []panel.AbuseAggregate {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var result []panel.AbuseAggregate
	var total uint64
	for total < want && time.Now().Before(deadline) {
		batch := aggregator.Take(abuseReportBatchLimit)
		result = append(result, batch...)
		for _, aggregate := range batch {
			total += aggregate.Count
		}
		if total < want {
			time.Sleep(time.Millisecond)
		}
	}
	return result
}
