package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/lighttous/ppanel-node/api/panel"
	log "github.com/sirupsen/logrus"
	bolt "go.etcd.io/bbolt"
)

const accessEventBucket = "events"

var unsafeStateFileChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type accessEventStore struct {
	db *bolt.DB
}

func openAccessEventStore(statePath string, serverID int, protocol string) (*accessEventStore, error) {
	if statePath == "" {
		statePath = "/var/lib/ppanel-node"
	}
	dir := filepath.Join(statePath, "access-events")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create access event state directory: %w", err)
	}
	name := fmt.Sprintf("%d-%s.db", serverID, unsafeStateFileChars.ReplaceAllString(protocol, "_"))
	db, err := bolt.Open(filepath.Join(dir, name), 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open access event store: %w", err)
	}
	store := &accessEventStore{db: db}
	if err = db.Update(func(tx *bolt.Tx) error {
		_, createErr := tx.CreateBucketIfNotExists([]byte(accessEventBucket))
		return createErr
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create access event bucket: %w", err)
	}
	return store, nil
}

func (s *accessEventStore) Put(event panel.AccessEvent) error {
	if s == nil || s.db == nil {
		return errors.New("access event store is not open")
	}
	value, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(accessEventBucket)).Put([]byte(event.EventID), value)
	})
}

func (s *accessEventStore) List(limit int) ([]panel.AccessEvent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("access event store is not open")
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	events := make([]panel.AccessEvent, 0, limit)
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket([]byte(accessEventBucket)).Cursor()
		for key, value := cursor.First(); key != nil && len(events) < limit; key, value = cursor.Next() {
			var event panel.AccessEvent
			if err := json.Unmarshal(value, &event); err != nil {
				return fmt.Errorf("decode access event %q: %w", key, err)
			}
			events = append(events, event)
		}
		return nil
	})
	return events, err
}

func (s *accessEventStore) Delete(events []panel.AccessEvent) error {
	if s == nil || s.db == nil || len(events) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(accessEventBucket))
		for _, event := range events {
			if err := bucket.Delete([]byte(event.EventID)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *accessEventStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func accessEventIdleWindow(info *panel.NodeInfo) time.Duration {
	window := 2 * time.Duration(info.PushInterval) * time.Second
	if window < 90*time.Second {
		window = 90 * time.Second
	}
	return window
}

func (c *Controller) recordAccessEvent(event panel.AccessEvent) error {
	if c.accessEvents == nil {
		return errors.New("access event store is not open")
	}
	return c.accessEvents.Put(event)
}

func (c *Controller) reportAccessEvents(ctx context.Context) {
	if c.accessEvents == nil {
		return
	}
	for batch := 0; batch < 5; batch++ {
		events, err := c.accessEvents.List(500)
		if err != nil {
			log.WithFields(log.Fields{"tag": c.tag, "err": err}).
				Error("Read access event queue failed")
			return
		}
		if len(events) == 0 {
			return
		}
		if err = c.apiClient.ReportAccessEvents(ctx, events); err != nil {
			log.WithFields(log.Fields{"tag": c.tag, "err": err}).
				Warn("Report access events failed; events retained for retry")
			return
		}
		if err = c.accessEvents.Delete(events); err != nil {
			log.WithFields(log.Fields{"tag": c.tag, "err": err}).
				Error("Acknowledge access events failed")
			return
		}
		log.WithField("节点", c.tag).Infof("已上报 %d 条访问事件", len(events))
		if len(events) < 500 {
			return
		}
	}
}
