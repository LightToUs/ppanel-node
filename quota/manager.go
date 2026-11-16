package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lighttous/ppanel-node/api/panel"
	log "github.com/sirupsen/logrus"
	bolt "go.etcd.io/bbolt"
)

const (
	defaultGrantBytes = int64(64 << 20)
	persistBlockBytes = int64(1 << 20)
	syncInterval      = 5 * time.Second
	maxSyncEntries    = 1000

	metaBucket    = "meta"
	budgetBucket  = "budgets"
	pendingBucket = "pending"
	instanceKey   = "node_instance"
	pendingKey    = "request"
)

var unsafeFileChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type SyncClient interface {
	SyncTrafficQuota(
		ctx context.Context,
		request panel.QuotaLeaseSyncRequest,
	) (*panel.QuotaLeaseSyncDecision, error)
}

type persistedBudget struct {
	UID                int    `json:"uid"`
	Required           bool   `json:"required"`
	ExpectedEpoch      int64  `json:"expected_epoch"`
	LeaseID            string `json:"lease_id"`
	Epoch              int64  `json:"epoch"`
	Sequence           int64  `json:"sequence"`
	GrantedRaw         int64  `json:"granted_raw"`
	UploadRaw          int64  `json:"upload_raw"`
	DownloadRaw        int64  `json:"download_raw"`
	PersistedUploadRaw int64  `json:"persisted_upload_raw"`
	PersistedDownRaw   int64  `json:"persisted_download_raw"`
	FactorPPM          int64  `json:"factor_ppm"`
	ExpiresAt          int64  `json:"expires_at"`
	Allowed            bool   `json:"allowed"`
}

type Budget struct {
	mu sync.Mutex

	manager            *Manager
	uid                int
	required           bool
	expectedEpoch      int64
	leaseID            string
	epoch              int64
	sequence           int64
	grantedRaw         int64
	uploadRaw          int64
	downloadRaw        int64
	persistedUploadRaw int64
	persistedDownRaw   int64
	factorPPM          int64
	expiresAt          time.Time
	allowed            bool
}

type Manager struct {
	mode        string
	grantBytes  int64
	renewBefore time.Duration
	client      SyncClient
	store       *store
	instanceID  string

	budgets  sync.Map
	syncMu   sync.Mutex
	trigger  chan int
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
	closeErr error
}

type store struct {
	db *bolt.DB
}

func Open(
	statePath string,
	serverID int,
	protocol string,
	mode string,
	leaseTTL int64,
	grantBytes int64,
	client SyncClient,
) (*Manager, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "observe", "enforce":
	default:
		mode = "disabled"
	}
	if statePath == "" {
		statePath = "/var/lib/ppanel-node"
	}
	dir := filepath.Join(statePath, "quota-leases")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create quota state directory: %w", err)
	}
	name := fmt.Sprintf("%d-%s.db", serverID, unsafeFileChars.ReplaceAllString(protocol, "_"))
	db, err := bolt.Open(filepath.Join(dir, name), 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open quota state: %w", err)
	}
	s := &store{db: db}
	if err = s.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	instanceID, err := s.instanceID()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if grantBytes <= 0 {
		grantBytes = defaultGrantBytes
	}
	if leaseTTL < 30 || leaseTTL > 300 {
		leaseTTL = 90
	}
	manager := &Manager{
		mode:        mode,
		grantBytes:  grantBytes,
		renewBefore: time.Duration(leaseTTL) * time.Second / 3,
		client:      client,
		store:       s,
		instanceID:  instanceID,
		trigger:     make(chan int, 1024),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	if err = manager.restore(); err != nil {
		_ = db.Close()
		return nil, err
	}
	go manager.run()
	return manager, nil
}

func (m *Manager) Mode() string {
	if m == nil {
		return "disabled"
	}
	return m.mode
}

func (m *Manager) UpdateUsers(users []panel.UserInfo) error {
	if m == nil {
		return nil
	}
	if m.mode == "disabled" {
		var result error
		m.budgets.Range(func(key, value any) bool {
			budget := value.(*Budget)
			budget.mu.Lock()
			if budget.leaseID == "" {
				budget.mu.Unlock()
				return true
			}
			budget.required = false
			budget.mu.Unlock()
			if err := m.persistBudget(budget); err != nil {
				result = errors.Join(result, err)
			}
			m.signal(key.(int))
			return true
		})
		return result
	}
	var result error
	active := make(map[int]panel.UserInfo, len(users))
	for _, user := range users {
		active[user.Id] = user
		value, _ := m.budgets.LoadOrStore(user.Id, &Budget{manager: m, uid: user.Id})
		budget := value.(*Budget)
		budget.mu.Lock()
		budget.required = user.QuotaRequired
		budget.expectedEpoch = user.QuotaEpoch
		budget.mu.Unlock()
		if err := m.persistBudget(budget); err != nil {
			result = errors.Join(result, err)
		}
	}
	m.budgets.Range(func(key, value any) bool {
		uid := key.(int)
		if _, ok := active[uid]; ok {
			return true
		}
		budget := value.(*Budget)
		budget.mu.Lock()
		budget.required = false
		needsRelease := budget.leaseID != ""
		budget.mu.Unlock()
		if needsRelease {
			m.signal(uid)
		}
		return true
	})
	return result
}

