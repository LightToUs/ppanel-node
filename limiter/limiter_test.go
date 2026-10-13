package limiter

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/juju/ratelimit"
	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/common/format"
)

func TestUpdateUserRefreshesLimiterStateForUpdatedUser(t *testing.T) {
	Init()
	tag := "node-tag"
	users := []panel.UserInfo{
		{Id: 1, Uuid: "u1", SpeedLimit: 10, DeviceLimit: 1},
	}
	l := AddLimiter(tag, users, map[int]int{1: 2})
	tagUUID := format.UserTag(tag, "u1")
	l.SpeedLimiter.Store(tagUUID, ratelimit.NewBucketWithQuantum(time.Second, 1, 1))
	online := new(sync.Map)
	online.Store("1.1.1.1", 1)
	l.UserOnlineIP.Store(tagUUID, online)

	l.UpdateUser(tag, []panel.UserInfo{
		{Id: 2, Uuid: "u1", SpeedLimit: 20, DeviceLimit: 3},
	}, nil)

	if _, ok := l.SpeedLimiter.Load(tagUUID); ok {
		t.Fatalf("speed limiter for %s should be recreated after update", tagUUID)
	}
	if _, ok := l.UserOnlineIP.Load(tagUUID); ok {
		t.Fatalf("online device cache for %s should be cleared after update", tagUUID)
	}
	if got := l.UUIDtoUID["u1"]; got != 2 {
		t.Fatalf("UUIDtoUID[u1] = %d, want 2", got)
	}
	if _, ok := l.AliveList[1]; ok {
		t.Fatalf("AliveList should remove stale uid 1 entry")
	}
	v, ok := l.UserLimitInfo.Load(tagUUID)
	if !ok {
		t.Fatalf("UserLimitInfo missing for %s", tagUUID)
	}
	limit := v.(*UserLimitInfo)
	if limit.SpeedLimit != 20 || limit.DeviceLimit != 3 || limit.UID != 2 {
		t.Fatalf("updated limit = %#v, want uid=2 speed=20 device=3", limit)
	}
}

func TestPresenceEnforceRejectsExcessDeviceAndCachesDecision(t *testing.T) {
	Init()
	tag := "node-tag"
	l := AddLimiter(tag, []panel.UserInfo{{Id: 42, Uuid: "user"}}, nil)
	var calls int
	l.SetPresenceAuthorizer("enforce", 90*time.Second, func(uid int, ip string) (bool, error) {
		calls++
		return false, nil
	})

	for range 2 {
		_, rejected := l.CheckLimit(format.UserTag(tag, "user"), "203.0.113.8", true, true)
		if !rejected {
			t.Fatal("enforce mode allowed a denied device")
		}
	}
	if calls != 1 {
		t.Fatalf("authorizer calls = %d, want cached single call", calls)
	}
}

func TestPresenceObserveNeverRejectsAndEnforceFailsClosed(t *testing.T) {
	for _, test := range []struct {
		mode       string
		wantReject bool
	}{
		{mode: "observe", wantReject: false},
		{mode: "enforce", wantReject: true},
	} {
		t.Run(test.mode, func(t *testing.T) {
			Init()
			tag := "node-" + test.mode
			l := AddLimiter(tag, []panel.UserInfo{{Id: 42, Uuid: "user"}}, nil)
			l.SetPresenceAuthorizer(test.mode, 90*time.Second, func(int, string) (bool, error) {
				return false, errors.New("control plane unavailable")
			})
			_, rejected := l.CheckLimit(format.UserTag(tag, "user"), "203.0.113.8", true, true)
			if rejected != test.wantReject {
				t.Fatalf("rejected = %v, want %v", rejected, test.wantReject)
			}
		})
	}
}

func TestPresenceRefreshesOnlyWhileConnectionIsTracked(t *testing.T) {
	Init()
	tag := "node-tag"
	tagUUID := format.UserTag(tag, "user")
	l := AddLimiter(tag, []panel.UserInfo{{Id: 42, Uuid: "user"}}, nil)
	var calls int
	l.SetPresenceAuthorizer("enforce", 90*time.Second, func(int, string) (bool, error) {
		calls++
		return true, nil
	})
	if _, rejected := l.CheckLimit(tagUUID, "203.0.113.8", true, true); rejected {
		t.Fatal("initial presence acquisition was rejected")
	}
	release := l.TrackPresence(tagUUID, "203.0.113.8")
	if errs := l.RefreshPresence(); len(errs) != 0 {
		t.Fatalf("refresh errors: %v", errs)
	}
	if calls != 2 {
		t.Fatalf("authorizer calls = %d, want initial acquire and one refresh", calls)
	}
	release()
	if errs := l.RefreshPresence(); len(errs) != 0 {
		t.Fatalf("refresh after release errors: %v", errs)
	}
	if calls != 2 {
		t.Fatalf("released presence was refreshed; calls = %d", calls)
	}
}

