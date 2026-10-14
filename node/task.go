package node

import (
	"context"
	"slices"
	"time"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/common/serverstatus"
	"github.com/lighttous/ppanel-node/common/task"
	vCore "github.com/lighttous/ppanel-node/core"
	log "github.com/sirupsen/logrus"
)

func (c *Controller) startTasks(node *panel.NodeInfo) {
	// fetch user list task
	c.userListMonitorPeriodic = &task.Task{
		Name:     "userListMonitor",
		Interval: time.Duration(node.PullInterval) * time.Second,
		Execute:  c.userListMonitor,
		ReloadCh: c.server.ReloadCh,
	}
	// report user traffic task
	c.userReportPeriodic = &task.Task{
		Name:     "reportUserTraffic",
		Interval: time.Duration(node.PushInterval) * time.Second,
		Execute:  c.reportUserTrafficTask,
		ReloadCh: c.server.ReloadCh,
	}
	_ = c.userListMonitorPeriodic.Start(false)
	log.WithField("节点", c.tag).Info("用户列表监控任务已启动")
	_ = c.userReportPeriodic.Start(false)
	log.WithField("节点", c.tag).Info("用户流量报告任务已启动")
	if normalizedPresenceMode(node.PresenceMode) != "disabled" {
		c.presenceRenewPeriodic = &task.Task{
			Name:     "presenceRenew",
			Interval: presenceLeaseTTL(node.PresenceLeaseTTL) / 3,
			Execute:  c.refreshPresenceTask,
			ReloadCh: c.server.ReloadCh,
		}
		_ = c.presenceRenewPeriodic.Start(false)
		log.WithField("节点", c.tag).Info("在线租约续期任务已启动")
	}
	var security string
	switch node.Type {
	case "vless":
		security = node.Protocol.Security
	case "vmess":
		security = node.Protocol.Security
	case "trojan":
		security = node.Protocol.Security
	case "shadowsocks":
		security = ""
	case "tuic":
		security = "tls"
	case "hysteria2", "hy2":
		security = "tls"
	default:
		security = ""
	}

	if security == "tls" {
		switch node.Protocol.CertMode {
		case "none", "", "file", "self":
		default:
			c.renewCertPeriodic = &task.Task{
				Name:     "renewCert",
				Interval: time.Hour * 24,
				Execute:  c.renewCertTask,
				ReloadCh: c.server.ReloadCh,
			}
			log.WithField("节点", c.tag).Info("证书定期更新任务已启动")
			// delay to start renewCert
			_ = c.renewCertPeriodic.Start(true)
		}
	}
}

func (c *Controller) reloadTask() {
	c.userListMonitorPeriodic.Close()
	c.userReportPeriodic.Close()
	if c.renewCertPeriodic != nil {
		c.renewCertPeriodic.Close()
	}
	if c.presenceRenewPeriodic != nil {
		c.presenceRenewPeriodic.Close()
		c.presenceRenewPeriodic = nil
	}
	c.startTasks(c.info)
}

func (c *Controller) refreshPresenceTask(_ context.Context) error {
	if c.limiter == nil {
		return nil
	}
	for _, err := range c.limiter.RefreshPresence() {
		log.WithFields(log.Fields{"tag": c.tag, "err": err}).
			Warn("Refresh presence lease failed")
	}
	return nil
}

func (c *Controller) userListMonitor(ctx context.Context) (err error) {
	// get user info
	newU, err := c.apiClient.GetUserList(ctx)
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get user list failed")
		return nil
	}
	// get user alive
	newA, err := c.apiClient.GetUserAlive()
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get alive list failed")
		return nil
	}
	// update alive list
	if newA != nil {
		c.limiter.UpdateAliveList(newA)
	}
	// update user list
	// newU == nil indicates 304 Not Modified; empty slice means the list is empty
	if newU == nil {
		return nil
	}
	deleted, added, updated := compareUserList(c.userList, newU)
	if err = c.applyUserChanges(deleted, added, updated); err != nil {
		c.apiClient.ResetUserETag()
		log.WithFields(log.Fields{"tag": c.tag, "err": err}).
			Error("Apply user changes failed; runtime changes rolled back")
		return nil
	}
	if len(added) > 0 || len(deleted) > 0 || len(updated) > 0 {
		// update Limiter
		c.limiter.UpdateUser(c.tag, append(added, updated...), deleted)
		if err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Error("limiter users failed")
			return nil
		}
	}
	c.userList = newU
	if len(added)+len(deleted)+len(updated) != 0 {
		log.WithField("节点", c.tag).
			Infof("删除 %d 个用户，新增 %d 个用户，更新 %d 个用户", len(deleted), len(added), len(updated))
	}
	return nil
}

