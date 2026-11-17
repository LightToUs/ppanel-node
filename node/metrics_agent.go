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
const budgetGuardInterval = time.Second
const metricsWarningInterval = 5 * time.Minute

type MetricsAgent struct {
	client            *panel.ClientV1
	core              *vCore.XrayCore
	interfaceOverride string
	interfaceName     string
	readCounters      func(string) (uint64, uint64, error)
	setSuspended      func(bool)
	sessionID         string
	sequence          uint64
	reportTask        *task.Task
	guardTask         *task.Task
	access            sync.Mutex
	budgetMu          sync.Mutex
	budgetDirective   *panel.ServerTrafficBudgetDirective
	budgetGeneration  uint64
	baselineUpload    uint64
	baselineDownload  uint64
	budgetInterface   string
	warningMu         sync.Mutex
	warningAt         map[string]time.Time
	unsupported       bool
}

func NewMetricsAgent(
	client *panel.ClientV1,
	core *vCore.XrayCore,
	interfaceOverride string,
) *MetricsAgent {
	agent := &MetricsAgent{
		client:            client,
		core:              core,
		interfaceOverride: interfaceOverride,
		readCounters:      readNetworkCounters,
		sessionID:         uuid.NewString(),
		warningAt:         make(map[string]time.Time),
	}
	if core != nil {
		agent.setSuspended = core.SetTrafficSuspended
	}
	return agent
}

// Prime obtains the initial budget directive before proxy traffic is accepted.
// A legacy control plane without the metrics endpoint remains compatible.
func (a *MetricsAgent) Prime(ctx context.Context) error {
	if a == nil || a.client == nil || a.unsupported {
		return nil
	}
	err := a.collectAndReport(ctx)
	if errors.Is(err, panel.ErrMetricsUnsupported) {
		a.unsupported = true
		log.Info("控制面未启用实时指标接口，已禁用本进程的指标上报")
		return nil
	}
	return err
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
	a.guardTask = &task.Task{
		Name:     "enforceServerTrafficBudget",
		Interval: budgetGuardInterval,
		Execute:  a.enforceBudget,
		ReloadCh: reloadCh,
	}
	_ = a.reportTask.Start(true)
	_ = a.guardTask.Start(false)
	log.WithField("interval", a.reportTask.Interval).Info("服务器实时指标任务已启动")
}

func (a *MetricsAgent) Close() {
	if a == nil {
		return
	}
	a.access.Lock()
	reportTask := a.reportTask
	guardTask := a.guardTask
	a.reportTask = nil
	a.guardTask = nil
	a.access.Unlock()
	if reportTask != nil {
		reportTask.Close()
	}
	if guardTask != nil {
		guardTask.Close()
	}
}

func (a *MetricsAgent) report(ctx context.Context) error {
	if a.unsupported {
		return nil
	}
	err := a.collectAndReport(ctx)
	if errors.Is(err, panel.ErrMetricsUnsupported) {
		a.unsupported = true
		log.Info("控制面未启用实时指标接口，已停止本进程的指标上报")
		return nil
	}
	if err != nil {
		a.warnRateLimited("report", "Report server realtime metrics failed", err)
	}
	// Metrics are best effort after the initial directive has been obtained.
	return nil
}

func (a *MetricsAgent) collectAndReport(ctx context.Context) error {
	interfaceName, err := a.resolveInterface()
	if err != nil {
		return fmt.Errorf("resolve metrics network interface: %w", err)
	}
	rxBytes, txBytes, err := a.readCounters(interfaceName)
	if err != nil {
		a.interfaceName = ""
		return fmt.Errorf("read metrics network counters: %w", err)
	}
	cpu, memory, disk, uptime, statusErr := serverstatus.GetSystemInfo()
	if statusErr != nil {
		a.warnRateLimited("collect-system", "Collect server metrics partially failed", statusErr)
	}
	bootTime, bootID := bootIdentity(uptime)
	a.sequence++
	directive, err := a.client.ReportServerMetrics(ctx, &panel.ServerMetricsReport{
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
	})
	if err != nil {
		return err
	}
	a.applyBudgetDirective(directive, interfaceName, txBytes, rxBytes)
	return nil
}

func (a *MetricsAgent) applyBudgetDirective(
	directive *panel.ServerTrafficBudgetDirective,
	interfaceName string,
	uploadTotal uint64,
	downloadTotal uint64,
) {
	if directive == nil {
		return
	}
	a.budgetMu.Lock()
	a.budgetDirective = directive
	a.budgetGeneration++
	a.baselineUpload = uploadTotal
	a.baselineDownload = downloadTotal
	a.budgetInterface = interfaceName
	if a.setSuspended != nil {
		a.setSuspended(
			directive.Enabled &&
				directive.Mode == "enforce" &&
				directive.Action == "suspend",
		)
	}
	a.budgetMu.Unlock()
}

func (a *MetricsAgent) enforceBudget(_ context.Context) error {
	a.budgetMu.Lock()
	directive := a.budgetDirective
	generation := a.budgetGeneration
	interfaceName := a.budgetInterface
	baselineUpload := a.baselineUpload
	baselineDownload := a.baselineDownload
	a.budgetMu.Unlock()
	if directive == nil ||
		!directive.Enabled ||
		directive.Mode != "enforce" ||
		directive.SuspendAtBytes == 0 ||
		directive.Action == "suspend" {
		return nil
	}

	downloadTotal, uploadTotal, err := a.readCounters(interfaceName)
	if err != nil {
		a.setGuardSuspended(generation, true)
		a.warnRateLimited(
			"budget-counter",
			"Server traffic budget counter failed; proxy traffic was suspended",
			err,
		)
		return nil
	}
	if uploadTotal < baselineUpload || downloadTotal < baselineDownload {
		a.setGuardSuspended(generation, true)
		a.warnRateLimited(
			"budget-counter-reset",
			"Server traffic counters moved backwards; proxy traffic was suspended",
			errors.New("physical network counters reset before a control-plane report"),
		)
		return nil
	}
	localUsed := directive.UsedBytes +
		(uploadTotal - baselineUpload) +
		(downloadTotal - baselineDownload)
	if localUsed >= directive.SuspendAtBytes &&
		a.setGuardSuspended(generation, true) {
		log.WithFields(log.Fields{
			"used_bytes":       localUsed,
			"suspend_at_bytes": directive.SuspendAtBytes,
		}).Error("服务器物理流量达到硬熔断阈值，已暂停代理流量")
	}
	return nil
}

func (a *MetricsAgent) setGuardSuspended(generation uint64, suspended bool) bool {
	a.budgetMu.Lock()
	defer a.budgetMu.Unlock()
	current := generation == a.budgetGeneration
	if !current || a.setSuspended == nil {
		return false
	}
	a.setSuspended(suspended)
	return true
}

func (a *MetricsAgent) warnRateLimited(key, message string, err error) {
	now := time.Now()
	a.warningMu.Lock()
	if last, ok := a.warningAt[key]; ok && now.Sub(last) < metricsWarningInterval {
		a.warningMu.Unlock()
		return
	}
	a.warningAt[key] = now
	a.warningMu.Unlock()
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
