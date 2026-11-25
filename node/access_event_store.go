package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/lighttous/ppanel-node/api/panel"
	bolt "go.etcd.io/bbolt"
)

const accessEventBucket = "events"

var unsafeStateFileChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type accessEventStore struct {
	db *bolt.DB
}

func removeAccessEventState(statePath string) error {
	if statePath == "" {
		statePath = "/var/lib/ppanel-node"
	}
	return os.RemoveAll(filepath.Join(statePath, "access-events"))
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
