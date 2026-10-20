package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/common/serverstatus"
	"github.com/lighttous/ppanel-node/common/task"
	vCore "github.com/lighttous/ppanel-node/core"
	log "github.com/sirupsen/logrus"
	bolt "go.etcd.io/bbolt"
)

const (
	relayInterfaceName = "pp-egress"
	relayStateBucket   = "relay"
	relayStateKey      = "traffic"
)

type relayTrafficState struct {
	SessionID       string `json:"session_id"`
	Sequence        uint64 `json:"sequence"`
	UploadTotal     uint64 `json:"upload_total"`
	DownloadTotal   uint64 `json:"download_total"`
	LastRX          uint64 `json:"last_rx"`
	LastTX          uint64 `json:"last_tx"`
	BaselinePresent bool   `json:"baseline_present"`
}

type relayTrafficStore struct {
	db *bolt.DB
}

func openRelayTrafficStore(statePath string, serverID int) (*relayTrafficStore, error) {
	if statePath == "" {
		statePath = "/var/lib/ppanel-node"
	}
	dir := filepath.Join(statePath, "relay")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create relay state directory: %w", err)
	}
	db, err := bolt.Open(
		filepath.Join(dir, fmt.Sprintf("%d.db", serverID)),
		0o600,
		&bolt.Options{Timeout: time.Second},
	)
	if err != nil {
		return nil, fmt.Errorf("open relay state: %w", err)
	}
	store := &relayTrafficStore{db: db}
	if err = db.Update(func(tx *bolt.Tx) error {
		_, createErr := tx.CreateBucketIfNotExists([]byte(relayStateBucket))
		return createErr
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *relayTrafficStore) update(
	rx, txBytes uint64,
) (relayTrafficState, error) {
	var result relayTrafficState
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(relayStateBucket))
		raw := bucket.Get([]byte(relayStateKey))
		if raw != nil {
			if err := json.Unmarshal(raw, &result); err != nil {
				return fmt.Errorf("decode relay traffic state: %w", err)
			}
		}
		if result.SessionID == "" {
			result.SessionID = uuid.NewString()
		}
		if !result.BaselinePresent {
			result.LastRX = rx
			result.LastTX = txBytes
			result.BaselinePresent = true
		} else {
			uploadDelta := counterDelta(result.LastRX, rx)
			downloadDelta := counterDelta(result.LastTX, txBytes)
			if uploadDelta > math.MaxInt64-result.UploadTotal ||
				downloadDelta > math.MaxInt64-result.DownloadTotal {
				return errors.New("relay traffic counter overflow")
			}
			result.UploadTotal += uploadDelta
			result.DownloadTotal += downloadDelta
			result.LastRX = rx
			result.LastTX = txBytes
		}
		result.Sequence++
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(relayStateKey), encoded)
	})
	return result, err
}

func counterDelta(previous, current uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	// The interface was recreated or the host rebooted.
	return current
}

func (s *relayTrafficStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

type RelayAgent struct {
	client       *panel.ClientV1
	statePath    string
	serverID     int
	pushInterval time.Duration
	core         *vCore.XrayCore
	store        *relayTrafficStore
	reportTask   *task.Task
	access       sync.Mutex
	readCounters func(string) (uint64, uint64, error)
}

func NewRelayAgent(
	client *panel.ClientV1,
	statePath string,
	serverID, pushInterval int,
	core *vCore.XrayCore,
) *RelayAgent {
	return &RelayAgent{
		client:       client,
		statePath:    statePath,
		serverID:     serverID,
		pushInterval: time.Duration(pushInterval) * time.Second,
		core:         core,
		readCounters: readInterfaceCounters,
	}
}

func (a *RelayAgent) Start() error {
	a.access.Lock()
	defer a.access.Unlock()
	if a.reportTask != nil {
		return nil
	}
	store, err := openRelayTrafficStore(a.statePath, a.serverID)
	if err != nil {
		return err
	}
	a.store = store
	var reloadCh chan struct{}
	if a.core != nil {
		reloadCh = a.core.ReloadCh
	}
	a.reportTask = &task.Task{
		Name:     "reportRelayEgress",
		Interval: a.pushInterval,
		Execute:  a.report,
		ReloadCh: reloadCh,
	}
	if err = a.reportTask.Start(true); err != nil {
		_ = store.Close()
		a.store = nil
		a.reportTask = nil
		return err
	}
	log.WithField("interface", relayInterfaceName).Info("中转落地监控任务已启动")
	return nil
}

func (a *RelayAgent) report(ctx context.Context) error {
	cpu, mem, disk, uptime, err := serverstatus.GetSystemInfo()
	if err != nil {
		log.WithField("err", err).Warn("Collect relay system status failed")
	} else if err = a.client.ReportNodeStatus(&panel.NodeStatus{
		CPU: cpu, Mem: mem, Disk: disk, Uptime: uptime,
	}); err != nil {
		log.WithField("err", err).Warn("Report relay system status failed")
	}

	rx, txBytes, err := a.readCounters(relayInterfaceName)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.WithField("err", err).Warn("Read relay interface counters failed")
		}
		return nil
	}
	state, err := a.store.update(rx, txBytes)
	if err != nil {
		log.WithField("err", err).Error("Persist relay traffic checkpoint failed")
		return nil
	}
	err = a.client.ReportRelayTraffic(ctx, &panel.RelayTrafficReport{
		InterfaceName: relayInterfaceName,
		SessionID:     state.SessionID,
		Sequence:      state.Sequence,
		UploadTotal:   state.UploadTotal,
		DownloadTotal: state.DownloadTotal,
		ReportedAt:    time.Now().UnixMilli(),
	})
	if err != nil {
		log.WithFields(log.Fields{
			"sequence": state.Sequence,
			"err":      err,
		}).Warn("Report relay traffic failed; cumulative checkpoint retained")
	}
	return nil
}

func (a *RelayAgent) Close() error {
	a.access.Lock()
	reportTask := a.reportTask
	a.reportTask = nil
	store := a.store
	a.store = nil
	a.access.Unlock()
	if reportTask != nil {
		reportTask.Close()
	}
	if store != nil {
		return store.Close()
	}
	return nil
}

func readInterfaceCounters(interfaceName string) (uint64, uint64, error) {
	if strings.ContainsAny(interfaceName, `/\`) {
		return 0, 0, errors.New("invalid network interface name")
	}
	base := filepath.Join("/sys/class/net", interfaceName, "statistics")
	rx, err := readUintFile(filepath.Join(base, "rx_bytes"))
	if err != nil {
		return 0, 0, err
	}
	txBytes, err := readUintFile(filepath.Join(base, "tx_bytes"))
	if err != nil {
		return 0, 0, err
	}
	return rx, txBytes, nil
}

func readUintFile(path string) (uint64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
}
