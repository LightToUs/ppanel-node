package node

import (
	"bufio"
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
	gnet "github.com/shirou/gopsutil/v3/net"
	log "github.com/sirupsen/logrus"
)

const metricsInterval = 10 * time.Second
const budgetGuardInterval = time.Second
const metricsFreshnessLimit = 3 * metricsInterval
const metricsWarningInterval = 5 * time.Minute
const defaultTrafficSuspensionFile = "/var/lib/PPanel-node/traffic-suspended"
const defaultTrafficBudgetFile = "/var/lib/PPanel-node/traffic-budget.json"

type MetricsAgent struct {
	client             *panel.ClientV1
	core               *vCore.XrayCore
	interfaceOverride  string
	interfaceName      string
	sessionID          string
	sequence           uint64
	reportTask         *task.Task
	guardTask          *task.Task
	access             sync.Mutex
	budgetMu           sync.Mutex
	budgetDirective    *panel.ServerTrafficBudgetDirective
	baselineUpload     uint64
	baselineDownload   uint64
	budgetInterface    string
	lastBudgetReportAt time.Time
	warningAt          map[string]time.Time
	warningMu          sync.Mutex
	unsupported        bool
	suspensionFirewall *trafficSuspensionFirewall
	suspensionMu       sync.Mutex
	certificateMu      sync.RWMutex
	certificateSource  func() []panel.CertificateStatusReport
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
		sessionID:         uuid.NewString(),
		warningAt:         make(map[string]time.Time),
	}
	persistedDirective, loadErr := loadTrafficBudgetDirective()
	if loadErr != nil && !os.IsNotExist(loadErr) {
		log.WithError(loadErr).Error("读取持久化流量预算失败，托管实例将保持熔断")
	}
	if persistedDirective != nil {
		agent.budgetDirective = persistedDirective
	}
	if (client != nil && client.ManagedInstanceID > 0) ||
		persistedDirective != nil ||
		(loadErr != nil && !os.IsNotExist(loadErr)) ||
		trafficSuspensionPersisted() {
		agent.setTrafficSuspended(true)
	}
	return agent
}

func (a *MetricsAgent) SetBusinessProtocols(protocols []panel.Protocol) {
	if a == nil {
		return
	}
	a.budgetMu.Lock()
	a.suspensionFirewall = newTrafficSuspensionFirewall(protocols)
	firewall := a.suspensionFirewall
	a.budgetMu.Unlock()
	a.suspensionMu.Lock()
	defer a.suspensionMu.Unlock()
	if trafficSuspensionPersisted() {
		if err := firewall.Apply(); err != nil {
			log.WithError(err).Error("恢复流量熔断防火墙失败，应用层仍保持暂停")
		}
	}
}

func (a *MetricsAgent) SetCertificateSource(
	source func() []panel.CertificateStatusReport,
) {
	if a == nil {
		return
	}
	a.certificateMu.Lock()
	a.certificateSource = source
	a.certificateMu.Unlock()
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
	interfaceName, err := a.resolveInterface()
	if err != nil {
		a.suspendManagedTrafficOnMetricsFailure(err)
		a.warnRateLimited("resolve-interface", "Resolve metrics network interface failed", err)
		return nil
	}
	rxBytes, txBytes, err := readNetworkCounters(interfaceName)
	if err != nil {
		a.interfaceName = ""
		a.suspendManagedTrafficOnMetricsFailure(err)
		a.warnRateLimited("read-interface", "Read metrics network counters failed", err)
		return nil
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
		Certificates:   a.certificateReports(),
	})
	if err != nil {
		if errors.Is(err, panel.ErrMetricsUnsupported) {
			a.unsupported = true
			a.suspendManagedTrafficOnMetricsFailure(err)
			log.Info("控制面未启用实时指标接口，已停止本进程的指标上报")
			return nil
		}
		a.suspendManagedTrafficOnMetricsFailure(err)
		a.warnRateLimited("report", "Report server realtime metrics failed", err)
		return nil
	}
	a.applyBudgetDirective(directive, interfaceName, txBytes, rxBytes)
	// Metrics are best effort and must never stop the periodic task or proxy runtime.
	return nil
}

