package node

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lighttous/ppanel-node/api/panel"
)

const trafficSuspensionTable = "ppanel_traffic_guard"

type trafficSuspensionFirewall struct {
	tcpPorts []uint16
	udpPorts []uint16
}

func newTrafficSuspensionFirewall(protocols []panel.Protocol) *trafficSuspensionFirewall {
	tcpPorts := make(map[uint16]struct{})
	udpPorts := make(map[uint16]struct{})
	for i := range protocols {
		protocol := &protocols[i]
		if !protocol.Enable || protocol.Port < 1 || protocol.Port > 65535 {
			continue
		}
		port := uint16(protocol.Port)
		switch strings.ToLower(strings.TrimSpace(protocol.Type)) {
		case "hysteria2", "hy2", "tuic":
			udpPorts[port] = struct{}{}
		default:
			// Xray protocols can expose TCP and UDP on the same inbound port.
			tcpPorts[port] = struct{}{}
			udpPorts[port] = struct{}{}
		}
		if strings.TrimSpace(protocol.HopPorts) != "" {
			start, end, err := parsePortHoppingRange(protocol.HopPorts)
			if err == nil {
				for hopPort := uint32(start); hopPort <= uint32(end); hopPort++ {
					udpPorts[uint16(hopPort)] = struct{}{}
				}
			}
		}
	}
	return &trafficSuspensionFirewall{
		tcpPorts: sortedPorts(tcpPorts),
		udpPorts: sortedPorts(udpPorts),
	}
}

func (f *trafficSuspensionFirewall) Apply() error {
	if f == nil || len(f.tcpPorts)+len(f.udpPorts) == 0 {
		return nil
	}
	_, _ = executeNFT("", "delete", "table", "inet", trafficSuspensionTable)
	var rules strings.Builder
	fmt.Fprintf(&rules, "table inet %s {\n", trafficSuspensionTable)
	rules.WriteString(" chain input {\n")
	rules.WriteString("  type filter hook input priority -20; policy accept;\n")
	if len(f.tcpPorts) > 0 {
		fmt.Fprintf(
			&rules,
			"  tcp dport { %s } counter reject with tcp reset\n",
			formatPorts(f.tcpPorts),
		)
	}
	if len(f.udpPorts) > 0 {
		fmt.Fprintf(
			&rules,
			"  udp dport { %s } counter drop\n",
			formatPorts(f.udpPorts),
		)
	}
	rules.WriteString(" }\n}\n")
	output, err := executeNFT(rules.String(), "-f", "-")
	if err != nil {
		return fmt.Errorf(
			"apply traffic suspension firewall: %w: %s",
			err,
			strings.TrimSpace(string(output)),
		)
	}
	return nil
}

func (f *trafficSuspensionFirewall) Close() error {
	output, err := executeNFT("", "delete", "table", "inet", trafficSuspensionTable)
	if err != nil && !strings.Contains(strings.ToLower(string(output)), "no such file or directory") {
		return fmt.Errorf(
			"remove traffic suspension firewall: %w: %s",
			err,
			strings.TrimSpace(string(output)),
		)
	}
	return nil
}

func sortedPorts(ports map[uint16]struct{}) []uint16 {
	result := make([]uint16, 0, len(ports))
	for port := range ports {
		result = append(result, port)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})
	return result
}

func formatPorts(ports []uint16) string {
	values := make([]string, 0, len(ports))
	for _, port := range ports {
		values = append(values, strconv.FormatUint(uint64(port), 10))
	}
	return strings.Join(values, ", ")
}
