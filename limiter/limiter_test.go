package limiter

import (
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
