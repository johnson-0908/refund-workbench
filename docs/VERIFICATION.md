# 可复现验证记录

日期：2026-10-27。范围为虚构订单、本地持久模拟渠道、独立测试库/容器，不代表招聘方签收、生产资金认证或远端服务器验收。命令与原始结果保留在本地 ignored `evidence/` / `.runtime/`，公开仓库仅收录脱敏摘要。

## 1. 最终核心回归

Windows / PowerShell 7，Go 1.26.10，真实 Docker MySQL / Redis / Kafka；UTC 10:06:12–10:07:48，耗时 95.157 秒。

```powershell
$env:GOTOOLCHAIN='go1.26.10'
./scripts/test.ps1
```

|检查|实际结果|
|---|---|
|Go 真实依赖测试|62 个顶层 PASS，包含子测试 123 PASS，0 SKIP / FAIL|
|`go vet ./...` / `go build ./...`|通过|
|前端单测|7 文件，86/86 通过，0 SKIP|
|TypeScript / Vite 生产构建|通过；AntD 超过 500 kB 的 chunk 警告保留|
|退款真实 HTTP / 进程验收|13/13|
|会话与权限真实 HTTP 验收|40/40|
|恢复初始化 / 双进程 / 旧事件验收|45/45|
|原开发数据保护|`refund_demo` 11 表只读一致性快照前后 SHA256 相同|

证据文件：`final-regression.json`、`validation.json`、`go-tests.txt`、`web-tests.txt`、`acceptance.json`、`auth-acceptance.json`、`reset-acceptance.json`（均在本地 `evidence/`）。首轮 Windows PowerShell 5.1 将 Vite 的非致命 stderr 警告误判为终止异常，HTTP 验收未执行完；保留 FAIL 记录，随后用 PowerShell 7 完整重跑。不能将首轮记为通过。

## 2. 九项实操验收对应关系

|要求|实现与可复现证据|
|---|---|
|正常退款|A-1001 成功、19900 分，业务与持久渠道记录一致；退款 HTTP 套件|
|重复/异参|同键同参返回原单当前状态，同键改原因/订单 409；不增加渠道受理|
|至少 10 个并发/换键|16 个不同键同时提交，1 个创建、15 个冲突；一退款、一渠道记录、effect_count=1，无未处理 500|
|未知结果|A-1002 渠道先受理后丢响应，业务 UNKNOWN；只按原请求查证成功|
|重启恢复|实际终止并重启后端及进程内模拟器；原请求号、submit_calls=1 保持不变，查询收敛|
|失败/未支付|A-1003 明确 FAILED 且不可重提；A-1004 拒绝、不调用渠道|
|权限/商户|匿名 401，本企业只读写 403，跨企业读写 404；覆盖订单/退款/动态/恢复和伪造字段|
|前端/身份切换|86 项前端测试含加载/空态/错误/处理中及迟到响应；实际浏览器已验证切换、刷新、注销和恢复闭环|
|干净启动|早期独立 ZIP 解包/空卷执行过；当前版本增加全容器一键启动验收，详见下面容器记录|

原题固定 Bearer 仅在隔离评测显式设置 `ENABLE_DEMO_BEARER=true`，正常浏览器模式禁用；额外登录、Redis/Kafka、ERP UI 与恢复是追加需求，不改变原题资金约束。

## 3. 追加维护与边界检查

- 恢复默认关闭，必须显式启用、经办权限、CSRF/Origin 和精确确认文字；六表归档与清理/重建同事务。
- 当前企业恢复保留其他企业及会话；归档失败/中途 SQL 失败完整回滚；跨进程申请/查证/恢复互斥。
- 持久 watermark 过滤已重新消费到 Redis 的旧 Kafka 事件；前端 data_version 防迟到响应恢复旧终态。
- 精确 Origin 替换默认列表；非 loopback 监听要求显式 opt-in；HTTPS 必须 Secure Cookie；不信任任意转发头。
- 字段化 MySQL 配置覆盖特殊字符密码；日志不回显任意驱动秘密/DSN；15 秒请求 context 与有界已受理收尾分别验证。

## 4. 依赖、容器静态检查与公开门禁

```sh
# backend 下使用 Go 1.26.10
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
# web 下
npm audit --audit-level=high
# 仓库根目录
python scripts/test_web_container.py
python scripts/test_public_release_check.py
python scripts/public_release_check.py --worktree
```

