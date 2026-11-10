# Debug Session: japan-a1-dns-errors
- **Status**: [OPEN]
- **progress_status**: analyzed
- **Issue**: 日本-a1升级后持续记录域名解析失败；需要区分无效客户端域名、IPv4-only策略回归、地址族误判和异常流量来源。
- **Evidence Source**: HTTP Debug Server (pending probe), existing ppnode journal
- **Evidence Directory**: /tmp/trae-cserver/japan-a1-dns-errors
- **Debug Server**: pending
- **Log File**: /tmp/trae-cserver/japan-a1-dns-errors/trae-debug-log-japan-a1-dns-errors.ndjson

## Reproduction Steps
1. 启动与日本-a1相同版本和配置的 ppnode。
2. 记录主机IPv4/IPv6能力、生成的DNS查询策略和Freedom出站域名策略。
3. 捕获解析失败时的目标域名、查询地址族、连接标识及来源上下文。
4. 对照v1.6.2与v1.6.3运行结果。

## Hypotheses & Verification
| ID | Hypothesis | Likelihood | Effort | Evidence |
|----|------------|------------|--------|----------|
| A | uc.cmd是客户端发起的无效域名，节点与公共DNS均只能返回NXDOMAIN | High | Low | CONFIRMED：Google DoH A/AAAA均返回Status=3 |
| B | v1.6.3在IPv4-only主机强制UseIPv4，使仅有AAAA的合法域名产生empty response | Medium | Medium | CONFIRMED：v1.6.3差异和Xray LookupForIP路径证明UseIPv4无A时返回ErrEmptyResponse |
| C | hasPublicIPv6检测误判或不稳定，使实际能力与生成策略不一致 | Medium | Low | INCONCLUSIVE：缺少日本-a1主机运行日志；本机观测结果稳定 |
| D | 异常客户端流量或扫描请求导致错误；旧版本只是日志可见性不同 | Medium | High | CONFIRMED（日志可见性部分）：4ce7c00将策略从Freedom层移动到Socket层，失败日志从Info路径转入transport Error路径 |

## Log Evidence
- `/tmp/trae-cserver/japan-a1-dns-errors/pre-fix-config.log`：pre-fix运行确认策略在Socket层生效。
- `/tmp/trae-cserver/japan-a1-dns-errors/post-fix-config.log`：post-fix运行确认默认Freedom和Socket均为AsIs，系统拨号器负责一次解析。
- 用户journal：`uc.cmd`由8.8.8.8返回`no such host`；另有Xray裸`features/dns: empty response`。
- Google DoH：`uc.cmd` A与AAAA均返回DNS Status 3（NXDOMAIN）。
- Git差异：`4ce7c00`将`FreedomConfig.DomainStrategy`迁移至`SocketConfig.DomainStrategy`；`efe9a6e`将IPv4-only节点的Socket策略改为`UseIPv4`。
- Xray源码：Socket拨号层在LookupForIP失败时调用`LogErrorInner("failed to resolve ip")`，Freedom处理层对应失败为`LogInfoInner`并允许非Force策略回退。
- 首次修复尝试被运行证据否定：当前Xray会把已弃用的`freedom.domainStrategy`自动迁移回Socket层，因此未保留该方案。
- 最终候选修复：默认Freedom不配置预解析策略（AsIs）；`go test ./...`与`go build`均通过。

## Verification Conclusion
根因不是8.8.8.8故障。客户端无效域名与IPv4-only下无A记录是触发条件；Socket层预解析把可回退结果提升为Error，是相较旧版本的日志回归。当前Xray已废弃Freedom层策略并自动迁回Socket层，因此最小修复是默认Freedom使用AsIs，由系统拨号器执行一次标准解析；节点DNS入站仍保留地址族策略。

本地post-fix已证明配置路径与构建正确。日本-a1生产复现对比尚待节点SSH通道可用后完成，调试会话保持OPEN。

## Final Evidence Gate
- **Evidence Sources**: 用户journal；Google DoH；Git提交差异；Xray运行路径源码；pre-fix测试日志
- **progress_status**: analyzed
- **Hypothesis Conclusions**: A CONFIRMED; B CONFIRMED; C INCONCLUSIVE; D CONFIRMED