func (a *MetricsAgent) suspendManagedTrafficOnMetricsFailure(err error) {
	if a == nil ||
		a.client == nil ||
		a.client.ManagedInstanceID <= 0 ||
		err == nil {
		return
	}
	a.setTrafficSuspended(true)
}

func (a *MetricsAgent) certificateReports() []panel.CertificateStatusReport {
	a.certificateMu.RLock()
	source := a.certificateSource
	a.certificateMu.RUnlock()
	if source == nil {
		return nil
	}
	return source()
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
	if err := persistTrafficBudgetDirective(directive); err != nil {
		a.setTrafficSuspended(true)
		log.WithError(err).Error("持久化流量预算失败，代理流量已暂停")
		return
	}
	a.budgetMu.Lock()
	a.budgetDirective = directive
	a.baselineUpload = uploadTotal
	a.baselineDownload = downloadTotal
	a.budgetInterface = interfaceName
	a.lastBudgetReportAt = time.Now()
	a.budgetMu.Unlock()

	a.setTrafficSuspended(
		directive.Enabled &&
			directive.Mode == "enforce" &&
			directive.Action == "suspend",
	)
}

func (a *MetricsAgent) enforceBudget(_ context.Context) error {
	a.budgetMu.Lock()
	directive := a.budgetDirective
	interfaceName := a.budgetInterface
	baselineUpload := a.baselineUpload
	baselineDownload := a.baselineDownload
	lastBudgetReportAt := a.lastBudgetReportAt
	a.budgetMu.Unlock()
	if a.client != nil &&
		a.client.ManagedInstanceID > 0 &&
		(lastBudgetReportAt.IsZero() ||
			time.Since(lastBudgetReportAt) > metricsFreshnessLimit) {
		a.setTrafficSuspended(true)
		a.warnRateLimited(
			"budget-stale",
			"Server traffic budget became stale; proxy traffic was suspended",
			errors.New("no fresh control-plane traffic budget"),
		)
		return nil
	}
	if directive == nil ||
		!directive.Enabled ||
		directive.Mode != "enforce" ||
		directive.SuspendAtBytes == 0 ||
		directive.Action == "suspend" {
		return nil
	}

	downloadTotal, uploadTotal, err := readNetworkCounters(interfaceName)
	if err != nil {
		a.setTrafficSuspended(true)
		a.warnRateLimited(
			"budget-counter",
			"Server traffic budget counter failed; proxy traffic was suspended",
			err,
		)
		return nil
	}
	if uploadTotal < baselineUpload || downloadTotal < baselineDownload {
		a.setTrafficSuspended(true)
		a.warnRateLimited(
			"budget-counter-reset",
			"Server traffic counters moved backwards; proxy traffic was suspended",
			errors.New("physical network counters reset before a control-plane report"),
		)
		return nil
	}
	localUsed, err := localBudgetUsage(
		directive.UsedBytes,
		uploadTotal-baselineUpload,
		downloadTotal-baselineDownload,
	)
	if err != nil {
		a.setTrafficSuspended(true)
		a.warnRateLimited(
			"budget-overflow",
			"Server traffic budget overflowed; proxy traffic was suspended",
			err,
		)
		return nil
	}
	if localUsed >= directive.SuspendAtBytes {
		a.setTrafficSuspended(true)
		log.WithFields(log.Fields{
			"used_bytes":       localUsed,
			"suspend_at_bytes": directive.SuspendAtBytes,
		}).Error("服务器物理流量达到硬熔断阈值，已暂停代理流量")
	}
	return nil
}

