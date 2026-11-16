package quota

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lighttous/ppanel-node/api/panel"
	bolt "go.etcd.io/bbolt"
)

type scriptedClient struct {
	requests []panel.QuotaLeaseSyncRequest
	sync     func(panel.QuotaLeaseSyncRequest, int) (*panel.QuotaLeaseSyncDecision, error)
}

func (c *scriptedClient) SyncTrafficQuota(
	_ context.Context,
	request panel.QuotaLeaseSyncRequest,
) (*panel.QuotaLeaseSyncDecision, error) {
	c.requests = append(c.requests, request)
	return c.sync(request, len(c.requests))
}

func newTestManager(
	t *testing.T,
	client SyncClient,
	path string,
) *Manager {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("open BoltDB: %v", err)
	}
	s := &store{db: db}
	if err = s.initialize(); err != nil {
		t.Fatalf("initialize BoltDB: %v", err)
	}
	manager := &Manager{
		mode:       "enforce",
		grantBytes: 8 << 20,
		client:     client,
		store:      s,
		instanceID: "test-node",
		trigger:    make(chan int, 16),
	}
	if err = manager.restore(); err != nil {
		t.Fatalf("restore quota manager: %v", err)
	}
	return manager
}

func grantDecision(
	request panel.QuotaLeaseSyncRequest,
) *panel.QuotaLeaseSyncDecision {
	now := time.Now()
	entry := request.Entries[0]
	return &panel.QuotaLeaseSyncDecision{
		Mode:       "enforce",
		ServerTime: now.UnixMilli(),
		LeaseTTL:   60,
		Grants: []panel.QuotaLeaseGrant{{
			UID:                  entry.UID,
			Required:             true,
			Allowed:              true,
			LeaseID:              "11111111-1111-1111-1111-111111111111",
			Epoch:                1,
			Sequence:             entry.Sequence,
			GrantedRaw:           8 << 20,
			ReportedUploadRaw:    entry.UploadTotal,
			ReportedDownloadRaw:  entry.DownloadTotal,
			BillingFactorPPM:     1_000_000,
			ExpiresAt:            now.Add(time.Minute).UnixMilli(),
			RemainingBilledBytes: 8 << 20,
		}},
	}
}

func TestManagerRetriesPendingBatchThenReleasesLease(t *testing.T) {
	client := &scriptedClient{}
	client.sync = func(
		request panel.QuotaLeaseSyncRequest,
		call int,
	) (*panel.QuotaLeaseSyncDecision, error) {
		if call == 1 {
			return nil, errors.New("response lost")
		}
		if request.Entries[0].Release {
			if request.Entries[0].RequestRaw != 0 {
				t.Fatalf("release request_raw = %d, want 0", request.Entries[0].RequestRaw)
			}
			return &panel.QuotaLeaseSyncDecision{
				Mode:       "enforce",
				ServerTime: time.Now().UnixMilli(),
				Grants: []panel.QuotaLeaseGrant{{
					UID:      request.Entries[0].UID,
					Required: false,
					Allowed:  true,
					Reason:   "lease_released",
				}},
			}, nil
		}
		return grantDecision(request), nil
	}
	manager := newTestManager(t, client, filepath.Join(t.TempDir(), "quota.db"))
	defer manager.store.close()

	if err := manager.UpdateUsers([]panel.UserInfo{{
		Id: 1, QuotaRequired: true, QuotaEpoch: 1,
	}}); err != nil {
		t.Fatalf("UpdateUsers: %v", err)
	}
	if _, allowed := manager.Budget(1, true, 1); allowed {
		t.Fatal("first Budget call succeeded after transport failure")
	}
	pending, err := manager.store.loadPending()
	if err != nil || pending == nil {
		t.Fatalf("pending request = %v, err = %v", pending, err)
	}
	firstBatchID := pending.BatchID

	budget, allowed := manager.Budget(1, true, 1)
	if !allowed || budget == nil {
		t.Fatal("pending batch retry did not install the granted budget")
	}
	if len(client.requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(client.requests))
	}
	if client.requests[0].BatchID != firstBatchID ||
		client.requests[1].BatchID != firstBatchID {
		t.Fatal("pending retry did not reuse the original batch ID")
	}

	if err = manager.UpdateUsers(nil); err != nil {
		t.Fatalf("remove user: %v", err)
	}
	if err = manager.sync(t.Context(), []int{1}, false); err != nil {
		t.Fatalf("release sync: %v", err)
	}
	if len(client.requests) != 3 || !client.requests[2].Entries[0].Release {
		t.Fatal("removed user did not send an explicit lease release")
	}
	if _, exists := manager.budgets.Load(1); exists {
		t.Fatal("released user budget remains in memory")
	}
	states, err := manager.store.loadBudgets()
	if err != nil {
		t.Fatalf("load budgets: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("persisted budgets = %d, want 0", len(states))
	}
}