func TestRecordAccessDeduplicatesActiveDevice(t *testing.T) {
	l := &Limiter{accessLastSeen: make(map[string]time.Time)}
	var events []panel.AccessEvent
	l.SetAccessRecorder(func(event panel.AccessEvent) error {
		events = append(events, event)
		return nil
	}, 90*time.Second)

	start := time.Unix(1_700_000_000, 0)
	l.recordAccess(42, "203.0.113.8", start)
	l.recordAccess(42, "203.0.113.8", start.Add(30*time.Second))
	l.recordAccess(42, "203.0.113.8", start.Add(121*time.Second))

	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	if events[0].UID != 42 || events[0].ClientIP != "203.0.113.8" {
		t.Fatalf("unexpected first event: %#v", events[0])
	}
	if events[0].EventID == events[1].EventID {
		t.Fatalf("separate access periods must use different event IDs")
	}
}

func TestRecordAccessRemovesExpiredDeviceState(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	l := &Limiter{
		accessLastSeen: map[string]time.Time{
			"1\x00198.51.100.1": start.Add(-2 * time.Minute),
		},
	}
	l.SetAccessRecorder(func(panel.AccessEvent) error { return nil }, 90*time.Second)

	l.recordAccess(42, "203.0.113.8", start)

	if _, exists := l.accessLastSeen["1\x00198.51.100.1"]; exists {
		t.Fatal("expired access device state was not removed")
	}
}

func TestAuthorizePresenceRemovesExpiredCacheEntries(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	l := &Limiter{
		presenceMode: "observe",
		presenceTTL:  90 * time.Second,
		presenceAuthorizer: func(int, string) (bool, error) {
			return true, nil
		},
		presenceCache: map[string]presenceCacheEntry{
			"1\x00198.51.100.1": {
				checkedAt: start.Add(-2 * time.Minute),
				allowed:   true,
			},
		},
	}

	if !l.authorizePresence(42, "203.0.113.8", start) {
		t.Fatal("observe mode unexpectedly rejected presence")
	}
	if _, exists := l.presenceCache["1\x00198.51.100.1"]; exists {
		t.Fatal("expired presence cache entry was not removed")
	}
}

func TestCheckLimitDoesNotAuditPlaceholderUser(t *testing.T) {
	Init()
	tag := "node-tag"
	l := AddLimiter(tag, []panel.UserInfo{{
		Id:          1,
		Uuid:        "placeholder",
		Placeholder: true,
	}}, nil)
	var eventCount int
	l.SetAccessRecorder(func(panel.AccessEvent) error {
		eventCount++
		return nil
	}, 90*time.Second)

	_, rejected := l.CheckLimit(format.UserTag(tag, "placeholder"), "203.0.113.8", true, true)
	if rejected {
		t.Fatal("placeholder connection was unexpectedly rejected")
	}
	if eventCount != 0 {
		t.Fatalf("placeholder generated %d access events", eventCount)
	}
}

func TestCheckLimitTracksUDPUserInOnlineSnapshot(t *testing.T) {
	Init()
	tag := "hysteria2-tag"
	tagUUID := format.UserTag(tag, "hy2-user")
	l := AddLimiter(tag, []panel.UserInfo{{Id: 42, Uuid: "hy2-user"}}, nil)

	if _, rejected := l.CheckLimit(tagUUID, "203.0.113.8", false, false); rejected {
		t.Fatal("authenticated UDP user was rejected")
	}
	snapshot, err := l.GetOnlineDevice()
	if err != nil {
		t.Fatalf("GetOnlineDevice() error = %v", err)
	}
	if len(*snapshot) != 1 || (*snapshot)[0].UID != 42 || (*snapshot)[0].IP != "203.0.113.8" {
		t.Fatalf("snapshot = %#v", *snapshot)
	}
	if _, exists := l.UserOnlineIP.Load(tagUUID); exists {
		t.Fatal("UDP user unexpectedly entered legacy device-limit state")
	}
}

func TestOnlineSnapshotIsNonDestructiveAndExpiresByActivityWindow(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	l := &Limiter{
		onlineLastSeen: make(map[string]onlineDeviceEntry),
		onlineWindow:   2 * time.Minute,
	}
	l.markOnline(42, "203.0.113.8", start)

	first := l.onlineSnapshot(start.Add(time.Minute))
	second := l.onlineSnapshot(start.Add(90 * time.Second))
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("non-destructive snapshots = %#v then %#v", first, second)
	}
	expired := l.onlineSnapshot(start.Add(2*time.Minute + time.Nanosecond))
	if len(expired) != 0 {
		t.Fatalf("expired snapshot = %#v", expired)
	}
}

func TestUpdateUserRemovesDeletedUserFromOnlineSnapshot(t *testing.T) {
	Init()
	tag := "node-tag"
	l := AddLimiter(tag, []panel.UserInfo{{Id: 42, Uuid: "user"}}, nil)
	l.markOnline(42, "203.0.113.8", time.Now())

	l.UpdateUser(tag, nil, []panel.UserInfo{{Id: 42, Uuid: "user"}})

	snapshot, err := l.GetOnlineDevice()
	if err != nil {
		t.Fatalf("GetOnlineDevice() error = %v", err)
	}
	if len(*snapshot) != 0 {
		t.Fatalf("deleted user remains online: %#v", *snapshot)
	}
}
