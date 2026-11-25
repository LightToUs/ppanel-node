package node

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/common/task"
	vCore "github.com/lighttous/ppanel-node/core"
	"github.com/lighttous/ppanel-node/core/app/dispatcher"
	"github.com/lighttous/ppanel-node/limiter"
	nodequota "github.com/lighttous/ppanel-node/quota"
	log "github.com/sirupsen/logrus"
)

type Controller struct {
	server                  *vCore.XrayCore
	apiClient               *panel.ClientV1
	tag                     string
	started                 bool
	prepared                bool
	limiter                 *limiter.Limiter
	userList                []panel.UserInfo
	aliveMap                map[int]int
	info                    *panel.NodeInfo
	userListMonitorPeriodic *task.Task
	userReportPeriodic      *task.Task
	renewCertPeriodic       *task.Task
	onlineIpReportPeriodic  *task.Task
	presenceRenewPeriodic   *task.Task
	abuseReportPeriodic     *task.Task
	portHopping             *portHoppingManager
	accessEvents            *accessEventStore
	abuseEvents             *abuseAggregator
	unregisterAbuseObserver func()
	pendingAbuseAggregates  []panel.AbuseAggregate
	abuseDroppedSinceLog    uint64
	abuseReportFailures     uint64
	lastAbuseDroppedLogAt   time.Time
	lastAbuseFailureLogAt   time.Time
	trafficBatches          *trafficBatchStore
	quotaManager            *nodequota.Manager
	statePath               string
	userMutationMu          sync.Mutex
	controlRevisions        map[int64]int64
	controlStates           map[int64]string
	refreshControlTracking  func()
	trafficReportMu         sync.Mutex
	pendingTrafficBatch     *vCore.UserTrafficBatch
}

// NewController return a Node controller with default parameters.
func NewController(core *vCore.XrayCore, api *panel.ClientV1, info *panel.NodeInfo, statePath ...string) *Controller {
	controller := &Controller{
		server:           core,
		apiClient:        api,
		info:             info,
		controlRevisions: make(map[int64]int64),
		controlStates:    make(map[int64]string),
	}
	if len(statePath) > 0 {
		controller.statePath = statePath[0]
	}
	return controller
}

// Prepare resolves every control-plane dependency before the runtime releases
// its existing listeners. Start can then complete without another API request.
func (c *Controller) Prepare() error {
	if c == nil || c.prepared {
		return nil
	}
	userList, err := c.apiClient.GetUserList(context.Background())
	if err != nil {
		return fmt.Errorf("get user list error: %s", err)
	}
	if len(userList) == 0 && c.info.Type != "wireguard" {
		return errors.New("add users error: not have any user")
	}
	aliveMap, err := c.apiClient.GetUserAlive()
	if err != nil {
		return fmt.Errorf("failed to get user alive list: %s", err)
	}
	if c.info.Protocol.Security == "tls" {
		if err = c.requestCert(); err != nil {
			return fmt.Errorf("request cert error: %s", err)
		}
	}
	c.userList = userList
	c.aliveMap = aliveMap
	c.prepared = true
	return nil
}

