package limiter

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/juju/ratelimit"
	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/common/format"
)

var limitLock sync.RWMutex
var limiter map[string]*Limiter

func Init() {
	limiter = map[string]*Limiter{}
}

type Limiter struct {
	SpeedLimit    int
	UserOnlineIP  *sync.Map      // Key: TagUUID, value: {Key: Ip, value: Uid}
	OldUserOnline *sync.Map      // Key: Ip, value: Uid
	UUIDtoUID     map[string]int // Key: UUID, value: Uid
	UserLimitInfo *sync.Map      // Key: TagUUID, value: UserLimitInfo
	SpeedLimiter  *sync.Map      // key: TagUUID, value: *ratelimit.Bucket
	AliveList     map[int]int    // Key: Uid, value: alive_ip

	accessMu         sync.Mutex
	accessLastSeen   map[string]time.Time
	accessIdleWindow time.Duration
	accessRecorder   func(panel.AccessEvent) error
	accessCleanupAt  time.Time

	onlineMu        sync.Mutex
	onlineLastSeen  map[string]onlineDeviceEntry
	onlineWindow    time.Duration
	onlineCleanupAt time.Time

	presenceMu         sync.Mutex
	presenceMode       string
	presenceTTL        time.Duration
	presenceAuthorizer func(uid int, ip string) (bool, error)
	presenceCache      map[string]presenceCacheEntry
	presenceActive     map[string]presenceActiveEntry
	presenceCleanupAt  time.Time
}

type presenceCacheEntry struct {
	checkedAt time.Time
	allowed   bool
}

type presenceActiveEntry struct {
	uid  int
	ip   string
	refs int
}

type onlineDeviceEntry struct {
	uid      int
	ip       string
	lastSeen time.Time
}

type UserLimitInfo struct {
	UID               int
	SpeedLimit        int
	DeviceLimit       int
	DynamicSpeedLimit int
	ExpireTime        int64
	OverLimit         bool
	Placeholder       bool
}

func AddLimiter(tag string, users []panel.UserInfo, aliveList map[int]int) *Limiter {
	info := &Limiter{
		UserOnlineIP:   new(sync.Map),
		UserLimitInfo:  new(sync.Map),
		SpeedLimiter:   new(sync.Map),
		AliveList:      aliveList,
		OldUserOnline:  new(sync.Map),
		accessLastSeen: make(map[string]time.Time),
		onlineLastSeen: make(map[string]onlineDeviceEntry),
		onlineWindow:   2 * time.Minute,
		presenceCache:  make(map[string]presenceCacheEntry),
		presenceActive: make(map[string]presenceActiveEntry),
	}
	uuidmap := make(map[string]int)
	for i := range users {
		uuidmap[users[i].Uuid] = users[i].Id
		userLimit := &UserLimitInfo{}
		userLimit.UID = users[i].Id
		if users[i].SpeedLimit != 0 {
			userLimit.SpeedLimit = users[i].SpeedLimit
		}
		if users[i].DeviceLimit != 0 {
			userLimit.DeviceLimit = users[i].DeviceLimit
		}
		userLimit.OverLimit = false
		userLimit.Placeholder = users[i].Placeholder
		info.UserLimitInfo.Store(format.UserTag(tag, users[i].Uuid), userLimit)
	}
	info.UUIDtoUID = uuidmap
	limitLock.Lock()
	limiter[tag] = info
	limitLock.Unlock()
	return info
}

func GetLimiter(tag string) (info *Limiter, err error) {
	limitLock.RLock()
	info, ok := limiter[tag]
	limitLock.RUnlock()
	if !ok {
		return nil, errors.New("not found")
	}
	return info, nil
}

func DeleteLimiter(tag string) {
	limitLock.Lock()
	delete(limiter, tag)
	limitLock.Unlock()
}

