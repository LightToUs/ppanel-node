package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/common/counter"
	"github.com/lighttous/ppanel-node/common/format"
	"github.com/lighttous/ppanel-node/core/app/dispatcher"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/proxy/anytls"
	hyaccount "github.com/xtls/xray-core/proxy/hysteria/account"
	"github.com/xtls/xray-core/proxy/shadowsocks"
	"github.com/xtls/xray-core/proxy/shadowsocks_2022"
	"github.com/xtls/xray-core/proxy/trojan"
	"github.com/xtls/xray-core/proxy/tuic"
	"github.com/xtls/xray-core/proxy/vless"
)

type userTrafficBatchEntry struct {
	rollback func()
}

// UserTrafficBatch owns traffic removed from live counters until the report is
// either acknowledged or rolled back.
type UserTrafficBatch struct {
	BatchID string
	Traffic []panel.UserTraffic
	entries []userTrafficBatchEntry
	once    sync.Once
}

func RestoreUserTrafficBatch(batchID string, traffic []panel.UserTraffic) *UserTrafficBatch {
	return &UserTrafficBatch{
		BatchID: batchID,
		Traffic: append([]panel.UserTraffic(nil), traffic...),
	}
}

func (b *UserTrafficBatch) Commit() {
	if b == nil {
		return
	}
	b.once.Do(func() {})
}

func (b *UserTrafficBatch) Rollback() {
	if b == nil {
		return
	}
	b.once.Do(func() {
		for _, entry := range b.entries {
			entry.rollback()
		}
	})
}

func (v *XrayCore) GetUserManager(tag string) (proxy.UserManager, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	handler, err := v.ihm.GetHandler(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("no such inbound tag: %s", err)
	}
	inboundInstance, ok := handler.(proxy.GetInbound)
	if !ok {
		return nil, fmt.Errorf("handler %s is not implement proxy.GetInbound", tag)
	}
	userManager, ok := inboundInstance.GetInbound().(proxy.UserManager)
	if !ok {
		return nil, fmt.Errorf("handler %s is not implement proxy.UserManager", tag)
	}
	return userManager, nil
}

func (vc *XrayCore) DelUsers(users []panel.UserInfo, tag string, _ *panel.NodeInfo) error {
	userManager, err := vc.GetUserManager(tag)
	if err != nil {
		return fmt.Errorf("get user manager error: %s", err)
	}
	var user string
	vc.users.mapLock.Lock()
	defer vc.users.mapLock.Unlock()
	for i := range users {
		user = format.UserTag(tag, users[i].Uuid)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = userManager.RemoveUser(ctx, user)
		cancel()
		if err != nil {
			return err
		}
		uid := vc.users.uidMap[user]
		if v, ok := vc.dispatcher.LinkManagers.Load(user); ok {
			lm := v.(*dispatcher.LinkManager)
			lm.CloseAll()
			vc.dispatcher.LinkManagers.Delete(user)
		}
		if v, ok := vc.dispatcher.Counter.Load(tag); ok {
			tc := v.(*counter.TrafficCounter)
			if value, exists := tc.Counters.Load(user); exists {
				traffic := value.(*counter.TrafficStorage)
				vc.addPendingTraffic(
					tag,
					uid,
					traffic.UpCounter.Swap(0),
					traffic.DownCounter.Swap(0),
				)
			}
			tc.Delete(user)
		}
		delete(vc.users.uidMap, user)
	}
	return nil
}

func (vc *XrayCore) TakeUserTrafficBatch(tag string, mintraffic int) (*UserTrafficBatch, error) {
	return vc.TakeUserTrafficBatchPersisted(tag, mintraffic, nil)
}