func localBudgetUsage(
	reported uint64,
	uploadDelta uint64,
	downloadDelta uint64,
) (uint64, error) {
	if uploadDelta > math.MaxUint64-reported {
		return 0, errors.New("server traffic upload exceeds supported range")
	}
	used := reported + uploadDelta
	if downloadDelta > math.MaxUint64-used {
		return 0, errors.New("server traffic download exceeds supported range")
	}
	return used + downloadDelta, nil
}

func (a *MetricsAgent) setTrafficSuspended(suspended bool) {
	if a == nil {
		return
	}
	a.suspensionMu.Lock()
	defer a.suspensionMu.Unlock()

	path := trafficSuspensionFile()
	if suspended {
		if a.core != nil {
			a.core.SetTrafficSuspended(true)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			log.WithError(err).Error("创建流量熔断状态目录失败")
		} else {
			tmp := path + ".tmp"
			if err := os.WriteFile(tmp, []byte("suspended\n"), 0o600); err != nil {
				log.WithError(err).Error("写入流量熔断状态失败")
			} else if err = os.Rename(tmp, path); err != nil {
				_ = os.Remove(tmp)
				log.WithError(err).Error("提交流量熔断状态失败")
			}
		}
		a.budgetMu.Lock()
		firewall := a.suspensionFirewall
		a.budgetMu.Unlock()
		if firewall != nil {
			if err := firewall.Apply(); err != nil {
				log.WithError(err).Error("应用流量熔断防火墙失败，应用层仍保持暂停")
			}
		}
		return
	}

	a.budgetMu.Lock()
	firewall := a.suspensionFirewall
	a.budgetMu.Unlock()
	if firewall != nil {
		if err := firewall.Close(); err != nil {
			log.WithError(err).Warn("清除流量熔断防火墙失败，继续保持暂停")
			return
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.WithError(err).Warn("清除流量熔断状态失败，继续保持暂停")
		return
	}
	if a.core != nil {
		a.core.SetTrafficSuspended(false)
	}
}

func trafficSuspensionPersisted() bool {
	_, err := os.Stat(trafficSuspensionFile())
	return err == nil
}

func trafficSuspensionFile() string {
	if value := strings.TrimSpace(os.Getenv("PPANEL_TRAFFIC_SUSPENSION_FILE")); value != "" {
		return value
	}
	return defaultTrafficSuspensionFile
}

func persistTrafficBudgetDirective(
	directive *panel.ServerTrafficBudgetDirective,
) error {
	path := trafficBudgetFile()
	if directive == nil || !directive.Enabled || directive.Mode != "enforce" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove persisted traffic budget: %w", err)
		}
		return nil
	}
	if directive.SuspendAtBytes == 0 {
		return errors.New("enforced traffic budget has no suspension threshold")
	}
	payload, err := json.Marshal(directive)
	if err != nil {
		return fmt.Errorf("encode traffic budget: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create traffic budget directory: %w", err)
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, payload, 0o600); err != nil {
		return fmt.Errorf("write traffic budget: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit traffic budget: %w", err)
	}
	return nil
}

func loadTrafficBudgetDirective() (*panel.ServerTrafficBudgetDirective, error) {
	payload, err := os.ReadFile(trafficBudgetFile())
	if err != nil {
		return nil, err
	}
	var directive panel.ServerTrafficBudgetDirective
	if err = json.Unmarshal(payload, &directive); err != nil {
		return nil, fmt.Errorf("decode persisted traffic budget: %w", err)
	}
	if !directive.Enabled ||
		directive.Mode != "enforce" ||
		directive.SuspendAtBytes == 0 {
		return nil, errors.New("persisted traffic budget is not enforceable")
	}
	return &directive, nil
}

func trafficBudgetFile() string {
	if value := strings.TrimSpace(os.Getenv("PPANEL_TRAFFIC_BUDGET_FILE")); value != "" {
		return value
	}
	return defaultTrafficBudgetFile
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
