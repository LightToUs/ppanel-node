package core

import (
	"errors"
	"runtime"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lighttous/ppanel-node/common/counter"
	"github.com/lighttous/ppanel-node/core/app/dispatcher"
)

func newTrafficTestCore(tag, email string, uid int) (*XrayCore, *counter.TrafficCounter) {
	trafficCounter := counter.NewTrafficCounter()
	d := &dispatcher.DefaultDispatcher{}
	d.Counter.Store(tag, trafficCounter)

	return &XrayCore{
		users: &UserMap{
			uidMap: map[string]int{email: uid},
		},
		dispatcher: d,
	}, trafficCounter
}

func TestUserTrafficBatchRollbackMergesConcurrentTraffic(t *testing.T) {
	const (
		tag   = "node-tag"
		email = "node-tag|user"
	)
	core, trafficCounter := newTrafficTestCore(tag, email, 42)
	trafficCounter.Tx(email, 100)
	trafficCounter.Rx(email, 200)

	batch, err := core.TakeUserTrafficBatch(tag, 0)
	if err != nil {
		t.Fatalf("TakeUserTrafficBatch() error = %v", err)
	}
	if batch == nil || len(batch.Traffic) != 1 {
		t.Fatalf("batch = %#v, want one traffic record", batch)
	}
	if _, err := uuid.Parse(batch.BatchID); err != nil {
		t.Fatalf("batch ID %q is not a UUID: %v", batch.BatchID, err)
	}
	if got := batch.Traffic[0]; got.UID != 42 || got.Upload != 100 || got.Download != 200 {
		t.Fatalf("traffic = %#v, want uid=42 upload=100 download=200", got)
	}

	trafficCounter.Tx(email, 7)
	trafficCounter.Rx(email, 11)
	batch.Rollback()
	batch.Rollback()

	if got := trafficCounter.GetUpCount(email); got != 107 {
		t.Fatalf("upload after rollback = %d, want 107", got)
	}
	if got := trafficCounter.GetDownCount(email); got != 211 {
		t.Fatalf("download after rollback = %d, want 211", got)
	}
}

func TestUserTrafficBatchCommitKeepsConcurrentTraffic(t *testing.T) {
	const (
		tag   = "node-tag"
		email = "node-tag|user"
	)
	core, trafficCounter := newTrafficTestCore(tag, email, 42)
	trafficCounter.Tx(email, 100)
	trafficCounter.Rx(email, 200)

	batch, err := core.TakeUserTrafficBatch(tag, 0)
	if err != nil {
		t.Fatalf("TakeUserTrafficBatch() error = %v", err)
	}
	trafficCounter.Tx(email, 7)
	trafficCounter.Rx(email, 11)
	batch.Commit()
	batch.Rollback()

	if got := trafficCounter.GetUpCount(email); got != 7 {
		t.Fatalf("upload after commit = %d, want 7", got)
	}
	if got := trafficCounter.GetDownCount(email); got != 11 {
		t.Fatalf("download after commit = %d, want 11", got)
	}
}

func TestUserTrafficBatchPersistsBeforeCountersAreDetached(t *testing.T) {
	const (
		tag   = "node-tag"
		email = "node-tag|user"
	)
	core, trafficCounter := newTrafficTestCore(tag, email, 42)
	trafficCounter.Tx(email, 100)
	trafficCounter.Rx(email, 200)

	batch, err := core.TakeUserTrafficBatchPersisted(tag, 0, func(batch *UserTrafficBatch) error {
		if len(batch.Traffic) != 1 {
			t.Fatalf("persisted traffic = %#v, want one record", batch.Traffic)
		}
		if got := trafficCounter.GetUpCount(email); got != 100 {
			t.Fatalf("upload before persistence = %d, want 100", got)
		}
		if got := trafficCounter.GetDownCount(email); got != 200 {
			t.Fatalf("download before persistence = %d, want 200", got)
		}
		return nil
	})
	if err != nil || batch == nil {
		t.Fatalf("TakeUserTrafficBatchPersisted() = %#v, %v", batch, err)
	}
	if got := trafficCounter.GetUpCount(email); got != 0 {
		t.Fatalf("upload after persistence = %d, want 0", got)
	}
	if got := trafficCounter.GetDownCount(email); got != 0 {
		t.Fatalf("download after persistence = %d, want 0", got)
	}
}

