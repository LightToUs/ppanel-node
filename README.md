
### 一键安装

```bash
wget -N https://raw.githubusercontent.com/lighttous/ppanel-node/main/scripts/install.sh && bash install.sh
```

安装或升级成功后，脚本会自动检测并应用以下系统调优：

- 内核支持时启用 BBR 和 `fq`，不支持时保留当前 TCP 拥塞控制算法。
- 提升监听队列、网卡 backlog、TCP/UDP socket 缓冲区和文件句柄上限。
- 根据物理内存动态调整 conntrack 上限，且不会降低管理员已有的更高配置。
- 配置写入 `/etc/sysctl.d/99-ppanel-node.conf`，重复执行不会产生重复项。

BBR 仅优化 TCP；Hysteria2 等 UDP 协议主要受 socket 缓冲区、队列和 conntrack 调优影响。需要保留系统原始参数时，可以跳过调优：

```bash
bash install.sh --skip-system-tuning
```

首次调优前的原始值保存在 `/var/lib/ppanel-node/sysctl.original.conf`。如需回滚 PPanel-node 的 sysctl 配置：

```bash
sysctl -p /var/lib/ppanel-node/sysctl.original.conf
rm -f /etc/sysctl.d/99-ppanel-node.conf
rm -f /etc/modules-load.d/ppanel-node.conf
```

## 构建
``` bash
GOEXPERIMENT=jsonv2 go build -v -o ./node -trimpath -ldflags "-s -w -buildid="
```

## update
``` bash
git add .
git commit -m "fix: more bug fixes"
./scripts/publish-new-repo-release.sh v1.2.1
```