// Start implement the Start() function of the service interface
func (c *Controller) Start() error {
	if c == nil || c.started {
		return nil
	}
	var err error
	var nodeAdded bool
	var limiterAdded bool
	var portHoppingAdded bool
	var accessStoreOpened bool
	var trafficStoreOpened bool
	var quotaManagerOpened bool
	var abuseObserverRegistered bool
	defer func() {
		if err == nil {
			return
		}
		if abuseObserverRegistered && c.unregisterAbuseObserver != nil {
			c.unregisterAbuseObserver()
			c.unregisterAbuseObserver = nil
		}
		if c.abuseEvents != nil {
			c.abuseEvents.Close()
			c.abuseEvents = nil
		}
		if portHoppingAdded && c.portHopping != nil {
			if closeErr := c.portHopping.Close(); closeErr != nil {
				log.WithFields(log.Fields{
					"tag": c.tag,
					"err": closeErr,
				}).Error("Start rollback: remove port hopping rules failed")
			}
			c.portHopping = nil
		}
		if nodeAdded && c.server != nil && c.tag != "" {
			if closeErr := c.server.DelNode(c.tag); closeErr != nil {
				log.WithFields(log.Fields{
					"tag": c.tag,
					"err": closeErr,
				}).Error("Start rollback: remove node failed")
			}
		}
		if limiterAdded && c.tag != "" {
			limiter.DeleteLimiter(c.tag)
		}
		if accessStoreOpened && c.accessEvents != nil {
			if closeErr := c.accessEvents.Close(); closeErr != nil {
				log.WithFields(log.Fields{"tag": c.tag, "err": closeErr}).
					Error("Start rollback: close access event store failed")
			}
			c.accessEvents = nil
		}
		if trafficStoreOpened && c.trafficBatches != nil {
			if closeErr := c.trafficBatches.Close(); closeErr != nil {
				log.WithFields(log.Fields{"tag": c.tag, "err": closeErr}).
					Error("Start rollback: close traffic batch store failed")
			}
			c.trafficBatches = nil
		}
		if quotaManagerOpened && c.quotaManager != nil {
			if closeErr := c.quotaManager.Close(); closeErr != nil {
				log.WithFields(log.Fields{"tag": c.tag, "err": closeErr}).
					Error("Start rollback: close quota manager failed")
			}
			c.quotaManager = nil
		}
		c.started = false
	}()

	if err = c.Prepare(); err != nil {
		return err
	}
	if err = c.loadControlState(); err != nil {
		return fmt.Errorf("load subscription control state: %w", err)
	}
	c.tag = c.buildNodeTag(c.info)
	c.abuseEvents = newAbuseAggregator(c.tag, c.info.Type, c.userList)
	c.unregisterAbuseObserver = dispatcher.SetAbuseObserver(c.abuseEvents.Observe)
	abuseObserverRegistered = true
	c.trafficBatches, err = openTrafficBatchStore(c.statePath, c.info.Id, c.info.Type)
	if err != nil {
		return fmt.Errorf("open traffic batch store: %w", err)
	}
	trafficStoreOpened = true
	c.pendingTrafficBatch, err = c.trafficBatches.Load()
	if err != nil {
		return fmt.Errorf("load pending traffic batch: %w", err)
	}
	c.quotaManager, err = nodequota.Open(
		c.statePath,
		c.info.Id,
		c.info.Type,
		c.info.QuotaLeaseMode,
		int64(c.info.QuotaLeaseTTL),
		c.info.QuotaLeaseGrantBytes,
		c.apiClient,
	)
	if err != nil {
		return fmt.Errorf("open quota manager: %w", err)
	}
	quotaManagerOpened = true
	if err = c.quotaManager.UpdateUsers(c.userList); err != nil {
		return fmt.Errorf("persist initial quota users: %w", err)
	}

	// add limiter
	l := limiter.AddLimiter(c.tag, c.userList, c.aliveMap)
	c.limiter = l
	c.limiter.SetQuotaProvider(c.quotaManager.Budget)
	limiterAdded = true
	c.limiter.SetOnlineWindow(accessEventIdleWindow(c.info))
	presenceMode := normalizedPresenceMode(c.info.PresenceMode)
	if presenceMode != "disabled" {
		c.limiter.SetPresenceAuthorizer(
			presenceMode,
			presenceLeaseTTL(c.info.PresenceLeaseTTL),
			c.authorizePresence,
		)
	}
	if c.info.AccessAuditEnabled {
		c.accessEvents, err = openAccessEventStore(c.statePath, c.info.Id, c.info.Type)
		if err != nil {
			return fmt.Errorf("open access event store: %w", err)
		}
		accessStoreOpened = true
		c.limiter.SetAccessRecorder(c.recordAccessEvent, accessEventIdleWindow(c.info))
	}

	// Add new tag
	err = c.server.AddNode(c.tag, c.info)
	if err != nil {
		return fmt.Errorf("add new node error: %s", err)
	}
	nodeAdded = true
	added, err := c.server.AddUsers(&vCore.AddUsersParams{
		Tag:      c.tag,
		Users:    c.userList,
		NodeInfo: c.info,
	})
	if err != nil {
		return fmt.Errorf("add users error: %s", err)
	}
	c.portHopping, err = newPortHoppingManagerForNode(c.info)
	if err != nil {
		return fmt.Errorf("prepare port hopping error: %s", err)
	}
	if c.portHopping != nil {
		if err = c.portHopping.Apply(); err != nil {
			return fmt.Errorf("configure port hopping error: %s", err)
		}
		portHoppingAdded = true
	}
	log.WithField("节点", c.tag).Infof("已添加 %d 个新用户", added)
	c.startTasks(c.info)
	c.started = true
	return nil
}

// Close implement the Close() function of the service interface
func (c *Controller) Close() error {
	if c == nil {
		return nil
	}
	if c.userListMonitorPeriodic != nil {
		c.userListMonitorPeriodic.Close()
		c.userListMonitorPeriodic = nil
	}
	if c.userReportPeriodic != nil {
		c.userReportPeriodic.Close()
		c.userReportPeriodic = nil
	}
	c.trafficReportMu.Lock()
	c.pendingTrafficBatch = nil
	c.trafficReportMu.Unlock()
	if c.renewCertPeriodic != nil {
		c.renewCertPeriodic.Close()
		c.renewCertPeriodic = nil
	}
	if c.onlineIpReportPeriodic != nil {
		c.onlineIpReportPeriodic.Close()
		c.onlineIpReportPeriodic = nil
	}
	if c.presenceRenewPeriodic != nil {
		c.presenceRenewPeriodic.Close()
		c.presenceRenewPeriodic = nil
	}
	if c.abuseReportPeriodic != nil {
		c.abuseReportPeriodic.Close()
		c.abuseReportPeriodic = nil
	}
	if c.unregisterAbuseObserver != nil {
		c.unregisterAbuseObserver()
		c.unregisterAbuseObserver = nil
	}
	if c.abuseEvents != nil {
		c.abuseEvents.Close()
		c.abuseEvents = nil
	}
	c.pendingAbuseAggregates = nil
	if c.tag != "" {
		limiter.DeleteLimiter(c.tag)
	}
	var closeErr error
	if c.accessEvents != nil {
		closeErr = c.accessEvents.Close()
		c.accessEvents = nil
	}
	if c.trafficBatches != nil {
		closeErr = errors.Join(closeErr, c.trafficBatches.Close())
		c.trafficBatches = nil
	}
	if c.quotaManager != nil {
		closeErr = errors.Join(closeErr, c.quotaManager.Close())
		c.quotaManager = nil
	}
	if c.portHopping != nil {
		closeErr = errors.Join(closeErr, c.portHopping.Close())
		c.portHopping = nil
	}
	if !c.started || c.server == nil || c.tag == "" {
		c.started = false
		return closeErr
	}
	err := c.server.DelNode(c.tag)
	c.started = false
	if err != nil {
		closeErr = errors.Join(closeErr, fmt.Errorf("del node error: %s", err))
	}
	return closeErr
}

func (c *Controller) buildNodeTag(node *panel.NodeInfo) string {
	return fmt.Sprintf("[%s]-%s:%d", c.apiClient.APIHost, node.Type, node.Id)
}
