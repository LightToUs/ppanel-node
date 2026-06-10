package node

import (
	"context"
	"errors"
	"fmt"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/common/task"
	vCore "github.com/lighttous/ppanel-node/core"
	"github.com/lighttous/ppanel-node/limiter"
	log "github.com/sirupsen/logrus"
)

type Controller struct {
	server                  *vCore.XrayCore
	apiClient               *panel.ClientV1
	tag                     string
	started                 bool
	limiter                 *limiter.Limiter
	userList                []panel.UserInfo
	aliveMap                map[int]int
	info                    *panel.NodeInfo
	userListMonitorPeriodic *task.Task
	userReportPeriodic      *task.Task
	renewCertPeriodic       *task.Task
	onlineIpReportPeriodic  *task.Task
}

// NewController return a Node controller with default parameters.
func NewController(core *vCore.XrayCore, api *panel.ClientV1, info *panel.NodeInfo) *Controller {
	controller := &Controller{
		server:    core,
		apiClient: api,
		info:      info,
	}
	return controller
}

// Start implement the Start() function of the service interface
func (c *Controller) Start() error {
	if c == nil || c.started {
		return nil
	}
	var err error
	var nodeAdded bool
	var limiterAdded bool
	defer func() {
		if err == nil {
			return
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
		c.started = false
	}()

	// Update user
	c.userList, err = c.apiClient.GetUserList(context.Background())
	if err != nil {
		return fmt.Errorf("get user list error: %s", err)
	}
	if len(c.userList) == 0 {
		return errors.New("add users error: not have any user")
	}
	c.aliveMap, err = c.apiClient.GetUserAlive()
	if err != nil {
		return fmt.Errorf("failed to get user alive list: %s", err)
	}
	c.tag = c.buildNodeTag(c.info)

	// add limiter
	l := limiter.AddLimiter(c.tag, c.userList, c.aliveMap)
	c.limiter = l
	limiterAdded = true

	if c.info.Protocol.Security == "tls" {
		err = c.requestCert()
		if err != nil {
			return fmt.Errorf("request cert error: %s", err)
		}
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
	if c.renewCertPeriodic != nil {
		c.renewCertPeriodic.Close()
		c.renewCertPeriodic = nil
	}
	if c.onlineIpReportPeriodic != nil {
		c.onlineIpReportPeriodic.Close()
		c.onlineIpReportPeriodic = nil
	}
	if c.tag != "" {
		limiter.DeleteLimiter(c.tag)
	}
	if !c.started || c.server == nil || c.tag == "" {
		c.started = false
		return nil
	}
	err := c.server.DelNode(c.tag)
	c.started = false
	if err != nil {
		return fmt.Errorf("del node error: %s", err)
	}
	return nil
}

func (c *Controller) buildNodeTag(node *panel.NodeInfo) string {
	return fmt.Sprintf("[%s]-%s:%d", c.apiClient.APIHost, node.Type, node.Id)
}