- govulncheck：`No vulnerabilities found`；npm audit：0 vulnerabilities。为该时点/依赖范围结果，不等于 OS 镜像漏洞全覆盖或安全认证。
- Web Docker/Nginx 静态契约：7/7。
- 公开门禁测试：23 PASS，2 项 Windows 实际创建 symlink 因权限 SKIP；Git index symlink 拒绝、staged 秘密与工作树差异等测试实际通过。
- 门禁默认读 Git index blob；未建仓库时拒绝，不自动退回工作树。`--worktree` 只检查闭集源码预览，最终提交必须另跑 index 检查。
- 依赖/基础镜像锁定；CI 官方 actions 使用完整 SHA、`contents: read`、不持有部署秘密。云端 CI 是否运行通过须另以真实结果确认。

## 5. 性能口径

前端按实际使用组件收敛注册：构建模块 3157 → 2709，总 JS 766.22 → 758.19 kB（约 1%）；AntD chunk 711.98 → 703.77 kB，gzip 221.25 → 219.33 kB。所有 86 个前端测试通过。没有将小幅体积变化表述为显著业务吞吐提升。

静态指纹资源独立长期缓存/gzip，HTML/API/错误响应 no-store；API 不压缩会话敏感响应、不自动重试写操作。MySQL 使用有界连接池；同商户资金写串行化是正确性取舍，不因压测而取消。容器读请求基线将单独列出实际样本、并发与分位数，不外推生产 SLA。

## 6. 仍然明确保留的边界

1. 预置角色免密选择不是人员身份核验；HTTP IP:端口没有 TLS。可访问页面者可以选择经办角色，测试部署必须限制可信来源。
2. 五笔虚构订单、模拟渠道、单节点 Compose/单 outbox worker；没有真实支付、SSO、HA、生产负载测试或正式渗透测试。
3. PROCESSING 落库后、提交渠道前退出的窗口可能长期 UNKNOWN；不擅自补发。
4. Redis 动态最新 100 条，去重索引无自动过期；Kafka 保留 7 天，不是永久审计存储。
5. 本机普通 Go 并发测试不是 race 检测；云端 CI 的 `-race` 在实际通过前不计入已完成结果。
6. 本地空卷验证不等于全新操作系统，Docker 缓存与宿主环境可复用；镜像发布不等于已经部署到用户远端服务器。

## 7. 五容器运行与浏览器复核

独立 `refund-deploy-test-*` 项目，空卷/随机 loopback 端口，源码一键启动五服务通过。重复启动的 10 项检查验证原配置字节、会话、退款请求号、submit/effect=1 保留；随后仅当前企业通过 API 归档恢复。

```sh
# 使用新的、未承载业务的独立验收项目；请选空闲端口。
bash scripts/start.sh --project refund-deploy-test-local --port 28080
python scripts/deployment_acceptance.py --project refund-deploy-test-local --env-file .runtime/start-refund-deploy-test-local.env --base-url http://127.0.0.1:28080
```

`deployment_acceptance.py` 实际 **98 项通过**，含：项目/卷/网络归属、非 root/只读/capability、仅 Web 端口映射、Nginx 安全头/no-store/指纹缓存/gzip、登录/权限/CSRF/伪造字段、10 并发同键仅一次受理、UNKNOWN 原号查证、明确拒绝、未支付、真实 outbox→Kafka→Redis、当前企业归档恢复及 B 不变、旧活动过滤、只重启自身 backend 后会话和退款保留。

测试前按固定五单、空退款/渠道/幂等/outbox 检查；已有归档/版本允许按基线比较，**不会自动全库重置**。测试后保留证据数据，再次完整执行请用新的项目/空卷，不能对现有用户业务库执行。

有界读请求基线：经 Nginx `GET /api/orders`，5 笔固定订单（A 4/B 1），160 次、并发 8，nearest-rank **p50 6.823 ms / p95 19.637 ms / max 27.067 ms**，请求段 0.175 秒，0 HTTP/传输错误。前置限流窗口等待 1.149 秒不计入请求耗时。是本机 Docker Desktop 的小样本，不是服务器/生产 SLA，也不是资金写入吞吐。

