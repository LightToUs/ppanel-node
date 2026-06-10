package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/conf"
	"github.com/lighttous/ppanel-node/core"
	"github.com/lighttous/ppanel-node/limiter"
	"github.com/lighttous/ppanel-node/node"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	config         string
	watch          bool
	currentLogFile *os.File

	prepareRuntimeFn   = prepareRuntime
	startRuntimeFn     = startRuntime
	shutdownRuntimeFn  = shutdownRuntime
	rollbackRuntimeFn  = rollbackRuntime
	configureLoggingFn = configureLogging
)

var serverCommand = cobra.Command{
	Use:   "server",
	Short: "Run ppnode server",
	Run:   serverHandle,
	Args:  cobra.NoArgs,
}

func init() {
	serverCommand.PersistentFlags().
		StringVarP(&config, "config", "c",
			"/etc/PPanel-node/config.yml", "config file path")
	serverCommand.PersistentFlags().
		BoolVarP(&watch, "watch", "w",
			true, "watch file path change")
	command.AddCommand(&serverCommand)
}

func serverHandle(_ *cobra.Command, _ []string) {
	showVersion()
	c := conf.New()
	err := c.LoadFromPath(config)
	configureLogging(c)
	defer closeLogOutput()
	if err != nil {
		log.WithField("err", err).Error("读取配置文件失败")
		return
	}
	// Enable pprof if configured
	if c.PprofPort != 0 {
		go func() {
			log.Infof("Starting pprof server on :%d", c.PprofPort)
			if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", c.PprofPort), nil); err != nil {
				log.WithField("err", err).Error("pprof server failed")
			}
		}()
	}
	limiter.Init()
	var reloadCh = make(chan struct{}, 1)
	xraycore, nodes, serverconfig, err := prepareRuntime(c)
	if err != nil {
		log.WithField("err", err).Error("准备运行时失败")
		return
	}
	xraycore.ReloadCh = reloadCh
	if err = startRuntime(xraycore, nodes, serverconfig); err != nil {
		log.WithField("err", err).Error("启动节点失败")
		return
	}
	defer func() {
		if err := shutdownRuntime(nodes, xraycore); err != nil {
			log.WithField("err", err).Error("关闭运行时失败")
		}
	}()
	log.Infof("已启动 %d 个节点", serverconfig.Data.Total)
	if watch {
		// On file change, just signal reload; do not run reload concurrently here
		err = c.Watch(config, func() {
			select {
			case reloadCh <- struct{}{}:
			default: // drop if a reload is already queued
			}
		})
		if err != nil {
			log.WithField("err", err).Error("start watch failed")
			return
		}
	}
	// clear memory
	runtime.GC()

	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-osSignals:
			if err := shutdownRuntime(nodes, xraycore); err != nil {
				log.WithField("err", err).Error("关闭运行时失败")
			}
			nodes = nil
			xraycore = nil
			return
		case <-reloadCh:
			log.Info("收到重启信号，正在重新加载配置...")
			if err := reload(config, &nodes, &xraycore); err != nil {
				log.WithField("err", err).Error("重启失败")
			}
		}
	}
}

func configureLogging(c *conf.Conf) {
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: true,
		DisableQuote:     true,
		PadLevelText:     false,
	})
	log.SetLevel(log.InfoLevel)
	log.SetOutput(os.Stdout)
	if currentLogFile != nil {
		_ = currentLogFile.Close()
		currentLogFile = nil
	}
	if c == nil {
		return
	}
	switch c.LogConfig.Level {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "warn", "warning":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	default:
		log.SetLevel(log.InfoLevel)
	}
	if c.LogConfig.Output == "" {
		return
	}
	f, err := os.OpenFile(c.LogConfig.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.WithField("err", err).Error("打开日志文件失败，使用stdout替代")
		return
	}
	currentLogFile = f
	log.SetOutput(f)
}

func closeLogOutput() {
	log.SetOutput(os.Stdout)
	if currentLogFile != nil {
		_ = currentLogFile.Close()
		currentLogFile = nil
	}
}

