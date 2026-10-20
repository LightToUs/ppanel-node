package node

import (
	"math"
	"testing"
)

func TestRelayTrafficStorePersistsCumulativeCounters(t *testing.T) {
	store, err := openRelayTrafficStore(t.TempDir(), 57)
	if err != nil {
		t.Fatalf("openRelayTrafficStore() error = %v", err)
	}
	defer store.Close()

	first, err := store.update(1000, 2000)
	if err != nil {
		t.Fatalf("first update error = %v", err)
	}
	if first.Sequence != 1 || first.UploadTotal != 0 || first.DownloadTotal != 0 {
		t.Fatalf("first state = %#v; want baseline only", first)
	}
	second, err := store.update(1600, 2900)
	if err != nil {
		t.Fatalf("second update error = %v", err)
	}
	if second.SessionID != first.SessionID ||
		second.Sequence != 2 ||
		second.UploadTotal != 600 ||
		second.DownloadTotal != 900 {
		t.Fatalf("second state = %#v; want persisted deltas", second)
	}
}

func TestRelayTrafficStoreHandlesInterfaceCounterReset(t *testing.T) {
	store, err := openRelayTrafficStore(t.TempDir(), 57)
	if err != nil {
		t.Fatalf("openRelayTrafficStore() error = %v", err)
	}
	defer store.Close()

	_, _ = store.update(1000, 2000)
	_, _ = store.update(1600, 2900)
	state, err := store.update(50, 80)
	if err != nil {
		t.Fatalf("reset update error = %v", err)
	}
	if state.UploadTotal != 650 || state.DownloadTotal != 980 {
		t.Fatalf("reset state = %#v; want reset counters included once", state)
	}
}

func TestCounterDelta(t *testing.T) {
	if got := counterDelta(100, 150); got != 50 {
		t.Fatalf("counterDelta normal = %d, want 50", got)
	}
	if got := counterDelta(math.MaxUint64-10, 25); got != 25 {
		t.Fatalf("counterDelta reset = %d, want 25", got)
	}
}