func (vc *XrayCore) TakeUserTrafficBatchPersisted(
	tag string,
	mintraffic int,
	persist func(*UserTrafficBatch) error,
) (*UserTrafficBatch, error) {
	vc.trafficTakeMu.Lock()
	defer vc.trafficTakeMu.Unlock()

	batch := &UserTrafficBatch{
		BatchID: uuid.NewString(),
		Traffic: make([]panel.UserTraffic, 0),
		entries: make([]userTrafficBatchEntry, 0),
	}
	finalize := make([]func(), 0)
	vc.users.mapLock.RLock()
	defer vc.users.mapLock.RUnlock()
	vc.pendingTrafficMu.Lock()
	defer vc.pendingTrafficMu.Unlock()
	for key, traffic := range vc.pendingTraffic {
		if key.tag != tag {
			continue
		}
		key := key
		traffic := traffic
		batch.Traffic = append(batch.Traffic, traffic)
		batch.entries = append(batch.entries, userTrafficBatchEntry{
			rollback: func() {
				vc.addPendingTraffic(tag, traffic.UID, traffic.Upload, traffic.Download)
			},
		})
		finalize = append(finalize, func() {
			delete(vc.pendingTraffic, key)
		})
	}
	if v, ok := vc.dispatcher.Counter.Load(tag); ok {
		c := v.(*counter.TrafficCounter)
		c.Counters.Range(func(key, value interface{}) bool {
			email := key.(string)
			traffic := value.(*counter.TrafficStorage)
			up := traffic.UpCounter.Load()
			down := traffic.DownCounter.Load()
			if up+down > int64(mintraffic) {
				if vc.users.uidMap[email] == 0 {
					return true
				}
				if up+down == 0 {
					return true
				}
				uid := vc.users.uidMap[email]
				batch.Traffic = append(batch.Traffic, panel.UserTraffic{
					UID:      uid,
					Upload:   up,
					Download: down,
				})
				batch.entries = append(batch.entries, userTrafficBatchEntry{
					rollback: func() {
						vc.rollbackActiveTraffic(tag, email, uid, c, traffic, up, down)
					},
				})
				finalize = append(finalize, func() {
					traffic.UpCounter.Add(-up)
					traffic.DownCounter.Add(-down)
				})
			}
			return true
		})
	}
	if len(batch.Traffic) == 0 {
		return nil, nil
	}
	if persist != nil {
		if err := persist(batch); err != nil {
			return nil, err
		}
	}
	for _, apply := range finalize {
		apply()
	}
	return batch, nil
}

func (vc *XrayCore) addPendingTraffic(tag string, uid int, upload, download int64) {
	if uid == 0 || upload+download == 0 {
		return
	}
	key := pendingTrafficKey{tag: tag, uid: uid}
	vc.pendingTrafficMu.Lock()
	defer vc.pendingTrafficMu.Unlock()
	if vc.pendingTraffic == nil {
		vc.pendingTraffic = make(map[pendingTrafficKey]panel.UserTraffic)
	}
	traffic := vc.pendingTraffic[key]
	traffic.UID = uid
	traffic.Upload += upload
	traffic.Download += download
	vc.pendingTraffic[key] = traffic
}

func (vc *XrayCore) rollbackActiveTraffic(
	tag, email string,
	uid int,
	owner *counter.TrafficCounter,
	traffic *counter.TrafficStorage,
	upload, download int64,
) {
	vc.users.mapLock.RLock()
	defer vc.users.mapLock.RUnlock()
	if current, ok := owner.Counters.Load(email); ok && current == traffic {
		traffic.UpCounter.Add(upload)
		traffic.DownCounter.Add(download)
		return
	}
	vc.addPendingTraffic(tag, uid, upload, download)
}

// GetUserTrafficSlice is retained for callers that do not support report
// acknowledgement. New reporting code should use TakeUserTrafficBatch.
func (vc *XrayCore) GetUserTrafficSlice(tag string, mintraffic int) ([]panel.UserTraffic, error) {
	batch, err := vc.TakeUserTrafficBatch(tag, mintraffic)
	if batch == nil || err != nil {
		return nil, err
	}
	batch.Commit()
	return batch.Traffic, nil
}

func (v *XrayCore) AddUsers(p *AddUsersParams) (added int, err error) {
	v.users.mapLock.Lock()
	defer v.users.mapLock.Unlock()
	for i := range p.Users {
		v.users.uidMap[format.UserTag(p.Tag, p.Users[i].Uuid)] = p.Users[i].Id
	}
	var users []*protocol.User
	switch p.NodeInfo.Type {
	case "vmess":
		users = buildVmessUsers(p.Tag, p.Users)
	case "vless":
		users = buildVlessUsers(p.Tag, p.Users, p.Protocol.Flow)
	case "trojan":
		users = buildTrojanUsers(p.Tag, p.Users)
	case "shadowsocks":
		users = buildSSUsers(p.Tag,
			p.Users,
			p.Protocol.Cipher,
			p.Protocol.ServerKey)
	case "hysteria2", "hy2":
		users = buildHysteria2Users(p.Tag, p.Users)
	case "tuic":
		users = buildTuicUsers(p.Tag, p.Users)
	case "anytls":
		users = buildAnyTLSUsers(p.Tag, p.Users)
	default:
		return 0, fmt.Errorf("unsupported node type: %s", p.NodeInfo.Type)
	}
	man, err := v.GetUserManager(p.Tag)
	if err != nil {
		return 0, fmt.Errorf("get user manager error: %s", err)
	}
	for _, u := range users {
		mUser, err := u.ToMemoryUser()
		if err != nil {
			return 0, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = man.AddUser(ctx, mUser)
		cancel()
		if err != nil {
			return 0, err
		}
	}
	return len(users), nil
}

func buildVmessUsers(tag string, userInfo []panel.UserInfo) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i, user := range userInfo {
		users[i] = buildVmessUser(tag, &user)
	}
	return users
}