func prepareRuntime(c *conf.Conf) (*core.XrayCore, *node.Node, *panel.ServerConfigResponse, error) {
	p := panel.NewClientV2(&c.ApiConfig)
	serverconfig, err := panel.GetServerConfig(context.Background(), p)
	if err != nil {
		return nil, nil, nil, err
	}
	if serverconfig == nil {
		return nil, nil, nil, fmt.Errorf("服务端配置为空")
	}
	xraycore := core.New(c, p)
	nodes, err := node.New(xraycore, c, serverconfig)
	if err != nil {
		return nil, nil, nil, err
	}
	return xraycore, nodes, serverconfig, nil
}

func startRuntime(xraycore *core.XrayCore, nodes *node.Node, serverconfig *panel.ServerConfigResponse) error {
	if err := xraycore.Start(serverconfig); err != nil {
		return err
	}
	if err := nodes.Start(); err != nil {
		_ = shutdownRuntime(nodes, xraycore)
		return err
	}
	return nil
}

func shutdownRuntime(nodes *node.Node, xraycore *core.XrayCore) error {
	var shutdownErr error
	if nodes != nil {
		shutdownErr = errors.Join(shutdownErr, nodes.Close())
	}
	if xraycore != nil {
		shutdownErr = errors.Join(shutdownErr, xraycore.Close())
	}
	return shutdownErr
}

func rollbackRuntime(c *conf.Conf, serverconfig *panel.ServerConfigResponse, reloadCh chan struct{}) (*core.XrayCore, *node.Node, error) {
	if c == nil || serverconfig == nil {
		return nil, nil, fmt.Errorf("旧实例上下文不可用")
	}
	xraycore := core.New(c, panel.NewClientV2(&c.ApiConfig))
	xraycore.ReloadCh = reloadCh
	nodes, err := node.New(xraycore, c, serverconfig)
	if err != nil {
		return nil, nil, err
	}
	if err := startRuntime(xraycore, nodes, serverconfig); err != nil {
		return nil, nil, err
	}
	return xraycore, nodes, nil
}

func reload(config string, nodes **node.Node, xcore **core.XrayCore) error {
	if nodes == nil || xcore == nil || *nodes == nil || *xcore == nil {
		return fmt.Errorf("当前运行时未初始化")
	}
	newConf := conf.New()
	if err := newConf.LoadFromPath(config); err != nil {
		return err
	}
	newCore, newNodes, serverconfig, err := prepareRuntimeFn(newConf)
	if err != nil {
		return err
	}
	oldCore := *xcore
	oldNodes := *nodes
	oldConf := oldCore.Config
	oldServerConfig := oldCore.ServerConfig
	oldReloadCh := oldCore.ReloadCh
	newCore.ReloadCh = oldReloadCh
	shutdownErr := shutdownRuntimeFn(oldNodes, oldCore)
	if shutdownErr != nil {
		log.WithField("err", shutdownErr).Warn("关闭旧实例时存在错误，继续尝试启动新实例")
	}
	if err := startRuntimeFn(newCore, newNodes, serverconfig); err != nil {
		log.WithField("err", err).Error("新实例启动失败，尝试回滚旧实例")
		_ = shutdownRuntimeFn(newNodes, newCore)
		rollbackCore, rollbackNodes, rollbackErr := rollbackRuntimeFn(oldConf, oldServerConfig, oldReloadCh)
		if rollbackErr != nil {
			if shutdownErr != nil {
				return fmt.Errorf("关闭旧实例失败: %v; 启动新实例失败: %w; 回滚旧实例失败: %v", shutdownErr, err, rollbackErr)
			}
			return fmt.Errorf("启动新实例失败: %w; 回滚旧实例失败: %v", err, rollbackErr)
		}
		*nodes = rollbackNodes
		*xcore = rollbackCore
		if shutdownErr != nil {
			return fmt.Errorf("关闭旧实例失败: %v; 启动新实例失败，已回滚旧实例: %w", shutdownErr, err)
		}
		return fmt.Errorf("启动新实例失败，已回滚旧实例: %w", err)
	}
	*nodes = newNodes
	*xcore = newCore
	configureLoggingFn(newConf)
	if shutdownErr != nil {
		log.WithField("err", shutdownErr).Warn("旧实例关闭过程中存在错误，但新实例已成功接管")
	}
	log.Infof("%d 个节点重启成功", serverconfig.Data.Total)
	runtime.GC()
	return nil
}