func TestManagerKeepsPendingRequestOnPartialResponse(t *testing.T) {
	client := &scriptedClient{
		sync: func(
			_ panel.QuotaLeaseSyncRequest,
			_ int,
		) (*panel.QuotaLeaseSyncDecision, error) {
			return &panel.QuotaLeaseSyncDecision{
				Mode:       "enforce",
				ServerTime: time.Now().UnixMilli(),
			}, nil
		},
	}
	manager := newTestManager(t, client, filepath.Join(t.TempDir(), "quota.db"))
	defer manager.store.close()
	if err := manager.UpdateUsers([]panel.UserInfo{{
		Id: 2, QuotaRequired: true, QuotaEpoch: 1,
	}}); err != nil {
		t.Fatalf("UpdateUsers: %v", err)
	}

	if _, allowed := manager.Budget(2, true, 1); allowed {
		t.Fatal("partial response unexpectedly allowed traffic")
	}
	pending, err := manager.store.loadPending()
	if err != nil || pending == nil {
		t.Fatalf("partial response cleared pending request: pending=%v err=%v", pending, err)
	}
}

func TestManagerObserveSyncNeverBlocksTraffic(t *testing.T) {
	now := time.Now()
	client := &scriptedClient{
		sync: func(
			request panel.QuotaLeaseSyncRequest,
			_ int,
		) (*panel.QuotaLeaseSyncDecision, error) {
			if request.Entries[0].RequestRaw != 8<<20 {
				t.Fatalf("observe request_raw = %d, want %d",
					request.Entries[0].RequestRaw, 8<<20)
			}
			return &panel.QuotaLeaseSyncDecision{
				Mode:       "observe",
				ServerTime: now.UnixMilli(),
				LeaseTTL:   90,
				Grants: []panel.QuotaLeaseGrant{{
					UID:       request.Entries[0].UID,
					Required:  true,
					Allowed:   false,
					Epoch:     2,
					ExpiresAt: now.Add(90 * time.Second).UnixMilli(),
					Reason:    "quota_exhausted",
				}},
			}, nil
		},
	}
	manager := newTestManager(t, client, filepath.Join(t.TempDir(), "quota.db"))
	defer manager.store.close()
	manager.mode = "observe"
	manager.renewBefore = 30 * time.Second
	if err := manager.UpdateUsers([]panel.UserInfo{{
		Id: 7, QuotaRequired: true, QuotaEpoch: 2,
	}}); err != nil {
		t.Fatalf("UpdateUsers: %v", err)
	}

	budget, allowed := manager.Budget(7, true, 2)
	if !allowed || budget != nil {
		t.Fatalf("observe Budget() = (%v, %v), want (nil, true)", budget, allowed)
	}
	if err := manager.sync(t.Context(), []int{7}, false); err != nil {
		t.Fatalf("observe sync: %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("observe request count = %d, want 1", len(client.requests))
	}

	budget, allowed = manager.Budget(7, true, 2)
	if !allowed || budget != nil {
		t.Fatalf("denied observe Budget() = (%v, %v), want (nil, true)", budget, allowed)
	}
}

func TestManagerDrainsLeaseAfterModeDowngrade(t *testing.T) {
	for _, mode := range []string{"observe", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			client := &scriptedClient{}
			client.sync = func(
				request panel.QuotaLeaseSyncRequest,
				call int,
			) (*panel.QuotaLeaseSyncDecision, error) {
				if call == 1 {
					return grantDecision(request), nil
				}
				entry := request.Entries[0]
				if !entry.Release || entry.RequestRaw != 0 {
					t.Fatalf("drain entry = %#v, want release without grant", entry)
				}
				return &panel.QuotaLeaseSyncDecision{
					Mode:       mode,
					ServerTime: time.Now().UnixMilli(),
					Grants: []panel.QuotaLeaseGrant{{
						UID:      entry.UID,
						Required: false,
						Allowed:  true,
						Reason:   "lease_released",
					}},
				}, nil
			}
			manager := newTestManager(t, client, filepath.Join(t.TempDir(), "quota.db"))
			defer manager.store.close()
			if err := manager.UpdateUsers([]panel.UserInfo{{
				Id: 8, QuotaRequired: true, QuotaEpoch: 1,
			}}); err != nil {
				t.Fatalf("UpdateUsers: %v", err)
			}
			if _, allowed := manager.Budget(8, true, 1); !allowed {
				t.Fatal("initial enforce grant failed")
			}

			manager.mode = mode
			if err := manager.sync(t.Context(), []int{8}, false); err != nil {
				t.Fatalf("drain sync: %v", err)
			}
			if _, exists := manager.budgets.Load(8); exists {
				t.Fatal("drained lease remained in memory")
			}
			states, err := manager.store.loadBudgets()
			if err != nil {
				t.Fatalf("load budgets: %v", err)
			}
			if len(states) != 0 {
				t.Fatalf("persisted budgets = %d, want 0", len(states))
			}
		})
	}
}