func buildVmessUser(tag string, userInfo *panel.UserInfo) (user *protocol.User) {
	vmessAccount := &conf.VMessAccount{
		ID:       userInfo.Uuid,
		Security: "auto",
	}
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(vmessAccount.Build()),
	}
}

func buildVlessUsers(tag string, userInfo []panel.UserInfo, flow string) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		users[i] = buildVlessUser(tag, &(userInfo)[i], flow)
	}
	return users
}

func buildVlessUser(tag string, userInfo *panel.UserInfo, flow string) (user *protocol.User) {
	vlessAccount := &vless.Account{
		Id: userInfo.Uuid,
	}
	vlessAccount.Flow = flow
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(vlessAccount),
	}
}

func buildTrojanUsers(tag string, userInfo []panel.UserInfo) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		users[i] = buildTrojanUser(tag, &(userInfo)[i])
	}
	return users
}

func buildTrojanUser(tag string, userInfo *panel.UserInfo) (user *protocol.User) {
	trojanAccount := &trojan.Account{
		Password: userInfo.Uuid,
	}
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(trojanAccount),
	}
}

func buildSSUsers(tag string, userInfo []panel.UserInfo, cypher string, serverKey string) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		users[i] = buildSSUser(tag, &userInfo[i], cypher, serverKey)
	}
	return users
}

func buildSSUser(tag string, userInfo *panel.UserInfo, cypher string, serverKey string) (user *protocol.User) {
	if !strings.Contains(cypher, "2022") {
		ssAccount := &shadowsocks.Account{
			Password:   userInfo.Uuid,
			CipherType: getCipherFromString(cypher),
		}
		return &protocol.User{
			Level:   0,
			Email:   format.UserTag(tag, userInfo.Uuid),
			Account: serial.ToTypedMessage(ssAccount),
		}
	} else {
		var keyLength int
		switch cypher {
		case "2022-blake3-aes-128-gcm":
			keyLength = 16
		case "2022-blake3-aes-256-gcm":
			keyLength = 32
		case "2022-blake3-chacha20-poly1305":
			keyLength = 32
		}
		ssAccount := &shadowsocks_2022.Account{
			Key: base64.StdEncoding.EncodeToString([]byte(userInfo.Uuid[:keyLength])),
		}
		return &protocol.User{
			Level:   0,
			Email:   format.UserTag(tag, userInfo.Uuid),
			Account: serial.ToTypedMessage(ssAccount),
		}
	}
}

func getCipherFromString(c string) shadowsocks.CipherType {
	switch strings.ToLower(c) {
	case "aes-128-gcm", "aead_aes_128_gcm":
		return shadowsocks.CipherType_AES_128_GCM
	case "aes-256-gcm", "aead_aes_256_gcm":
		return shadowsocks.CipherType_AES_256_GCM
	case "chacha20-poly1305", "aead_chacha20_poly1305", "chacha20-ietf-poly1305":
		return shadowsocks.CipherType_CHACHA20_POLY1305
	default:
		return shadowsocks.CipherType_UNKNOWN
	}
}

func buildHysteria2Users(tag string, userInfo []panel.UserInfo) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		users[i] = buildHysteria2User(tag, &userInfo[i])
	}
	return users
}

func buildHysteria2User(tag string, userInfo *panel.UserInfo) (user *protocol.User) {
	hysteria2Account := &hyaccount.Account{
		Auth: userInfo.Uuid,
	}
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(hysteria2Account),
	}
}

func buildTuicUsers(tag string, userInfo []panel.UserInfo) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		users[i] = buildTuicUser(tag, &userInfo[i])
	}
	return users
}

func buildTuicUser(tag string, userInfo *panel.UserInfo) (user *protocol.User) {
	tuicAccount := &tuic.Account{
		Uuid:     userInfo.Uuid,
		Password: userInfo.Uuid,
	}
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(tuicAccount),
	}
}

func buildAnyTLSUsers(tag string, userInfo []panel.UserInfo) (users []*protocol.User) {
	users = make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		users[i] = buildAnyTLSUser(tag, &userInfo[i])
	}
	return users
}

func buildAnyTLSUser(tag string, userInfo *panel.UserInfo) (user *protocol.User) {
	anyTLSAccount := &anytls.Account{
		Password: userInfo.Uuid,
	}
	return &protocol.User{
		Level:   0,
		Email:   format.UserTag(tag, userInfo.Uuid),
		Account: serial.ToTypedMessage(anyTLSAccount),
	}
}