func TestUserTrafficBatchPersistenceFailureKeepsCounters(t *testing.T) {
	const (
		tag   = "node-tag"
		email = "node-tag|user"
	)
	core, trafficCounter := newTrafficTestCore(tag, email, 42)
	trafficCounter.Tx(email, 100)
	trafficCounter.Rx(email, 200)
	persistErr := errors.New("disk unavailable")

	batch, err := core.TakeUserTrafficBatchPersisted(tag, 0, func(*UserTrafficBatch) error {
		return persistErr
	})
	if !errors.Is(err, persistErr) || batch != nil {
		t.Fatalf("TakeUserTrafficBatchPersisted() = %#v, %v; want nil, %v", batch, err, persistErr)
	}
	if got := trafficCounter.GetUpCount(email); got != 100 {
		t.Fatalf("upload after persistence failure = %d, want 100", got)
	}
	if got := trafficCounter.GetDownCount(email); got != 200 {
		t.Fatalf("download after persistence failure = %d, want 200", got)
	}
}

func TestUserTrafficBatchRollbackAfterUserRemovalStaysReportable(t *testing.T) {
	const (
		tag   = "node-tag"
		email = "node-tag|user"
	)
	core, trafficCounter := newTrafficTestCore(tag, email, 42)
	trafficCounter.Tx(email, 100)
	trafficCounter.Rx(email, 200)

	batch, err := core.TakeUserTrafficBatch(tag, 0)
	if err != nil {
		t.Fatalf("TakeUserTrafficBatch() error = %v", err)
	}

	core.users.mapLock.Lock()
	delete(core.users.uidMap, email)
	trafficCounter.Delete(email)
	core.users.mapLock.Unlock()
	batch.Rollback()
	core.dispatcher.Counter.Delete(tag)

	retry, err := core.TakeUserTrafficBatch(tag, 0)
	if err != nil {
		t.Fatalf("retry TakeUserTrafficBatch() error = %v", err)
	}
	if retry == nil || len(retry.Traffic) != 1 {
		t.Fatalf("retry batch = %#v, want one pending traffic record", retry)
	}
	if got := retry.Traffic[0]; got.UID != 42 || got.Upload != 100 || got.Download != 200 {
		t.Fatalf("retry traffic = %#v, want uid=42 upload=100 download=200", got)
	}
	retry.Commit()
}

func TestUserTrafficBatchConcurrentRollbackPreservesTotals(t *testing.T) {
	const (
		tag        = "node-tag"
		email      = "node-tag|user"
		increments = 10_000
	)
	core, trafficCounter := newTrafficTestCore(tag, email, 42)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range increments {
			trafficCounter.Tx(email, 1)
			trafficCounter.Rx(email, 2)
		}
	}()
	go func() {
		defer wg.Done()
		for range increments / 10 {
			batch, err := core.TakeUserTrafficBatch(tag, 0)
			if err != nil {
				t.Errorf("TakeUserTrafficBatch() error = %v", err)
				return
			}
			if batch != nil {
				batch.Rollback()
			}
			runtime.Gosched()
		}
	}()
	wg.Wait()

	if got := trafficCounter.GetUpCount(email); got != increments {
		t.Fatalf("upload total = %d, want %d", got, increments)
	}
	if got := trafficCounter.GetDownCount(email); got != increments*2 {
		t.Fatalf("download total = %d, want %d", got, increments*2)
	}
}