func (m *Manager) Budget(uid int, required bool, epoch int64) (*Budget, bool) {
	if m == nil || m.mode == "disabled" || !required {
		return nil, true
	}
	value, _ := m.budgets.LoadOrStore(uid, &Budget{
		manager: m, uid: uid, required: true, expectedEpoch: epoch,
	})
	budget := value.(*Budget)
	budget.mu.Lock()
	budget.required = true
	budget.expectedEpoch = epoch
	if m.mode == "observe" {
		if !budget.expiresAt.After(time.Now().Add(m.renewBefore)) {
			m.signal(uid)
		}
		budget.mu.Unlock()
		return nil, true
	}
	allowed := budget.usableLocked(time.Now())
	budget.mu.Unlock()
	if allowed {
		return budget, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := m.sync(ctx, []int{uid}, true)
	cancel()
	if err != nil {
		return budget, false
	}
	budget.mu.Lock()
	allowed = budget.usableLocked(time.Now())
	budget.mu.Unlock()
	return budget, allowed
}

func (b *Budget) Consume(upload bool, count int64) bool {
	if b == nil || count <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.usableLocked(time.Now()) {
		return false
	}
	if count > b.remainingLocked() {
		b.manager.signal(b.uid)
		return false
	}
	if !b.reserveDurablyLocked(upload, count) {
		return false
	}
	if upload {
		b.uploadRaw += count
	} else {
		b.downloadRaw += count
	}
	remaining := b.remainingLocked()
	threshold := max(b.manager.grantBytes/4, persistBlockBytes)
	if remaining <= threshold {
		b.manager.signal(b.uid)
	}
	return true
}

func (b *Budget) reserveDurablyLocked(upload bool, count int64) bool {
	neededUpload := b.uploadRaw
	neededDownload := b.downloadRaw
	if upload {
		neededUpload += count
	} else {
		neededDownload += count
	}
	if neededUpload <= b.persistedUploadRaw && neededDownload <= b.persistedDownRaw {
		return true
	}

	// Reclaim unused local durability headroom before reserving the next block.
	b.persistedUploadRaw = b.uploadRaw
	b.persistedDownRaw = b.downloadRaw
	available := b.grantedRaw - b.persistedUploadRaw - b.persistedDownRaw
	if available < count {
		return false
	}
	reserve := max(count, persistBlockBytes)
	if reserve > available {
		reserve = available
	}
	if upload {
		b.persistedUploadRaw += reserve
	} else {
		b.persistedDownRaw += reserve
	}
	if err := b.manager.persistBudgetLocked(b); err != nil {
		if upload {
			b.persistedUploadRaw -= reserve
		} else {
			b.persistedDownRaw -= reserve
		}
		return false
	}
	return true
}

func (b *Budget) usableLocked(now time.Time) bool {
	return b.required &&
		b.allowed &&
		b.leaseID != "" &&
		b.epoch == b.expectedEpoch &&
		now.Before(b.expiresAt) &&
		b.remainingLocked() > 0
}

func (b *Budget) remainingLocked() int64 {
	used := b.uploadRaw + b.downloadRaw
	if used >= b.grantedRaw {
		return 0
	}
	return b.grantedRaw - used
}

func (m *Manager) signal(uid int) {
	select {
	case m.trigger <- uid:
	default:
	}
}

func (m *Manager) run() {
	defer close(m.done)
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()
	pending := make(map[int]struct{})
	for {
		select {
		case uid := <-m.trigger:
			pending[uid] = struct{}{}
			if len(pending) >= 100 {
				m.syncPending(pending)
			}
		case <-ticker.C:
			now := time.Now()
			m.budgets.Range(func(key, value any) bool {
				budget := value.(*Budget)
				budget.mu.Lock()
				needsSync := m.needsPeriodicSyncLocked(budget, now)
				budget.mu.Unlock()
				if needsSync {
					pending[key.(int)] = struct{}{}
				}
				return len(pending) < 1000
			})
			m.syncPending(pending)
		case <-m.stop:
			m.syncPending(pending)
			return
		}
	}
}

func (m *Manager) needsPeriodicSyncLocked(budget *Budget, now time.Time) bool {
	if budget == nil {
		return false
	}
	if m.mode == "observe" {
		if budget.leaseID != "" {
			return true
		}
		return budget.required && budget.expiresAt.Before(now.Add(m.renewBefore))
	}
	if m.mode == "disabled" {
		return budget.leaseID != ""
	}
	if budget.leaseID == "" {
		return false
	}
	return !budget.required ||
		!budget.allowed ||
		budget.remainingLocked() <= max(m.grantBytes/4, persistBlockBytes) ||
		budget.expiresAt.Before(now.Add(m.renewBefore))
}

func (m *Manager) syncPending(pending map[int]struct{}) {
	for len(pending) > 0 {
		ids := make([]int, 0, min(len(pending), maxSyncEntries))
		for uid := range pending {
			ids = append(ids, uid)
			if len(ids) == maxSyncEntries {
				break
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		synced, err := m.syncBatch(ctx, ids, false)
		cancel()
		if err != nil {
			return
		}
		for _, uid := range synced {
			delete(pending, uid)
		}
	}
}

func (m *Manager) sync(ctx context.Context, uids []int, forceGrant bool) error {
	pending := make(map[int]struct{}, len(uids))
	for _, uid := range uids {
		pending[uid] = struct{}{}
	}
	for len(pending) > 0 {
		batch := make([]int, 0, min(len(pending), maxSyncEntries))
		for uid := range pending {
			batch = append(batch, uid)
			if len(batch) == maxSyncEntries {
				break
			}
		}
		synced, err := m.syncBatch(ctx, batch, forceGrant)
		if err != nil {
			return err
		}
		for _, uid := range synced {
			delete(pending, uid)
		}
	}
	return nil
}

func (m *Manager) syncBatch(
	ctx context.Context,
	uids []int,
	forceGrant bool,
) ([]int, error) {
	if m == nil || m.client == nil {
		return uids, nil
	}
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	if pending, err := m.store.loadPending(); err != nil {
		return nil, err
	} else if pending != nil {
		if err = m.sendAndApply(ctx, *pending); err != nil {
			return nil, err
		}
		return requestUIDs(*pending), nil
	}

	entries := make([]panel.QuotaLeaseSyncEntry, 0, len(uids))
	states := make([]persistedBudget, 0, len(uids))
	processed := make([]int, 0, len(uids))
	seen := make(map[int]struct{}, len(uids))
	now := time.Now()
	for _, uid := range uids {
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		processed = append(processed, uid)
		value, ok := m.budgets.Load(uid)
		if !ok {
			continue
		}
		budget := value.(*Budget)
		budget.mu.Lock()
		if forceGrant && budget.usableLocked(now) {
			budget.mu.Unlock()
			continue
		}
		if !budget.required && budget.leaseID == "" {
			budget.mu.Unlock()
			continue
		}
		if m.mode == "disabled" && budget.leaseID == "" {
			budget.mu.Unlock()
			continue
		}
		if budget.leaseID != "" {
			budget.sequence++
		}
		budget.persistedUploadRaw = budget.uploadRaw
		budget.persistedDownRaw = budget.downloadRaw
		requestRaw := int64(0)
		release := !budget.required ||
			(budget.leaseID != "" && m.mode != "enforce")
		if !release && budget.required && (m.mode == "observe" ||
			(forceGrant ||
				budget.remainingLocked() <= max(m.grantBytes/4, persistBlockBytes))) {
			requestRaw = m.grantBytes
		}
		entry := panel.QuotaLeaseSyncEntry{
			UID:           uid,
			LeaseID:       budget.leaseID,
			Epoch:         budget.epoch,
			Sequence:      budget.sequence,
			UploadTotal:   budget.uploadRaw,
			DownloadTotal: budget.downloadRaw,
			RequestRaw:    requestRaw,
			Release:       release,
		}
		states = append(states, budget.persisted())
		budget.mu.Unlock()
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return processed, nil
	}
	request := panel.QuotaLeaseSyncRequest{
		BatchID:      uuid.NewString(),
		NodeInstance: m.instanceID,
		Entries:      entries,
	}
	if err := m.store.savePending(request, states); err != nil {
		return nil, err
	}
	if err := m.sendAndApply(ctx, request); err != nil {
		return nil, err
	}
	return processed, nil
}

func requestUIDs(request panel.QuotaLeaseSyncRequest) []int {
	uids := make([]int, 0, len(request.Entries))
	for _, entry := range request.Entries {
		uids = append(uids, entry.UID)
	}
	return uids
}

func (m *Manager) sendAndApply(ctx context.Context, request panel.QuotaLeaseSyncRequest) error {
	decision, err := m.client.SyncTrafficQuota(ctx, request)
	if err != nil {
		return err
	}
	if decision == nil {
		return errors.New("traffic quota sync returned an empty decision")
	}
	expected := make(map[int]struct{}, len(request.Entries))
	for _, entry := range request.Entries {
		expected[entry.UID] = struct{}{}
	}
	if len(decision.Grants) != len(expected) {
		return fmt.Errorf(
			"traffic quota sync returned %d grants for %d users",
			len(decision.Grants),
			len(expected),
		)
	}
	seen := make(map[int]struct{}, len(decision.Grants))
	for _, grant := range decision.Grants {
		if _, ok := expected[grant.UID]; !ok {
			return fmt.Errorf("traffic quota sync returned unexpected user %d", grant.UID)
		}
		if _, duplicate := seen[grant.UID]; duplicate {
			return fmt.Errorf("traffic quota sync returned duplicate user %d", grant.UID)
		}
		seen[grant.UID] = struct{}{}
	}
	if decision.Mode == "observe" {
		denied := 0
		reasons := make(map[string]int)
		for _, grant := range decision.Grants {
			if grant.Allowed {
				continue
			}
			denied++
			reasons[grant.Reason]++
		}
		log.WithFields(log.Fields{
			"users":   len(decision.Grants),
			"denied":  denied,
			"reasons": reasons,
		}).Info("Traffic quota observe decision")
	}
	now := time.Now()
	for _, grant := range decision.Grants {
		value, _ := m.budgets.LoadOrStore(grant.UID, &Budget{manager: m, uid: grant.UID})
		budget := value.(*Budget)
		budget.mu.Lock()
		if grant.LeaseID != budget.leaseID {
			budget.uploadRaw = grant.ReportedUploadRaw
			budget.downloadRaw = grant.ReportedDownloadRaw
			budget.persistedUploadRaw = grant.ReportedUploadRaw
			budget.persistedDownRaw = grant.ReportedDownloadRaw
		}
		budget.required = grant.Required
		budget.allowed = grant.Allowed
		budget.leaseID = grant.LeaseID
		budget.epoch = grant.Epoch
		budget.sequence = grant.Sequence
		budget.grantedRaw = grant.GrantedRaw
		budget.factorPPM = grant.BillingFactorPPM
		if budget.expectedEpoch == 0 {
			budget.expectedEpoch = grant.Epoch
		}
		if grant.ExpiresAt > 0 {
			remaining := time.Duration(grant.ExpiresAt-decision.ServerTime) * time.Millisecond
			if remaining < 0 {
				remaining = 0
			}
			budget.expiresAt = now.Add(remaining)
		} else {
			budget.expiresAt = time.Time{}
		}
		budget.mu.Unlock()
	}
	states := make([]persistedBudget, 0, len(decision.Grants))
	released := make([]int, 0)
	for _, grant := range decision.Grants {
		if value, ok := m.budgets.Load(grant.UID); ok {
			budget := value.(*Budget)
			budget.mu.Lock()
			if !budget.required && budget.leaseID == "" {
				released = append(released, grant.UID)
			} else {
				states = append(states, budget.persisted())
			}
			budget.mu.Unlock()
		}
	}
	if err = m.store.applyResponse(states, released); err != nil {
		return err
	}
	for _, uid := range released {
		m.budgets.Delete(uid)
	}
	return nil
}

func (m *Manager) persistBudget(budget *Budget) error {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return m.persistBudgetLocked(budget)
}

func (m *Manager) persistBudgetLocked(budget *Budget) error {
	return m.store.saveBudget(budget.persisted())
}

func (b *Budget) persisted() persistedBudget {
	return persistedBudget{
		UID:                b.uid,
		Required:           b.required,
		ExpectedEpoch:      b.expectedEpoch,
		LeaseID:            b.leaseID,
		Epoch:              b.epoch,
		Sequence:           b.sequence,
		GrantedRaw:         b.grantedRaw,
		UploadRaw:          b.uploadRaw,
		DownloadRaw:        b.downloadRaw,
		PersistedUploadRaw: b.persistedUploadRaw,
		PersistedDownRaw:   b.persistedDownRaw,
		FactorPPM:          b.factorPPM,
		ExpiresAt:          b.expiresAt.UnixMilli(),
		Allowed:            b.allowed,
	}
}

func (m *Manager) restore() error {
	budgets, err := m.store.loadBudgets()
	if err != nil {
		return err
	}
	for _, state := range budgets {
		// Durable ceilings are conservative after an unclean shutdown.
		state.UploadRaw = max(state.UploadRaw, state.PersistedUploadRaw)
		state.DownloadRaw = max(state.DownloadRaw, state.PersistedDownRaw)
		budget := &Budget{
			manager:            m,
			uid:                state.UID,
			required:           state.Required,
			expectedEpoch:      state.ExpectedEpoch,
			leaseID:            state.LeaseID,
			epoch:              state.Epoch,
			sequence:           state.Sequence,
			grantedRaw:         state.GrantedRaw,
			uploadRaw:          state.UploadRaw,
			downloadRaw:        state.DownloadRaw,
			persistedUploadRaw: state.PersistedUploadRaw,
			persistedDownRaw:   state.PersistedDownRaw,
			factorPPM:          state.FactorPPM,
			expiresAt:          time.UnixMilli(state.ExpiresAt),
			allowed:            state.Allowed,
		}
		m.budgets.Store(state.UID, budget)
	}
	pending, err := m.store.loadPending()
	if err != nil {
		return err
	}
	if pending != nil && len(pending.Entries) > maxSyncEntries {
		// The server rejects oversized requests before opening the quota
		// transaction, so this legacy WAL record cannot have been applied.
		if err = m.store.clearPending(); err != nil {
			return err
		}
		log.WithFields(log.Fields{
			"batch_id": pending.BatchID,
			"entries":  len(pending.Entries),
		}).Warn("Discarded oversized legacy traffic quota WAL request")
	}
	return nil
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.once.Do(func() {
		close(m.stop)
		<-m.done
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ids := make([]int, 0)
		m.budgets.Range(func(key, _ any) bool {
			ids = append(ids, key.(int))
			return true
		})
		syncErr := m.sync(ctx, ids, false)
		closeErr := m.store.close()
		m.closeErr = errors.Join(syncErr, closeErr)
	})
	return m.closeErr
}

func (s *store) initialize() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{metaBucket, budgetBucket, pendingBucket} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *store) instanceID() (string, error) {
	var result string
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(metaBucket))
		if value := bucket.Get([]byte(instanceKey)); value != nil {
			result = string(value)
			return nil
		}
		result = uuid.NewString()
		return bucket.Put([]byte(instanceKey), []byte(result))
	})
	return result, err
}

