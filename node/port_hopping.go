package node

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/lighttous/ppanel-node/api/panel"
)

const maxPortHoppingRangeSize = 4096

type nftRunner func(input string, args ...string) ([]byte, error)

var executeNFT nftRunner = func(input string, args ...string) ([]byte, error) {
	cmd := exec.Command("nft", args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	return cmd.CombinedOutput()
}

type portHoppingManager struct {
	tableName  string
	targetPort uint16
	rangeStart uint16
	rangeEnd   uint16
}

func newPortHoppingManagerForNode(info *panel.NodeInfo) (*portHoppingManager, error) {
	if info == nil || info.Protocol == nil {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(info.Type)) {
	case "hysteria2", "hy2":
	default:
		return nil, nil
	}
	if strings.TrimSpace(info.Protocol.HopPorts) == "" {
		return nil, nil
	}
	return newPortHoppingManager(info.Id, info.Protocol.Port, info.Protocol.HopPorts)
}

func newPortHoppingManager(nodeID int, targetPort int, portRange string) (*portHoppingManager, error) {
	if nodeID <= 0 {
		return nil, errors.New("port hopping requires a valid node ID")
	}
	if targetPort < 1 || targetPort > 65535 {
		return nil, errors.New("port hopping requires a valid target port")
	}
	rangeStart, rangeEnd, err := parsePortHoppingRange(portRange)
	if err != nil {
		return nil, err
	}

	return &portHoppingManager{
		tableName:  fmt.Sprintf("ppanel_hy2_%d_%d", nodeID, targetPort),
		targetPort: uint16(targetPort),
		rangeStart: rangeStart,
		rangeEnd:   rangeEnd,
	}, nil
}

func parsePortHoppingRange(value string) (uint16, uint16, error) {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("port hopping range %q must use start-end format", value)
	}

	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || start < 1 || start > 65535 {
		return 0, 0, fmt.Errorf("invalid port hopping range start %q", parts[0])
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || end < 1 || end > 65535 {
		return 0, 0, fmt.Errorf("invalid port hopping range end %q", parts[1])
	}
	if start > end {
		return 0, 0, errors.New("port hopping range start must not exceed end")
	}
	if end-start+1 > maxPortHoppingRangeSize {
		return 0, 0, fmt.Errorf("port hopping range exceeds %d ports", maxPortHoppingRangeSize)
	}

	return uint16(start), uint16(end), nil
}

func (m *portHoppingManager) Apply() error {
	if m == nil {
		return nil
	}

	// A stale table can remain after an ungraceful shutdown. Replacing it makes startup idempotent.
	_, _ = executeNFT("", "delete", "table", "inet", m.tableName)
	ruleset := fmt.Sprintf(
		"table inet %s {\n"+
			" chain prerouting {\n"+
			"  type nat hook prerouting priority dstnat; policy accept;\n"+
			"  udp dport %d-%d counter redirect to :%d;\n"+
			" };\n"+
			"}\n",
		m.tableName,
		m.rangeStart,
		m.rangeEnd,
		m.targetPort,
	)
	output, err := executeNFT(ruleset, "-f", "-")
	if err != nil {
		return fmt.Errorf("apply nftables port hopping rules: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *portHoppingManager) Close() error {
	if m == nil {
		return nil
	}
	output, err := executeNFT("", "delete", "table", "inet", m.tableName)
	if err != nil && !strings.Contains(strings.ToLower(string(output)), "no such file or directory") {
		return fmt.Errorf("remove nftables port hopping rules: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
