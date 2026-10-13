package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lighttous/ppanel-node/api/panel"
	vCore "github.com/lighttous/ppanel-node/core"
	bolt "go.etcd.io/bbolt"
)

const (
	trafficBatchBucket = "batches"
	trafficBatchKey    = "pending"
)

type persistedTrafficBatch struct {
	BatchID string              `json:"batch_id"`
	Traffic []panel.UserTraffic `json:"traffic"`
}

type trafficBatchStore struct {
	db *bolt.DB
}

func openTrafficBatchStore(statePath string, serverID int, protocol string) (*trafficBatchStore, error) {
	if statePath == "" {
		statePath = "/var/lib/ppanel-node"
	}
	dir := filepath.Join(statePath, "traffic-batches")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create traffic batch state directory: %w", err)
	}
	name := fmt.Sprintf("%d-%s.db", serverID, unsafeStateFileChars.ReplaceAllString(protocol, "_"))
	db, err := bolt.Open(filepath.Join(dir, name), 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open traffic batch store: %w", err)
	}
	store := &trafficBatchStore{db: db}
	if err = db.Update(func(tx *bolt.Tx) error {
		_, createErr := tx.CreateBucketIfNotExists([]byte(trafficBatchBucket))
		return createErr
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create traffic batch bucket: %w", err)
	}
	return store, nil
}

func (s *trafficBatchStore) Put(batch *vCore.UserTrafficBatch) error {
	if s == nil || s.db == nil {
		return errors.New("traffic batch store is not open")
	}
	if batch == nil || batch.BatchID == "" || len(batch.Traffic) == 0 {
		return errors.New("traffic batch is empty")
	}
	value, err := json.Marshal(persistedTrafficBatch{
		BatchID: batch.BatchID,
		Traffic: batch.Traffic,
	})
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(trafficBatchBucket))
		if current := bucket.Get([]byte(trafficBatchKey)); current != nil {
			var existing persistedTrafficBatch
			if err := json.Unmarshal(current, &existing); err != nil {
				return err
			}
			if existing.BatchID != batch.BatchID {
				return fmt.Errorf(
					"traffic batch %s is still pending; refusing to overwrite it with %s",
					existing.BatchID,
					batch.BatchID,
				)
			}
		}
		return bucket.Put([]byte(trafficBatchKey), value)
	})
}

func (s *trafficBatchStore) Load() (*vCore.UserTrafficBatch, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("traffic batch store is not open")
	}
	var persisted persistedTrafficBatch
	err := s.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket([]byte(trafficBatchBucket)).Get([]byte(trafficBatchKey))
		if value == nil {
			return nil
		}
		return json.Unmarshal(value, &persisted)
	})
	if err != nil {
		return nil, err
	}
	if persisted.BatchID == "" {
		return nil, nil
	}
	return vCore.RestoreUserTrafficBatch(persisted.BatchID, persisted.Traffic), nil
}

func (s *trafficBatchStore) Delete(batchID string) error {
	if s == nil || s.db == nil {
		return errors.New("traffic batch store is not open")
	}
	current, err := s.Load()
	if err != nil || current == nil {
		return err
	}
	if current.BatchID != batchID {
		return fmt.Errorf(
			"stored traffic batch %s does not match acknowledged batch %s",
			current.BatchID,
			batchID,
		)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(trafficBatchBucket)).Delete([]byte(trafficBatchKey))
	})
}

func (s *trafficBatchStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}