主任务随后用 Chrome 对当前容器构建实际操作 A-1002：填写原因、二次确认、UNKNOWN→原请求查证成功；前后退款号/渠道号保持一致，观测到的控制台 warn/error 为 0。截图和四项记录仅保存本地，不随公开源码分发。

已发现并处理的环境问题：中文构建路径触发 Buildx sharedkey 非 ASCII 错误，改为受控源码暂存；官方 Kafka 镜像声明两个匿名配置卷，验收只对精确目录/随机卷名/当前容器独占挂载的组合放行，业务数据卷仍强制项目归属。不是为了测试通过放开任意卷。

## 8. 镜像发布回读（0.1.0）

两个仓库均位于广州 `pandacount` 命名空间，**私有**，已 push，远端 `imagetools inspect` 与 `docker pull --platform linux/amd64` 回读成功。基础镜像及 56 个应用构建输入与当前源码逐一核对一致。

|镜像|OCI index digest|
|---|---|
|`crpi-vpvynwydljgcai3w.cn-guangzhou.personal.cr.aliyuncs.com/pandacount/refund-workbench-backend:0.1.0`|`sha256:ab01c18d6aa32f4c2d50dea3fbe6914ec8e3cfb06b3a6f293d616d7890ee2578`|
|`crpi-vpvynwydljgcai3w.cn-guangzhou.personal.cr.aliyuncs.com/pandacount/refund-workbench-web:0.1.0`|`sha256:b1b5b807f15152926961a623ed2eaba67096bd6fa0f1e9074dd7b71a801fb7dd`|

发布应用平台为 **linux/amd64**；manifest 中额外的 unknown/unknown 是 BuildKit attestation，不代表 ARM 支持。需要不可变拉取时可使用上表的 `镜像@sha256:...`。此阶段仅验证镜像发布；随后经明确授权执行的服务器验收见下一节，公网地址不写入公开仓库。

随后对同一独立项目实际执行 `scripts/start.ps1 -Project <同一项目> -Pull`：五服务健康、切换前后业务 inspect 完全相同（三笔测试退款），浏览器刷新后原会话与记录保持。说明已验证的是私仓镜像运行，而不仅是本地 build tag。

启动器最后安全回归：PowerShell 32/32（含创建时原子 owner-only ACL、失败不写密码、源码白名单和 junction 拒绝）；Windows PowerShell 5 的 `-Check` 通过；Git Bash 语法和实际配置检查通过，六组 saved-env 错误配置均拒绝且文件 hash 不变。Git Bash 检查不冒充原生 Linux 全栈验证；原生 Linux 的一键构建/98 项验收由仓库 Actions 在隔离 runner 执行，结果以对应运行记录为准。

## 9. 原生 Linux CI 与授权服务器验收

提交 `574af53` 的 [Actions 40488198137](https://github.com/johnson-0908/refund-workbench/actions/runs/40488198137) 已完成 success：Go 四包 race test / vet / build、三组 HTTP 验收、前端 86 测试及 audit、Linux 一键源码构建和 98 项容器验收均通过；两个隔离项目清理成功。此次运行耗时 4 分 54 秒，不作为生产容量指标。

随后在明确授权的 Linux amd64 服务器部署相同不可变应用镜像。Docker Hub 下载慢时，改为 SSH 传输本机已验收中间件的 OCI 镜像归档；48 个 blob、归档 SHA256、原 index digest 与 RootFS 逐一核对，不替换镜像版本。五服务全部 healthy，仅 Web 映射宿主机端口，应用非 root/只读，配置权限 0600。原有 15 个容器的 ID 与启动时间保持不变。

公网 API **41/41 通过**，覆盖匿名/来源/CSRF、三角色、企业隔离、退出撤销及虚构 A-1002 的 UNKNOWN→原请求查证成功。仅该测试订单产生退款，其他订单不变；服务器只读核对 `submit_calls=1`、`query_calls=1`、`effect_count=1`、`outbox_pending=0`。公网浏览器完成登录和成功详情回读。原始证据保存在私有 `evidence/`，不公开地址、实例标识、凭据或截图。

公网仍为预置身份、HTTP、模拟资金的体验部署，不代表生产身份核验、TLS、HA、容量压测或真实支付验收。页面当前企业恢复可用于下一轮复测；未为交付自动清空已经验证的成功记录。