func TestManagerRestoresDurableConsumptionCeiling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.db")
	client := &scriptedClient{
		sync: func(
			request panel.QuotaLeaseSyncRequest,
			_ int,
		) (*panel.QuotaLeaseSyncDecision, error) {
			return grantDecision(request), nil
		},
	}
	manager := newTestManager(t, client, path)
	if err := manager.UpdateUsers([]panel.UserInfo{{
		Id: 3, QuotaRequired: true, QuotaEpoch: 1,
	}}); err != nil {
		t.Fatalf("UpdateUsers: %v", err)
	}
	budget, allowed := manager.Budget(3, true, 1)
	if !allowed || !budget.Consume(true, 1) {
		t.Fatal("failed to consume from granted budget")
	}
	if err := manager.store.close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	restored := newTestManager(t, client, path)
	defer restored.store.close()
	value, ok := restored.budgets.Load(3)
	if !ok {
		t.Fatal("restored budget not found")
	}
	recoveredBudget := value.(*Budget)
	recoveredBudget.mu.Lock()
	uploadRaw := recoveredBudget.uploadRaw
	recoveredBudget.mu.Unlock()
	if uploadRaw != persistBlockBytes {
		t.Fatalf("restored upload = %d, want conservative ceiling %d",
			uploadRaw, persistBlockBytes)
	}
}

