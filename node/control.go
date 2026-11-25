package node

import (
	"context"
	"fmt"

	"github.com/lighttous/ppanel-node/api/panel"
	log "github.com/sirupsen/logrus"
)

func (c *Controller) applyControlMessage(
	ctx context.Context,
	message panel.ControlMessage,
) error {
	if c == nil || !c.started {
		return nil
	}
	if message.DesiredState == "authorized" {
		c.userMutationMu.Lock()
		if !c.isSubscriptionTrackedLocked(message.UserSubscribeID) {
			c.userMutationMu.Unlock()
			return nil
		}
		if c.controlRevisions[message.UserSubscribeID] >= message.AuthRevision {
			c.userMutationMu.Unlock()
			return nil
		}
		previousRevision, hadRevision := c.controlRevisions[message.UserSubscribeID]
		previousState, hadState := c.controlStates[message.UserSubscribeID]
		c.controlRevisions[message.UserSubscribeID] = message.AuthRevision
		c.controlStates[message.UserSubscribeID] = message.DesiredState
		c.apiClient.ResetUserETag()
		c.userMutationMu.Unlock()
		if err := c.syncUserList(ctx); err != nil {
			c.userMutationMu.Lock()
			if c.controlRevisions[message.UserSubscribeID] == message.AuthRevision &&
				c.controlStates[message.UserSubscribeID] == message.DesiredState {
				if hadRevision {
					c.controlRevisions[message.UserSubscribeID] = previousRevision
				} else {
					delete(c.controlRevisions, message.UserSubscribeID)
				}
				if hadState {
					c.controlStates[message.UserSubscribeID] = previousState
				} else {
					delete(c.controlStates, message.UserSubscribeID)
				}
			}
			c.apiClient.ResetUserETag()
			c.userMutationMu.Unlock()
			return err
		}
		c.userMutationMu.Lock()
		err := c.persistControlStateLocked()
		c.userMutationMu.Unlock()
		return err
	}
	if message.DesiredState != "revoked" {
		return fmt.Errorf("unsupported authorization state %q", message.DesiredState)
	}

	c.userMutationMu.Lock()
	defer c.userMutationMu.Unlock()
	if c.controlRevisions[message.UserSubscribeID] >= message.AuthRevision {
		return nil
	}

	if !c.needsRevocationLocked(message.UserSubscribeID) {
		if _, tracked := c.controlStates[message.UserSubscribeID]; tracked {
			c.controlRevisions[message.UserSubscribeID] = message.AuthRevision
			c.controlStates[message.UserSubscribeID] = message.DesiredState
			return c.persistControlStateLocked()
		}
		return nil
	}
	if err := c.applyRevocationsLocked(map[int64]struct{}{
		message.UserSubscribeID: {},
	}); err != nil {
		return err
	}
	c.controlRevisions[message.UserSubscribeID] = message.AuthRevision
	c.controlStates[message.UserSubscribeID] = message.DesiredState
	c.apiClient.ResetUserETag()
	return c.persistControlStateLocked()
}

func (c *Controller) needsRevocation(subscriptionID int64) bool {
	c.userMutationMu.Lock()
	defer c.userMutationMu.Unlock()
	return c.needsRevocationLocked(subscriptionID)
}

func (c *Controller) needsRevocationLocked(subscriptionID int64) bool {
	if c.controlStates[subscriptionID] == "revoked" {
		return true
	}
	return c.hasRuntimeSubscriptionLocked(subscriptionID)
}

func (c *Controller) isSubscriptionTrackedLocked(subscriptionID int64) bool {
	if _, tracked := c.controlStates[subscriptionID]; tracked {
		return true
	}
	return c.hasRuntimeSubscriptionLocked(subscriptionID)
}

func (c *Controller) hasRuntimeSubscriptionLocked(subscriptionID int64) bool {
	for _, current := range c.userList {
		if current.Id == int(subscriptionID) {
			return true
		}
	}
	return false
}