func (c *Controller) applyUserChanges(
	deleted, added, updated []panel.UserInfo,
) error {
	rollbacks := make([]func() error, 0, len(deleted)+len(added)+len(updated)*2)
	rollback := func(cause error) error {
		for i := len(rollbacks) - 1; i >= 0; i-- {
			if err := rollbacks[i](); err != nil {
				log.WithFields(log.Fields{"tag": c.tag, "err": err}).
					Error("Rollback user change failed")
			}
		}
		return cause
	}
	addOne := func(user panel.UserInfo) error {
		_, err := c.server.AddUsers(&vCore.AddUsersParams{
			Tag: c.tag, NodeInfo: c.info, Users: []panel.UserInfo{user},
		})
		return err
	}
	deleteOne := func(user panel.UserInfo) error {
		return c.server.DelUsers([]panel.UserInfo{user}, c.tag, c.info)
	}
	for _, user := range deleted {
		if err := deleteOne(user); err != nil {
			return rollback(err)
		}
		current := user
		rollbacks = append(rollbacks, func() error { return addOne(current) })
	}
	for _, user := range added {
		if err := addOne(user); err != nil {
			return rollback(err)
		}
		current := user
		rollbacks = append(rollbacks, func() error { return deleteOne(current) })
	}
	if c.info.Type == "wireguard" {
		oldByUUID := make(map[string]panel.UserInfo, len(c.userList))
		for _, user := range c.userList {
			oldByUUID[user.Uuid] = user
		}
		for _, user := range updated {
			oldUser := oldByUUID[user.Uuid]
			if err := deleteOne(oldUser); err != nil {
				return rollback(err)
			}
			oldCopy := oldUser
			rollbacks = append(rollbacks, func() error { return addOne(oldCopy) })
			if err := addOne(user); err != nil {
				return rollback(err)
			}
			newCopy := user
			rollbacks = append(rollbacks, func() error { return deleteOne(newCopy) })
		}
	}
	return nil
}

func (c *Controller) reportUserTrafficTask(ctx context.Context) (err error) {
	// Access events use their own durable queue and are acknowledged only after a
	// successful server response, so a traffic report failure cannot drop them.
	c.reportAccessEvents(ctx)

	var reportmin = 0
	if c.info.TrafficReportThreshold > 0 {
		reportmin = c.info.TrafficReportThreshold
	}
	c.reportUserTraffic(ctx, reportmin)

	if onlineDevice, err := c.limiter.GetOnlineDevice(); err != nil {
		log.Print(err)
	} else {
		if err = c.apiClient.ReportNodeOnlineSnapshot(ctx, onlineDevice); err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Info("Report online snapshot failed")
		} else {
			log.WithField("节点", c.tag).Infof("已上报 %d 名在线用户快照", len(*onlineDevice))
		}
	}

	CPU, Mem, Disk, Uptime, err := serverstatus.GetSystemInfo()
	if err != nil {
		log.Print(err)
	}
	err = c.apiClient.ReportNodeStatus(
		&panel.NodeStatus{
			CPU:    CPU,
			Mem:    Mem,
			Disk:   Disk,
			Uptime: Uptime,
		})
	if err != nil {
		log.Print(err)
	}

	return nil
}

func (c *Controller) reportUserTraffic(ctx context.Context, reportmin int) {
	c.trafficReportMu.Lock()
	defer c.trafficReportMu.Unlock()

	trafficBatch := c.pendingTrafficBatch
	if trafficBatch == nil {
		var trafficErr error
		trafficBatch, trafficErr = c.server.TakeUserTrafficBatchPersisted(
			c.tag,
			reportmin,
			c.trafficBatches.Put,
		)
		if trafficErr != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": trafficErr,
			}).Info("Collect user traffic failed")
		}
	}
	if trafficBatch != nil {
		reportErr := c.apiClient.ReportUserTraffic(ctx, trafficBatch.BatchID, &trafficBatch.Traffic)
		if reportErr != nil {
			// Keep this exact batch and ID for retry. A lost HTTP response may
			// mean the server already processed it, so rebuilding a new batch ID
			// would allow duplicate accounting.
			c.pendingTrafficBatch = trafficBatch
			log.WithFields(log.Fields{
				"tag":      c.tag,
				"batch_id": trafficBatch.BatchID,
				"err":      reportErr,
			}).Info("Report user traffic failed")
		} else {
			if deleteErr := c.trafficBatches.Delete(trafficBatch.BatchID); deleteErr != nil {
				c.pendingTrafficBatch = trafficBatch
				log.WithFields(log.Fields{
					"tag":      c.tag,
					"batch_id": trafficBatch.BatchID,
					"err":      deleteErr,
				}).Error("Acknowledge traffic batch failed; batch retained for retry")
				return
			}
			trafficBatch.Commit()
			c.pendingTrafficBatch = nil
			log.WithFields(log.Fields{
				"节点":       c.tag,
				"batch_id": trafficBatch.BatchID,
			}).Infof("已上报 %d 名用户消耗流量", len(trafficBatch.Traffic))
		}
	}
}

func compareUserList(old, new []panel.UserInfo) (deleted, added, updated []panel.UserInfo) {
	oldMap := make(map[string]panel.UserInfo, len(old))
	for _, user := range old {
		oldMap[user.Uuid] = user
	}
	seen := make(map[string]struct{}, len(new))
	for _, user := range new {
		oldUser, exists := oldMap[user.Uuid]
		if !exists {
			added = append(added, user)
			continue
		}
		seen[user.Uuid] = struct{}{}
		if !sameUserState(oldUser, user) {
			updated = append(updated, user)
		}
	}
	for _, user := range old {
		if _, ok := seen[user.Uuid]; !ok {
			deleted = append(deleted, user)
		}
	}
	return deleted, added, updated
}

func sameUserState(oldUser, newUser panel.UserInfo) bool {
	return oldUser.Id == newUser.Id &&
		oldUser.SpeedLimit == newUser.SpeedLimit &&
		oldUser.DeviceLimit == newUser.DeviceLimit &&
		oldUser.Placeholder == newUser.Placeholder &&
		oldUser.PeerId == newUser.PeerId &&
		oldUser.PublicKey == newUser.PublicKey &&
		slices.Equal(oldUser.AllowedIPs, newUser.AllowedIPs) &&
		oldUser.PreSharedKey == newUser.PreSharedKey &&
		oldUser.PersistentKeepalive == newUser.PersistentKeepalive
}
