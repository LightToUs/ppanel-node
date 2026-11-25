package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type persistedControlState struct {
	Revoked map[int64]int64 `json:"revoked"`
}

func controlStatePath(statePath string, serverID int, protocol string) string {
	if statePath == "" {
		statePath = "/var/lib/ppanel-node"
	}
	name := fmt.Sprintf(
		"%d-%s.json",
		serverID,
		unsafeStateFileChars.ReplaceAllString(protocol, "_"),
	)
	return filepath.Join(statePath, "subscription-control", name)
}

func loadControlState(
	statePath string,
	serverID int,
	protocol string,
) (map[int64]int64, error) {
	path := controlStatePath(statePath, serverID, protocol)
	payload, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return make(map[int64]int64), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read subscription control state: %w", err)
	}
	var state persistedControlState
	if err = json.Unmarshal(payload, &state); err != nil {
		return nil, fmt.Errorf("decode subscription control state: %w", err)
	}
	if state.Revoked == nil {
		state.Revoked = make(map[int64]int64)
	}
	return state.Revoked, nil
}

func saveControlState(
	statePath string,
	serverID int,
	protocol string,
	revoked map[int64]int64,
) error {
	path := controlStatePath(statePath, serverID, protocol)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create subscription control state directory: %w", err)
	}
	payload, err := json.Marshal(persistedControlState{Revoked: revoked})
	if err != nil {
		return fmt.Errorf("encode subscription control state: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".subscription-control-*.tmp")
	if err != nil {
		return fmt.Errorf("create subscription control state file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(payload)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write subscription control state: %w", err)
	}
	if err = os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("commit subscription control state: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open subscription control state directory: %w", err)
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return fmt.Errorf("sync subscription control state directory: %w", err)
	}
	return nil
}
