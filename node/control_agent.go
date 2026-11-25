package node

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lighttous/ppanel-node/api/panel"
	log "github.com/sirupsen/logrus"
)

const (
	controlReconnectMin = time.Second
	controlReconnectMax = time.Minute
	controlApplyTimeout = 30 * time.Second
	controlErrorLogRate = 5 * time.Minute
	controlReadTimeout  = 90 * time.Second
	controlWriteTimeout = 5 * time.Second
)

type controlAgent struct {
	client      *panel.ClientV1
	controllers []*Controller

	startOnce sync.Once
	closeOnce sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
	connMu    sync.Mutex
	conn      *websocket.Conn

	lastConnectionLog time.Time
}

func newControlAgent(
	client *panel.ClientV1,
	controllers []*Controller,
) *controlAgent {
	return &controlAgent{
		client:      client,
		controllers: controllers,
		done:        make(chan struct{}),
	}
}

func (a *controlAgent) Start() {
	if a == nil || a.client == nil {
		return
	}
	a.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		a.cancel = cancel
		go a.run(ctx)
	})
}

func (a *controlAgent) Close() {
	if a == nil {
		return
	}
	a.closeOnce.Do(func() {
		if a.cancel == nil {
			return
		}
		a.cancel()
		a.closeConnection()
		<-a.done
	})
}

func (a *controlAgent) run(ctx context.Context) {
	defer close(a.done)
	backoff := controlReconnectMin
	for {
		if ctx.Err() != nil {
			return
		}
		dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		conn, err := a.client.DialControl(dialCtx)
		cancel()
		if err == nil {
			if ctx.Err() != nil {
				_ = conn.Close()
				return
			}
			backoff = controlReconnectMin
			a.setConnection(conn)
			_ = conn.SetReadDeadline(time.Now().Add(controlReadTimeout))
			conn.SetPingHandler(func(payload string) error {
				_ = conn.SetReadDeadline(time.Now().Add(controlReadTimeout))
				return conn.WriteControl(
					websocket.PongMessage,
					[]byte(payload),
					time.Now().Add(controlWriteTimeout),
				)
			})
			err = a.readLoop(ctx, conn)
			a.clearConnection(conn)
			_ = conn.Close()
		}
		if ctx.Err() != nil {
			return
		}
		a.logConnectionError(err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > controlReconnectMax {
			backoff = controlReconnectMax
		}
	}
}

func (a *controlAgent) setConnection(conn *websocket.Conn) {
	a.connMu.Lock()
	a.conn = conn
	a.connMu.Unlock()
}

func (a *controlAgent) clearConnection(conn *websocket.Conn) {
	a.connMu.Lock()
	if a.conn == conn {
		a.conn = nil
	}
	a.connMu.Unlock()
}

func (a *controlAgent) closeConnection() {
	a.connMu.Lock()
	conn := a.conn
	a.conn = nil
	a.connMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (a *controlAgent) refreshTracking() {
	if a == nil {
		return
	}
	// Reconnecting rebuilds the server-side subject filter and obtains a fresh
	// snapshot, closing the race between a newly tracked user and its next event.
	a.closeConnection()
}

func (a *controlAgent) readLoop(ctx context.Context, conn controlConnection) error {
	var authorizationSnapshot map[int64]panel.ControlMessage
	for {
		var message panel.ControlMessage
		if err := conn.ReadJSON(&message); err != nil {
			return err
		}
		switch message.Type {
		case "ready":
			if err := conn.WriteJSON(panel.ControlRequest{
				Type:            "snapshot_request",
				SubscriptionIDs: a.trackedSubscriptions(),
			}); err != nil {
				return err
			}
			continue
		case "snapshot_begin":
			authorizationSnapshot = make(map[int64]panel.ControlMessage)
			continue
		case "snapshot_end":
			if authorizationSnapshot == nil {
				continue
			}
			applyCtx, cancel := context.WithTimeout(ctx, controlApplyTimeout)
			var applyErr error
			for _, controller := range a.controllers {
				if controller == nil || !controller.info.Protocol.Enable {
					continue
				}
				if err := controller.replaceAuthorizationSnapshot(applyCtx, authorizationSnapshot); err != nil {
					log.WithFields(log.Fields{
						"tag": controller.tag,
						"err": err,
					}).Warn("Apply subscription authorization snapshot failed")
					applyErr = errors.Join(applyErr, err)
				}
			}
			cancel()
			authorizationSnapshot = nil
			if applyErr != nil {
				return applyErr
			}
			continue
		case "authorization_changed":
			if authorizationSnapshot != nil {
				authorizationSnapshot[message.UserSubscribeID] = message
				continue
			}
		default:
			continue
		}
		applyCtx, cancel := context.WithTimeout(ctx, controlApplyTimeout)
		var applyErr error
		for _, controller := range a.controllers {
			if controller == nil || !controller.info.Protocol.Enable {
				continue
			}
			if err := controller.applyControlMessage(applyCtx, message); err != nil {
				log.WithFields(log.Fields{
					"tag":             controller.tag,
					"event_id":        message.EventID,
					"subscription_id": message.UserSubscribeID,
					"err":             err,
				}).Warn("Apply subscription control event failed; polling will retry")
				applyErr = errors.Join(applyErr, err)
			}
		}
		cancel()
		if applyErr != nil {
			return applyErr
		}
	}
}

func (a *controlAgent) trackedSubscriptions() []int64 {
	tracked := make(map[int64]struct{})
	for _, controller := range a.controllers {
		if controller == nil {
			continue
		}
		for _, subscriptionID := range controller.trackedSubscriptions() {
			tracked[subscriptionID] = struct{}{}
		}
	}
	ids := make([]int64, 0, len(tracked))
	for subscriptionID := range tracked {
		ids = append(ids, subscriptionID)
	}
	return ids
}

func (a *controlAgent) logConnectionError(err error) {
	now := time.Now()
	if err == nil ||
		(!a.lastConnectionLog.IsZero() &&
			now.Sub(a.lastConnectionLog) < controlErrorLogRate) {
		return
	}
	a.lastConnectionLog = now
	log.WithError(err).Warn("Node control channel unavailable; user polling remains active")
}

type controlConnection interface {
	ReadJSON(interface{}) error
	WriteJSON(interface{}) error
}
