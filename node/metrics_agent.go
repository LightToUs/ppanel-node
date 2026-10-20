package node

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/common/serverstatus"
	"github.com/lighttous/ppanel-node/common/task"
	vCore "github.com/lighttous/ppanel-node/core"
	gnet "github.com/shirou/gopsutil/v3/net"
	log "github.com/sirupsen/logrus"
)

const metricsInterval = 10 * time.Second
const metricsWarningInterval = 5 * time.Minute

type MetricsAgent struct {
	client            *panel.ClientV1
	core              *vCore.XrayCore
	interfaceOverride string
	interfaceName     string
	sessionID         string
	sequence          uint64
	reportTask        *task.Task
	access            sync.Mutex
	warningAt         map[string]time.Time
}

func NewMetricsAgent(
	client *panel.ClientV1,
	core *vCore.XrayCore,
	interfaceOverride string,
) *MetricsAgent {
	return &MetricsAgent{
		client:            client,
		core:              core,
		interfaceOverride: interfaceOverride,
		sessionID:         uuid.NewString(),
		warningAt:         make(map[string]time.Time),
	}
}

func (a *MetricsAgent) Start() {
	if a == nil || a.client == nil {
		return
	}
	a.access.Lock()
	defer a.access.Unlock()
	if a.reportTask != nil {
		return
	}
	var reloadCh chan struct{}
	if a.core != nil {
		reloadCh = a.core.ReloadCh
	}
	// A stable per-process jitter spreads reports from nodes started together.
	jitter := time.Duration(time.Now().UnixNano()%int64(3*time.Second)) - time.Second
	a.reportTask = &task.Task{
		Name:     "reportServerMetrics",
		Interval: metricsInterval + jitter,
		Execute:  a.report,
		ReloadCh: reloadCh,
	}
	_ = a.reportTask.Start(true)
	log.WithField("interval", a.reportTask.Interval).Info("服务器实时指标任务已启动")
}

func (a *MetricsAgent) Close() {
	if a == nil {
		return
	}
	a.access.Lock()
	reportTask := a.reportTask
	a.reportTask = nil
	a.access.Unlock()
	if reportTask != nil {
		reportTask.Close()
	}
}

func (a *MetricsAgent) report(ctx context.Context) error {
	interfaceName, err := a.resolveInterface()
	if err != nil {
		a.warnRateLimited("resolve-interface", "Resolve metrics network interface failed", err)
		return nil
	}
	rxBytes, txBytes, err := readNetworkCounters(interfaceName)
	if err != nil {
		a.interfaceName = ""
		a.warnRateLimited("read-interface", "Read metrics network counters failed", err)
		return nil
	}
	cpu, memory, disk, uptime, statusErr := serverstatus.GetSystemInfo()
	if statusErr != nil {
		a.warnRateLimited("collect-system", "Collect server metrics partially failed", statusErr)
	}
	bootTime, bootID := bootIdentity(uptime)
	a.sequence++
	if err = a.client.ReportServerMetrics(ctx, &panel.ServerMetricsReport{
		SchemaVersion:  1,
		AgentSessionID: a.sessionID,
		Sequence:       a.sequence,
		CollectedAt:    time.Now().UnixMilli(),
		BootID:         bootID,
		BootTime:       bootTime,
		UptimeSeconds:  uptime,
		InterfaceName:  interfaceName,
		UploadBytes:    txBytes,
		DownloadBytes:  rxBytes,
		CPUPercent:     cpu,
		MemoryPercent:  memory,
		DiskPercent:    disk,
	}); err != nil {
		a.warnRateLimited("report", "Report server realtime metrics failed", err)
	}
	// Metrics are best effort and must never stop the periodic task or proxy runtime.
	return nil
}

func (a *MetricsAgent) warnRateLimited(key, message string, err error) {
	now := time.Now()
	if last, ok := a.warningAt[key]; ok && now.Sub(last) < metricsWarningInterval {
		return
	}
	a.warningAt[key] = now
	log.WithField("err", err).Warn(message)
}

func (a *MetricsAgent) resolveInterface() (string, error) {
	if a.interfaceOverride != "" {
		return a.interfaceOverride, nil
	}
	if a.interfaceName != "" {
		return a.interfaceName, nil
	}
	interfaceName, err := defaultRouteInterface()
	if err != nil {
		return "", err
	}
	a.interfaceName = interfaceName
	return interfaceName, nil
}

func bootIdentity(uptime uint64) (int64, string) {
	bootTime := time.Now().Add(-time.Duration(uptime) * time.Second).Unix()
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err == nil {
		if bootID := strings.TrimSpace(string(raw)); bootID != "" {
			return bootTime, bootID
		}
	}
	return bootTime, fmt.Sprintf("boot-%d", bootTime)
}

func defaultRouteInterface() (string, error) {
	file, err := os.Open("/proc/net/route")
	if err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 4 || fields[1] != "00000000" {
				continue
			}
			flags, parseErr := strconv.ParseUint(fields[3], 16, 64)
			if parseErr == nil && flags&0x1 != 0 && isMetricsInterface(fields[0]) {
				return fields[0], nil
			}
		}
	}

	interfaces, listErr := gnet.Interfaces()
	if listErr != nil {
		return "", listErr
	}
	for _, networkInterface := range interfaces {
		if isMetricsInterface(networkInterface.Name) {
			return networkInterface.Name, nil
		}
	}
	return "", errors.New("no eligible network interface found")
}

func isMetricsInterface(name string) bool {
	if name == "" || name == "lo" {
		return false
	}
	for _, prefix := range []string{
		"docker", "veth", "br-", "virbr", "tun", "tap", "wg", "pp-",
	} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

func readNetworkCounters(interfaceName string) (rxBytes, txBytes uint64, err error) {
	counters, err := gnet.IOCounters(true)
	if err != nil {
		return 0, 0, err
	}
	for _, counter := range counters {
		if counter.Name == interfaceName {
			return counter.BytesRecv, counter.BytesSent, nil
		}
	}
	return 0, 0, fmt.Errorf("network interface %q not found", interfaceName)
}