func (l *Limiter) UpdateUser(tag string, upserts []panel.UserInfo, deleted []panel.UserInfo) {
	for i := range deleted {
		l.UserLimitInfo.Delete(format.UserTag(tag, deleted[i].Uuid))
		l.UserOnlineIP.Delete(format.UserTag(tag, deleted[i].Uuid))
		l.SpeedLimiter.Delete(format.UserTag(tag, deleted[i].Uuid))
		delete(l.UUIDtoUID, deleted[i].Uuid)
		delete(l.AliveList, deleted[i].Id)
		l.removeOnlineUID(deleted[i].Id)
	}
	if l.AliveList == nil {
		l.AliveList = make(map[int]int)
	}
	for i := range upserts {
		tagUUID := format.UserTag(tag, upserts[i].Uuid)
		if oldUID, ok := l.UUIDtoUID[upserts[i].Uuid]; ok && oldUID != upserts[i].Id {
			delete(l.AliveList, oldUID)
			l.removeOnlineUID(oldUID)
		}
		l.UserOnlineIP.Delete(tagUUID)
		l.SpeedLimiter.Delete(tagUUID)
		userLimit := &UserLimitInfo{
			UID:         upserts[i].Id,
			Placeholder: upserts[i].Placeholder,
		}
		if upserts[i].SpeedLimit != 0 {
			userLimit.SpeedLimit = upserts[i].SpeedLimit
			userLimit.ExpireTime = 0
		}
		if upserts[i].DeviceLimit != 0 {
			userLimit.DeviceLimit = upserts[i].DeviceLimit
		}
		userLimit.OverLimit = false
		l.UserLimitInfo.Store(tagUUID, userLimit)
		l.UUIDtoUID[upserts[i].Uuid] = upserts[i].Id
	}
}

func (l *Limiter) SetAccessRecorder(recorder func(panel.AccessEvent) error, idleWindow time.Duration) {
	l.accessMu.Lock()
	defer l.accessMu.Unlock()
	l.accessRecorder = recorder
	if idleWindow <= 0 {
		idleWindow = 2 * time.Minute
	}
	l.accessIdleWindow = idleWindow
}

func (l *Limiter) SetOnlineWindow(window time.Duration) {
	if window < time.Minute {
		window = 2 * time.Minute
	}
	l.onlineMu.Lock()
	l.onlineWindow = window
	l.onlineMu.Unlock()
}

func (l *Limiter) SetPresenceAuthorizer(
	mode string,
	leaseTTL time.Duration,
	authorizer func(uid int, ip string) (bool, error),
) {
	l.presenceMu.Lock()
	defer l.presenceMu.Unlock()
	l.presenceMode = strings.ToLower(strings.TrimSpace(mode))
	if leaseTTL < 30*time.Second || leaseTTL > 5*time.Minute {
		leaseTTL = 90 * time.Second
	}
	l.presenceTTL = leaseTTL
	l.presenceAuthorizer = authorizer
}

func (l *Limiter) CheckLimit(taguuid string, ip string, isTcp bool, noSSUDP bool) (Bucket *ratelimit.Bucket, Reject bool) {
	// check if ipv4 mapped ipv6
	ip = strings.TrimPrefix(ip, "::ffff:")

	// check and gen speed limit Bucket
	nodeLimit := l.SpeedLimit
	userLimit := 0
	deviceLimit := 0
	var uid int
	var placeholder bool
	if v, ok := l.UserLimitInfo.Load(taguuid); ok {
		u := v.(*UserLimitInfo)
		deviceLimit = u.DeviceLimit
		uid = u.UID
		placeholder = u.Placeholder
		if u.ExpireTime < time.Now().Unix() && u.ExpireTime != 0 {
			if u.SpeedLimit != 0 {
				userLimit = u.SpeedLimit
				u.DynamicSpeedLimit = 0
				u.ExpireTime = 0
			} else {
				l.UserLimitInfo.Delete(taguuid)
			}
		} else {
			userLimit = determineSpeedLimit(u.SpeedLimit, u.DynamicSpeedLimit)
		}
	} else {
		return nil, true
	}
	now := time.Now()
	if !placeholder && !l.authorizePresence(uid, ip, now) {
		return nil, true
	}
	if noSSUDP {
		// Store online user for device limit
		ipMap := new(sync.Map)
		ipMap.Store(ip, uid)
		aliveIp := l.AliveList[uid]
		// If any device is online
		if v, ok := l.UserOnlineIP.LoadOrStore(taguuid, ipMap); ok {
			ipMap := v.(*sync.Map)
			// If this is a new ip
			if _, ok := ipMap.LoadOrStore(ip, uid); !ok {
				if deviceLimit > 0 {
					if deviceLimit <= aliveIp {
						ipMap.Delete(ip)
						return nil, true
					}
				}
			}
		} else if v, ok := l.OldUserOnline.Load(ip); ok {
			if v.(int) == uid {
				l.OldUserOnline.Delete(ip)
			}
		} else {
			if deviceLimit > 0 {
				if deviceLimit <= aliveIp {
					l.UserOnlineIP.Delete(taguuid)
					return nil, true
				}
			}
		}
	}
	if !placeholder {
		// Online visibility is protocol-independent. In particular, authenticated
		// HY2 sessions arrive over UDP and must not be excluded by noSSUDP.
		l.markOnline(uid, ip, now)
		l.recordAccess(uid, ip, now)
	}

	limit := int64(determineSpeedLimit(nodeLimit, userLimit)) * 1000000 / 8 // If you need the Speed limit
	if limit > 0 {
		Bucket = ratelimit.NewBucketWithQuantum(time.Second, limit, limit) // Byte/s
		if v, ok := l.SpeedLimiter.LoadOrStore(taguuid, Bucket); ok {
			return v.(*ratelimit.Bucket), false
		} else {
			l.SpeedLimiter.Store(taguuid, Bucket)
			return Bucket, false
		}
	} else {
		return nil, false
	}
}