func TestManagerCloseIsIdempotent(t *testing.T) {
	client := &scriptedClient{
		sync: func(
			request panel.QuotaLeaseSyncRequest,
			_ int,
		) (*panel.QuotaLeaseSyncDecision, error) {
			return grantDecision(request), nil
		},
	}
	manager, err := Open(t.TempDir(), 1, "vless", "enforce", 90, 8<<20, client)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err = manager.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err = manager.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestManagerPeriodicSyncIsLeaseDriven(t *testing.T) {
	now := time.Now()
	manager := &Manager{
		grantBytes:  8 << 20,
		renewBefore: 30 * time.Second,
	}
	budget := &Budget{
		required:   true,
		allowed:    true,
		grantedRaw: 8 << 20,
		expiresAt:  now.Add(time.Minute),
	}
	if manager.needsPeriodicSyncLocked(budget, now) {
		t.Fatal("user without a lease was scheduled for eager quota allocation")
	}

	budget.leaseID = "lease"
	if manager.needsPeriodicSyncLocked(budget, now) {
		t.Fatal("healthy lease was scheduled before its renewal window")
	}
	budget.expiresAt = now.Add(20 * time.Second)
	if !manager.needsPeriodicSyncLocked(budget, now) {
		t.Fatal("expiring lease was not scheduled")
	}

	budget.expiresAt = now.Add(time.Minute)
	budget.uploadRaw = 7 << 20
	if !manager.needsPeriodicSyncLocked(budget, now) {
		t.Fatal("low-water lease was not scheduled")
	}

	budget.uploadRaw = 0
	budget.required = false
	if !manager.needsPeriodicSyncLocked(budget, now) {
		t.Fatal("removed user lease was not scheduled for release")
	}
}

func TestManagerConcurrentFirstUseRequestsSingleGrant(t *testing.T) {
	client := &scriptedClient{
		sync: func(
			request panel.QuotaLeaseSyncRequest,
			call int,
		) (*panel.QuotaLeaseSyncDecision, error) {
			if call == 1 {
				time.Sleep(50 * time.Millisecond)
			}
			return grantDecision(request), nil
		},
	}
	manager := newTestManager(t, client, filepath.Join(t.TempDir(), "quota.db"))
	defer manager.store.close()
	if err := manager.UpdateUsers([]panel.UserInfo{{
		Id: 4, QuotaRequired: true, QuotaEpoch: 1,
	}}); err != nil {
		t.Fatalf("UpdateUsers: %v", err)
	}

	const connections = 20
	var wg sync.WaitGroup
	errs := make(chan error, connections)
	for range connections {
		wg.Add(1)
		go func() {
			defer wg.Done()
			budget, allowed := manager.Budget(4, true, 1)
			if !allowed || budget == nil {
				errs <- errors.New("concurrent first use was not allowed")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("quota grant requests = %d, want 1", len(client.requests))
	}
}

func TestManagerSyncPendingBatchesAndRetainsFailedBatch(t *testing.T) {
	client := &scriptedClient{}
	client.sync = func(
		request panel.QuotaLeaseSyncRequest,
		call int,
	) (*panel.QuotaLeaseSyncDecision, error) {
		if len(request.Entries) > maxSyncEntries {
			t.Fatalf("request entries = %d, max = %d", len(request.Entries), maxSyncEntries)
		}
		if call == 2 {
			return nil, errors.New("second batch failed")
		}
		grants := make([]panel.QuotaLeaseGrant, 0, len(request.Entries))
		for _, entry := range request.Entries {
			grants = append(grants, panel.QuotaLeaseGrant{
				UID:       entry.UID,
				Required:  true,
				Allowed:   true,
				Epoch:     1,
				Sequence:  entry.Sequence,
				ExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
			})
		}
		return &panel.QuotaLeaseSyncDecision{
			Mode:       "observe",
			ServerTime: time.Now().UnixMilli(),
			LeaseTTL:   60,
			Grants:     grants,
		}, nil
	}
	manager := newTestManager(t, client, filepath.Join(t.TempDir(), "quota.db"))
	defer manager.store.close()
	manager.mode = "observe"

	const users = 1500
	pending := make(map[int]struct{}, users)
	for uid := 1; uid <= users; uid++ {
		manager.budgets.Store(uid, &Budget{
			manager:       manager,
			uid:           uid,
			required:      true,
			expectedEpoch: 1,
		})
		pending[uid] = struct{}{}
	}

	manager.syncPending(pending)
	if len(client.requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(client.requests))
	}
	if len(client.requests[0].Entries) != maxSyncEntries {
		t.Fatalf("first batch entries = %d, want %d",
			len(client.requests[0].Entries), maxSyncEntries)
	}
	if len(client.requests[1].Entries) != users-maxSyncEntries {
		t.Fatalf("second batch entries = %d, want %d",
			len(client.requests[1].Entries), users-maxSyncEntries)
	}
	if len(pending) != users-maxSyncEntries {
		t.Fatalf("pending users = %d, want %d", len(pending), users-maxSyncEntries)
	}
	failedBatchID := client.requests[1].BatchID
	for _, entry := range client.requests[1].Entries {
		if _, ok := pending[entry.UID]; !ok {
			t.Fatalf("failed user %d was removed from pending", entry.UID)
		}
	}

	manager.syncPending(pending)
	if len(client.requests) != 3 {
		t.Fatalf("request count after retry = %d, want 3", len(client.requests))
	}
	if client.requests[2].BatchID != failedBatchID {
		t.Fatal("failed batch retry did not preserve its WAL batch ID")
	}
	if len(pending) != 0 {
		t.Fatalf("pending users after retry = %d, want 0", len(pending))
	}
}

func TestManagerRestoresAndRetriesPendingRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.db")
	failingClient := &scriptedClient{
		sync: func(
			_ panel.QuotaLeaseSyncRequest,
			_ int,
		) (*panel.QuotaLeaseSyncDecision, error) {
			return nil, errors.New("response lost")
		},
	}
	manager := newTestManager(t, failingClient, path)
	if err := manager.UpdateUsers([]panel.UserInfo{{
		Id: 99, QuotaRequired: true, QuotaEpoch: 1,
	}}); err != nil {
		t.Fatalf("UpdateUsers: %v", err)
	}
	if _, allowed := manager.Budget(99, true, 1); allowed {
		t.Fatal("failed request unexpectedly allowed traffic")
	}
	failed := failingClient.requests[0]
	if err := manager.store.close(); err != nil {
		t.Fatalf("close failed manager store: %v", err)
	}

	retryClient := &scriptedClient{
		sync: func(
			request panel.QuotaLeaseSyncRequest,
			_ int,
		) (*panel.QuotaLeaseSyncDecision, error) {
			return grantDecision(request), nil
		},
	}
	restored := newTestManager(t, retryClient, path)
	defer restored.store.close()
	if err := restored.sync(t.Context(), []int{99}, true); err != nil {
		t.Fatalf("retry restored pending request: %v", err)
	}
	if len(retryClient.requests) != 1 {
		t.Fatalf("restored retry requests = %d, want 1", len(retryClient.requests))
	}
	if retryClient.requests[0].BatchID != failed.BatchID {
		t.Fatal("restart retry did not preserve its WAL batch ID")
	}
}

func TestManagerDiscardsOnlyOversizedLegacyPendingRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.db")
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("open BoltDB: %v", err)
	}
	s := &store{db: db}
	if err = s.initialize(); err != nil {
		t.Fatalf("initialize BoltDB: %v", err)
	}
	entries := make([]panel.QuotaLeaseSyncEntry, maxSyncEntries+1)
	states := make([]persistedBudget, maxSyncEntries+1)
	for index := range entries {
		uid := index + 1
		entries[index] = panel.QuotaLeaseSyncEntry{
			UID:         uid,
			Sequence:    1,
			UploadTotal: int64(uid),
		}
		states[index] = persistedBudget{
			UID:                uid,
			Required:           true,
			ExpectedEpoch:      1,
			Sequence:           1,
			UploadRaw:          int64(uid),
			PersistedUploadRaw: int64(uid),
		}
	}
	if err = s.savePending(panel.QuotaLeaseSyncRequest{
		BatchID:      "oversized-legacy-batch",
		NodeInstance: "test-node",
		Entries:      entries,
	}, states); err != nil {
		t.Fatalf("save oversized pending request: %v", err)
	}
	if err = s.close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	manager := newTestManager(t, &scriptedClient{
		sync: func(
			request panel.QuotaLeaseSyncRequest,
			_ int,
		) (*panel.QuotaLeaseSyncDecision, error) {
			return grantDecision(request), nil
		},
	}, path)
	defer manager.store.close()
	pending, err := manager.store.loadPending()
	if err != nil {
		t.Fatalf("load pending request: %v", err)
	}
	if pending != nil {
		t.Fatalf("oversized pending request was not discarded: %#v", pending)
	}
	value, ok := manager.budgets.Load(maxSyncEntries + 1)
	if !ok {
		t.Fatal("persisted budget from oversized request was discarded")
	}
	budget := value.(*Budget)
	budget.mu.Lock()
	uploadRaw := budget.uploadRaw
	sequence := budget.sequence
	budget.mu.Unlock()
	if uploadRaw != maxSyncEntries+1 || sequence != 1 {
		t.Fatalf("restored budget = upload %d sequence %d", uploadRaw, sequence)
	}
}