func (c *Controller) trackedSubscriptions() []int64 {
	c.userMutationMu.Lock()
	defer c.userMutationMu.Unlock()
	tracked := make(map[int64]struct{}, len(c.userList)+len(c.controlStates))
	for _, current := range c.userList {
		tracked[int64(current.Id)] = struct{}{}
	}
	for subscriptionID := range c.controlStates {
		tracked[subscriptionID] = struct{}{}
	}
	ids := make([]int64, 0, len(tracked))
	for subscriptionID := range tracked {
		ids = append(ids, subscriptionID)
	}
	return ids
}

func (c *Controller) replaceAuthorizationSnapshot(
	ctx context.Context,
	snapshot map[int64]panel.ControlMessage,
) error {
	c.userMutationMu.Lock()
	revisions := make(map[int64]int64, len(snapshot))
	states := make(map[int64]string, len(snapshot))
	revoked := make(map[int64]struct{})
	for subscriptionID, message := range snapshot {
		if !c.isSubscriptionTrackedLocked(subscriptionID) {
			continue
		}
		revisions[subscriptionID] = message.AuthRevision
		states[subscriptionID] = message.DesiredState
		if message.DesiredState == "revoked" {
			revoked[subscriptionID] = struct{}{}
		}
	}
	if err := c.applyRevocationsLocked(revoked); err != nil {
		c.userMutationMu.Unlock()
		return err
	}
	c.controlRevisions = revisions
	c.controlStates = states
	c.apiClient.ResetUserETag()
	if err := c.persistControlStateLocked(); err != nil {
		c.userMutationMu.Unlock()
		return err
	}
	c.userMutationMu.Unlock()
	return c.syncUserList(ctx)
}

func (c *Controller) loadControlState() error {
	if c == nil || c.info == nil {
		return nil
	}
	revoked, err := loadControlState(c.statePath, c.info.Id, c.info.Type)
	if err != nil {
		return err
	}
	for subscriptionID, revision := range revoked {
		c.controlRevisions[subscriptionID] = revision
		c.controlStates[subscriptionID] = "revoked"
	}
	c.userList = c.filterRevokedUsers(c.userList)
	return nil
}

func (c *Controller) persistControlStateLocked() error {
	if c == nil || c.info == nil {
		return nil
	}
	revoked := make(map[int64]int64)
	for subscriptionID, state := range c.controlStates {
		if state == "revoked" {
			revoked[subscriptionID] = c.controlRevisions[subscriptionID]
		}
	}
	return saveControlState(c.statePath, c.info.Id, c.info.Type, revoked)
}

func (c *Controller) applyRevocationsLocked(revoked map[int64]struct{}) error {
	newUsers := make([]panel.UserInfo, 0, len(c.userList))
	deleted := make([]panel.UserInfo, 0, len(revoked))
	for _, current := range c.userList {
		if _, shouldRevoke := revoked[int64(current.Id)]; shouldRevoke {
			deleted = append(deleted, current)
			continue
		}
		newUsers = append(newUsers, current)
	}
	if len(deleted) == 0 {
		return nil
	}
	if c.quotaManager != nil {
		if err := c.quotaManager.UpdateUsers(newUsers); err != nil {
			return fmt.Errorf("persist revoked quota users: %w", err)
		}
	}
	if err := c.applyUserChanges(deleted, nil, nil); err != nil {
		if c.quotaManager != nil {
			if rollbackErr := c.quotaManager.UpdateUsers(c.userList); rollbackErr != nil {
				log.WithFields(log.Fields{"tag": c.tag, "err": rollbackErr}).
					Error("Rollback quota users after control failure")
			}
		}
		return fmt.Errorf("remove revoked user from runtime: %w", err)
	}
	c.limiter.UpdateUser(c.tag, nil, deleted)
	c.userList = newUsers
	if c.abuseEvents != nil {
		c.abuseEvents.UpdateUsers(newUsers)
	}
	return nil
}

func (c *Controller) filterRevokedUsers(users []panel.UserInfo) []panel.UserInfo {
	filtered := users[:0]
	for _, candidate := range users {
		if c.controlStates[int64(candidate.Id)] == "revoked" {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}