func (l *Limiter) TrackPresence(taguuid string, ip string) func() {
	value, exists := l.UserLimitInfo.Load(taguuid)
	if !exists {
		return func() {}
	}
	info := value.(*UserLimitInfo)
	if info.Placeholder {
		return func() {}
	}
	ip = strings.TrimPrefix(ip, "::ffff:")
	key := fmt.Sprintf("%d\x00%s", info.UID, ip)
	l.presenceMu.Lock()
	if l.presenceMode == "" || l.presenceMode == "disabled" || l.presenceAuthorizer == nil {
		l.presenceMu.Unlock()
		return func() {}
	}
	active := l.presenceActive[key]
	active.uid = info.UID
	active.ip = ip
	active.refs++
	l.presenceActive[key] = active
	l.presenceMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.presenceMu.Lock()
			defer l.presenceMu.Unlock()
			active, exists := l.presenceActive[key]
			if !exists {
				return
			}
			active.refs--
			if active.refs <= 0 {
				delete(l.presenceActive, key)
				return
			}
			l.presenceActive[key] = active
		})
	}
}

func (l *Limiter) RefreshPresence() []error {
	l.presenceMu.Lock()
	authorizer := l.presenceAuthorizer
	active := make([]presenceActiveEntry, 0, len(l.presenceActive))
	for _, entry := range l.presenceActive {
		active = append(active, entry)
	}
	l.presenceMu.Unlock()
	if authorizer == nil {
		return nil
	}

	var errs []error
	now := time.Now()
	for _, entry := range active {
		allowed, err := authorizer(entry.uid, entry.ip)
		if err != nil {
			errs = append(errs, fmt.Errorf("refresh uid %d ip %s: %w", entry.uid, entry.ip, err))
			continue
		}
		key := fmt.Sprintf("%d\x00%s", entry.uid, entry.ip)
		l.presenceMu.Lock()
		l.presenceCache[key] = presenceCacheEntry{checkedAt: now, allowed: allowed}
		l.presenceMu.Unlock()
	}
	return errs
}

func (l *Limiter) authorizePresence(uid int, ip string, now time.Time) bool {
	l.presenceMu.Lock()
	mode := l.presenceMode
	authorizer := l.presenceAuthorizer
	ttl := l.presenceTTL
	if mode == "" || mode == "disabled" || authorizer == nil {
		l.presenceMu.Unlock()
		return true
	}
	if l.presenceCleanupAt.IsZero() || !now.Before(l.presenceCleanupAt) {
		for cacheKey, cached := range l.presenceCache {
			if now.Sub(cached.checkedAt) >= ttl {
				delete(l.presenceCache, cacheKey)
			}
		}
		l.presenceCleanupAt = now.Add(ttl)
	}
	key := fmt.Sprintf("%d\x00%s", uid, ip)
	if cached, exists := l.presenceCache[key]; exists {
		cacheTTL := ttl / 3
		if mode == "enforce" && !cached.allowed {
			cacheTTL = 5 * time.Second
		}
		if now.Sub(cached.checkedAt) < cacheTTL {
			l.presenceMu.Unlock()
			return mode != "enforce" || cached.allowed
		}
	}
	l.presenceMu.Unlock()

	allowed, err := authorizer(uid, ip)
	if err != nil {
		l.presenceMu.Lock()
		l.presenceCache[key] = presenceCacheEntry{checkedAt: now, allowed: false}
		l.presenceMu.Unlock()
		return mode != "enforce"
	}
	l.presenceMu.Lock()
	l.presenceCache[key] = presenceCacheEntry{checkedAt: now, allowed: allowed}
	l.presenceMu.Unlock()
	if mode == "observe" {
		return true
	}
	return allowed
}