func (s *store) saveBudget(state persistedBudget) error {
	value, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(budgetBucket)).Put([]byte(fmt.Sprint(state.UID)), value)
	})
}

func (s *store) loadBudgets() ([]persistedBudget, error) {
	var result []persistedBudget
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(budgetBucket)).ForEach(func(_, value []byte) error {
			var state persistedBudget
			if err := json.Unmarshal(value, &state); err != nil {
				return err
			}
			result = append(result, state)
			return nil
		})
	})
	return result, err
}

func (s *store) savePending(
	request panel.QuotaLeaseSyncRequest,
	states []persistedBudget,
) error {
	value, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		budgetStore := tx.Bucket([]byte(budgetBucket))
		for _, state := range states {
			encoded, encodeErr := json.Marshal(state)
			if encodeErr != nil {
				return encodeErr
			}
			if putErr := budgetStore.Put([]byte(fmt.Sprint(state.UID)), encoded); putErr != nil {
				return putErr
			}
		}
		return tx.Bucket([]byte(pendingBucket)).Put([]byte(pendingKey), value)
	})
}

func (s *store) loadPending() (*panel.QuotaLeaseSyncRequest, error) {
	var request panel.QuotaLeaseSyncRequest
	err := s.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket([]byte(pendingBucket)).Get([]byte(pendingKey))
		if value == nil {
			return nil
		}
		return json.Unmarshal(value, &request)
	})
	if err != nil || request.BatchID == "" {
		return nil, err
	}
	return &request, nil
}

func (s *store) clearPending() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(pendingBucket)).Delete([]byte(pendingKey))
	})
}

func (s *store) applyResponse(states []persistedBudget, released []int) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		budgetStore := tx.Bucket([]byte(budgetBucket))
		for _, state := range states {
			encoded, err := json.Marshal(state)
			if err != nil {
				return err
			}
			if err = budgetStore.Put([]byte(fmt.Sprint(state.UID)), encoded); err != nil {
				return err
			}
		}
		for _, uid := range released {
			if err := budgetStore.Delete([]byte(fmt.Sprint(uid))); err != nil {
				return err
			}
		}
		return tx.Bucket([]byte(pendingBucket)).Delete([]byte(pendingKey))
	})
}

func (s *store) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}