func (l *Limiter) recordAccess(uid int, ip string, now time.Time) {
	l.accessMu.Lock()
	recorder := l.accessRecorder
	if recorder == nil {
		l.accessMu.Unlock()
		return
	}
	if l.accessCleanupAt.IsZero() || !now.Before(l.accessCleanupAt) {
		for accessKey, lastSeen := range l.accessLastSeen {
			if now.Sub(lastSeen) > l.accessIdleWindow {
				delete(l.accessLastSeen, accessKey)
			}
		}
		l.accessCleanupAt = now.Add(l.accessIdleWindow)
	}
	key := fmt.Sprintf("%d\x00%s", uid, ip)
	previous, exists := l.accessLastSeen[key]
	l.accessLastSeen[key] = now
	if exists && now.Sub(previous) <= l.accessIdleWindow {
		l.accessMu.Unlock()
		return
	}
	l.accessMu.Unlock()

	event := panel.AccessEvent{
		EventID:     uuid.NewString(),
		UID:         uid,
		ClientIP:    ip,
		ConnectedAt: now.UnixMilli(),
	}
	if err := recorder(event); err == nil {
		return
	}

	l.accessMu.Lock()
	if current, ok := l.accessLastSeen[key]; ok && current.Equal(now) {
		if exists {
			l.accessLastSeen[key] = previous
		} else {
			delete(l.accessLastSeen, key)
		}
	}
	l.accessMu.Unlock()
}

func (l *Limiter) GetOnlineDevice() (*[]panel.OnlineUser, error) {
	// Preserve the legacy interval reset used by local device-limit accounting.
	// The reported snapshot is sourced from onlineLastSeen below and is therefore
	// not destroyed before the server acknowledges it.
	l.UserOnlineIP.Range(func(key, value interface{}) bool {
		taguuid := key.(string)
		ipMap := value.(*sync.Map)
		ipMap.Range(func(key, value interface{}) bool {
			uid := value.(int)
			ip := key.(string)
			l.OldUserOnline.Store(ip, uid)
			return true
		})
		l.UserOnlineIP.Delete(taguuid) // Reset online device
		return true
	})

	onlineUser := l.onlineSnapshot(time.Now())
	return &onlineUser, nil
}

func (l *Limiter) markOnline(uid int, ip string, now time.Time) {
	key := fmt.Sprintf("%d\x00%s", uid, ip)
	l.onlineMu.Lock()
	if l.onlineLastSeen == nil {
		l.onlineLastSeen = make(map[string]onlineDeviceEntry)
	}
	if l.onlineWindow <= 0 {
		l.onlineWindow = 2 * time.Minute
	}
	l.onlineLastSeen[key] = onlineDeviceEntry{
		uid:      uid,
		ip:       ip,
		lastSeen: now,
	}
	l.onlineMu.Unlock()
}

func (l *Limiter) onlineSnapshot(now time.Time) []panel.OnlineUser {
	l.onlineMu.Lock()
	defer l.onlineMu.Unlock()

	window := l.onlineWindow
	if window <= 0 {
		window = 2 * time.Minute
	}
	online := make([]panel.OnlineUser, 0, len(l.onlineLastSeen))
	for key, entry := range l.onlineLastSeen {
		if now.Sub(entry.lastSeen) > window {
			delete(l.onlineLastSeen, key)
			continue
		}
		online = append(online, panel.OnlineUser{UID: entry.uid, IP: entry.ip})
	}
	sort.Slice(online, func(i, j int) bool {
		if online[i].UID == online[j].UID {
			return online[i].IP < online[j].IP
		}
		return online[i].UID < online[j].UID
	})
	return online
}

func (l *Limiter) removeOnlineUID(uid int) {
	l.onlineMu.Lock()
	for key, entry := range l.onlineLastSeen {
		if entry.uid == uid {
			delete(l.onlineLastSeen, key)
		}
	}
	l.onlineMu.Unlock()
}

type UserIpList struct {
	Uid    int      `json:"Uid"`
	IpList []string `json:"Ips"`
}
