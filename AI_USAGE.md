# AI 操作日志

> 记录日期：2026-10-27。按真实执行记录整理为操作日志。保留委派、命令、失败与复测；只有原始证据带时间的条目填写具体时间，其余使用顺序编号。命令省略机器绝对路径及秘密，均以仓库根目录为工作目录。未执行项不补写为成功。

## RUN-01 / 读取任务与冻结业务契约

```text
[主任务 / READ]
读取工程能力实操 V1.0 的业务边界、接口、九项验收及 AI 交付规则。
核对追加范围：MySQL、Redis、Kafka、ERP 工作区、便捷登录、复测恢复、容器交付。

[主任务 / PLAN]
后端 Go + database/sql；前端 Vue 3 + TypeScript + Ant Design Vue。
金额统一整数分；一单一次全额退款；未知结果只能查原请求。
MySQL 是真相源；Redis/Kafka 不参与资金状态判定。
保留题目固定 Bearer 兼容，但新增会话模式默认不启用它。

[主任务 / DELEGATE]
backend    -> SQL 模型、退款状态机、持久模拟渠道、幂等/越权测试。
frontend   -> ERP 界面、API 契约、身份切换、加载/失败/未知状态。
middleware -> Compose、中间件连接、outbox -> Kafka -> Redis。
主任务保留 -> 接口整合、测试库协调、真实 HTTP/浏览器验收与最终发布。
```

## RUN-02 / 建立退款闭环

```text
[backend / IMPLEMENT]
建立订单、退款、商户范围幂等键、持久渠道请求与 outbox 表。
创建退款先提交 PROCESSING，再调用渠道，再条件写入最终状态。
加入商户行锁和数据库唯一约束，渠道调用放在短事务之外。

[backend / CHECK]
SUCCESS：业务成功必须有渠道记录。
LOST_RESPONSE：先提交渠道成功，再返回超时，业务首次保持 UNKNOWN。
REJECT：明确 FAILED；UNPAID：在渠道调用前拒绝。
reconcile 只 QueryRefund 原 request_id，不重新 SubmitRefund。

[主任务 / VERIFY]
真实 HTTP 并发测试检查数据库和渠道受理数，而非只检查状态码。
16 个不同键并发：1 个创建、15 个冲突；仅一条退款和一次实际受理。
杀掉并重启实际后端/进程内模拟器后，继续查证原 UNKNOWN。
```

## RUN-03 / 登录、权限与界面竞态

```text
[frontend / IMPLEMENT]
改成独立登录面板与财务工作区，删除考题/技术说明式文案。
按服务端权限显示操作；切身份清列表、详情、动态和旧意图。

[backend / IMPLEMENT]
加入 MySQL 摘要会话、8 小时过期、轮换、退出撤销、Origin/CSRF。
未登录 401；本企业只读写入 403；跨企业对象 404。
角色与商户只从服务端身份映射获取，不接受客户端覆盖。

[frontend / REVIEW -> FIX]
检查不只发生在成功回调：迟到的 finally 也可能改变新会话加载状态。
加入请求取消、代际判定与跨标签页失效同步，补回归。

[主任务 / VERIFY]
运行真实会话 HTTP 验收；浏览器检查经办/查看员/切 B/刷新/注销。
没有把“可选择预置经办角色”写成真实人员身份核验。
```

## RUN-04 / 追加可重复初始化

```text
[主任务 / PLAN]
日常复测只恢复当前企业，不使用全库清空。
要求显式开关、data:reset、CSRF、精确确认文字、归档与恢复同事务。

[backend / IMPLEMENT]
归档六表业务快照；清理当前企业本轮记录并补回固定订单。
申请/查证/恢复全程增加本地队列 + MySQL GET_LOCK。
连接锁释放失败时丢弃物理连接，避免带锁连接回池。

[middleware + frontend / FIX]
仅清页面不够：旧 Kafka 消息和迟到 HTTP 响应可能恢复旧终态。
增加持久 watermark + data_version；过滤旧动态并清理旧缓存。

[主任务 / VERIFY]
真实双进程检查互斥、跨企业不变、失败回滚、版本递增。
重放旧 Kafka 事件，确认实际消费后仍被 API 水位过滤。
新解包/空卷环境中完成 UNKNOWN -> 查证 -> 恢复 -> 再次退款闭环。
```
## RUN-05 / 容器、安全与前端依赖收敛

```text
[主任务 / DELEGATE]
backend    -> 精确 Origin 配置、Secure Cookie 规则、字段化 DSN、超时和日志边界。
frontend   -> Web Dockerfile/Nginx、组件按需注册、公开源码检查和 CI。
middleware -> 五服务独立 Compose、随机密码、一键启动、配置负测。

[backend / IMPLEMENT]
非 loopback 监听必须显式 opt-in 并设置 Origin。
HTTPS Origin 必须 Secure；容器恢复仅额外允许 mysql:3306/refund_demo。
请求 context 15 秒；已受理收尾保留独立 8 秒边界。
驱动错误诊断不输出任意 DSN、秘密或 SQL 明文。

[frontend / IMPLEMENT]
应用镜像非 root；Nginx 安全头、API no-store、禁止自动重试写请求。
仅指纹静态资源使用长期缓存/gzip；错误和 HTML 不做 immutable。
保留原有 13 个实际使用组件，去掉未使用子组件注册。

[frontend / MEASURE]
构建模块 3157 -> 2709。
总 JS 766.22 kB -> 758.19 kB，约减少 1%。
AntD chunk 711.98 -> 703.77 kB；gzip 221.25 -> 219.33 kB。
500 kB chunk 警告仍存在，不将这次调整写成数量级性能提升。

[middleware / IMPLEMENT]
MySQL/Redis/Kafka/API 不发布宿主端口；只有 Web 发布。
应用只读根文件系统、去 capabilities、no-new-privileges。
配置私有随机密码、日志轮转、内存/进程上限，启动等待五服务健康。
```

## RUN-06 / 一键启动失败与修复

```text
[middleware / RUN]
从中文路径执行 PowerShell 一键源码构建。

[RESULT / FAIL]
Docker Buildx 0.34 / Compose 5 在构建前发生 sharedkey 非 ASCII gRPC 错误。
此时应用尚未启动；没有归因成退款代码故障，没有删除数据卷重试。

[middleware / FIX]
对受影响路径使用 ASCII 临时构建目录，仅复制明确允许的源码输入。
保留原项目位置，不改 Docker 全局配置，不复制私密 env/runtime。
配置重复使用、不覆盖已有密码；已有卷而配置丢失时拒绝生成新密码。

[主任务 / REVIEW]
继续收紧构建复制白名单与链接路径检查，不以 .dockerignore 代替源文件选择。
负测覆盖 Origin、占用端口、不覆盖配置、私密文件权限及环境隔离。

[middleware / RETEST]
独立 refund-deploy-test-* 项目、空卷、空闲 loopback 端口。
一键构建并启动五服务，健康检查通过。
继续验证重复启动保留数据和会话；后续完整容器链路单独记录。
```

## RUN-07 / 最终全量回归（18:06:12–18:07:48，UTC+8）

```powershell
$env:GOTOOLCHAIN='go1.26.10'
./scripts/test.ps1
```

```text
[PRIOR ATTEMPT / FAIL]
Windows PowerShell 5.1 将 Vite 非致命 stderr chunk 警告当成终止错误。
Go/前端已通过，但 HTTP 验收未执行完；该轮保留 FAIL，不补记成功。

[backend / RETRY]
改用 PowerShell 7，重新运行完整套件，而非只补后半段。

[RESULT / PASS]
Go：62 个顶层测试，含子测试 123 项；0 SKIP、0 FAIL。
go vet / go build：PASS。
前端：7 文件、86/86；TypeScript / Vite build：PASS。
退款 HTTP：13/13；会话 HTTP：40/40；恢复 HTTP：45/45。
总耗时：95.157 秒。
原开发 refund_demo 11 张表只读快照前后 SHA256 一致。
```

## RUN-08 / 依赖、公开边界与发布前检查

```sh
# backend 目录，Go 1.26.10
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
# web 目录
npm audit --audit-level=high
# 仓库根目录
python scripts/test_web_container.py
python scripts/test_public_release_check.py
python scripts/public_release_check.py --worktree
```

```text
[RESULT]
govulncheck：No vulnerabilities found。
npm audit：0 vulnerabilities。
Web 容器静态契约：7/7。
公开检查测试：23 PASS，2 项 Windows 创建 symlink 权限限制 SKIP。
Git index symlink、staged 秘密被工作树掩盖、未解决 merge 等检查实际通过。
工作树白名单预检通过；不能代替提交前的 Git index 检查。

[frontend / CI]
加入只读 GitHub Actions，固定官方 action 完整 SHA。
真实依赖回归、Go race/test/vet、前端与两镜像构建；无部署秘密、不推镜像。

[主任务 / PUBLICATION GATE]
原始运行配置、密钥、Cookie、截图/日志、数据归档和绝对用户路径不入库。
源码包复用公开检查返回的同一份 bytes，避免校验后重新读到变化文件。
完成全部验收后才提交 Git；镜像上传、Git 推送、服务器部署分别记状态。
```

## RUN-09 / 五容器真实验收与镜像回读

```text
[frontend / GUARD -> FIX]
首轮拓扑检查拒绝了 Kafka 官方镜像的两个匿名配置卷，尚未写业务数据。
只为两个固定目录、随机卷名、当前容器独占挂载增加精确例外。
业务数据卷仍检查项目标签，不放开任意共享卷。

[frontend / RUN]
执行 scripts/deployment_acceptance.py，只接管 refund-deploy-test-* 项目。
98 项 PASS：拓扑/权限/缓存/真实业务/恢复/事件/重启链路。
A 归档数量 1 -> 2，data_version 1 -> 2，B 不变。
160 次读取，并发 8：p50 6.823 ms，p95 19.637 ms，0 错误。
测试后保留数据和证据，不清掉现场。

[主任务 / BROWSER]
Chrome 登录容器版 ERP，实际填写 A-1002 原因并二次确认。
首次待确认；查询原请求后成功，退款号/渠道号保持一致。
定位按钮时 exact locator 未命中，读取实际页面发现可访问名称带图标；
按实际控件重新定位后继续，不把定位失败记成业务失败。
观测到的控制台 warn/error 为 0；截图私有留存。

[主任务 / PUBLISH IMAGE]
核对 56 个构建输入与工作区源码哈希一致。
将已经验证的 local 镜像标记为私仓 0.1.0 并 docker push。
backend/web 均成功；远端 imagetools inspect 和 linux/amd64 pull 回读一致。
仓库控制台显示 web 0.1.0 状态正常。
未发布 latest，未更改其他仓库，未登录部署远端服务器。
```

## RUN-10 / 交叉审查与私仓一键启动

```text
[backend / READ-ONLY REVIEW]
指出 Windows 临时配置先写密码再收紧 ACL 的读取窗口。
指出启动器 --check 与后端的 Origin 端口、HTTPS/Secure 配对不完全一致。
没有修改全局权限，没有将预检失败夸大成越权漏洞。

[middleware / FIX -> TEST]
Windows 改为创建文件时即原子应用 owner-only ACL，验证后再写密码。
同步 PowerShell/Bash Origin 与 Secure 预检，收紧源码链接/扩展白名单。
PowerShell 配置回归：32/32。
Windows PowerShell 5 -Check：PASS。
Git Bash 语法/实际配置检查：PASS。
Bash 六组错误配置全部拒绝，已有配置 hash 不变。

[主任务 / RUN]
对原独立验收项目执行 scripts/start.ps1 -Project <同一项目> -Pull。
从已发布私仓拉取并启动五服务，全部 healthy。
切换前后业务 inspect 完全相同，三笔测试退款保留。
浏览器刷新后原会话继续有效，A 的两笔成功记录仍可见。
未删除卷、未重置其他企业或原开发环境。
```

## RUN-11 / 最终源码门禁

```text
[主任务 / CHECK -> FIX]
Git index 公开检查：89 文件通过，无运行配置/原始证据入库。
git diff --cached --check 发现七个前端测试文件有多余 EOF 空行。
只清理这些尾部空行，重跑前端：86/86。
重新构建 Web 镜像并回读：运行文件系统层完全相同；
运行配置仅差 Compose 构建标签，更新构建证明与最终镜像 digest。
最终私仓拉取启动再次通过，版本仍为本次初始交付的 0.1.0。
公开源码打包与 Git index 检查分别执行，不把工作树预检代替暂存区检查。
```

## RUN-12 / Git 发布与原生 Linux CI

```text
[主任务 / PUBLISH SOURCE]
显式暂存 89 个公开源码文件，公开门禁与 whitespace 检查通过。
提交 574af53 并推送 main；远端 SHA 与本地一致。
运行配置、凭据、截图、原始验收日志均未进入 Git。

[主任务 / DELEGATE]
frontend -> 只读跟踪本次提交的 GitHub Actions，失败时回传具体步骤。
不把本机通过当作 Linux race 或服务器验收。

[frontend / OBSERVE]
Actions run 40488198137：2026-10-27 10:24:20Z -> 10:29:14Z，success。
Go 四包真实 race test / vet / build、三组 HTTP 验收均通过。
前端 86 测试通过，npm audit 0 漏洞。
Linux start.sh 从源码构建五容器，部署验收 98 PASS / 0 FAIL。
两个隔离项目的清理步骤成功；没有改源码或再次推送。
```

## RUN-13 / 远端部署与公网验收

```text
[主任务 / INSPECT]
收到明确目标后才连接 SSH；核对架构、Docker、内存、磁盘和空闲端口。
记录原有 15 个运行容器的 ID/启动时间；采用独立目录、项目、网络和数据卷。
应用使用已发布的不可变 digest，数据库密码在服务器随机生成，文件权限 0600。

[主任务 / RUN -> ADJUST]
私仓应用镜像拉取成功，Docker Hub 中间件下载过慢。
精确取消本次部署的 pull 进程，不停现有容器、不改全局代理。
middleware -> 导出本机已验收的三个中间件镜像，逐一校验 OCI blob 与 RootFS。
经 SSH 传输，服务器 SHA256 校验、docker load、原 digest/RootFS 回读均通过。
使用原 Compose 与配置执行 up -d --pull never --wait，五服务全部 healthy。

[backend / TOOLING]
准备云安全组只读查询及显式单端口操作助手，默认不写，凭据只在内存。
主任务核对实例元数据、EIP、网卡和独享安全组，确认缺少 Web 入站规则。
只新增 TCP 8080；回读原 12 条规则全部保留，只增加 1 条。
主机防火墙先备份再放行同一 Web 端口；未发布数据库或中间件端口。

[frontend / PUBLIC API]
公网 41/41：匿名、Origin/CSRF、只读与跨商户保护、退出撤销通过。
只操作虚构 A-1002：UNKNOWN -> 原号查证 SUCCEEDED，活动投影可见。
其他订单保持原样，成功记录保留，没有恢复/清库。

[主任务 / VERIFY]
公网浏览器完成登录、刷新及成功详情回读。
服务器只读 inspect：submit_calls=1、query_calls=1、effect_count=1、outbox_pending=0。
原有 15 个容器仍运行且启动时间不变；五个新服务 restart_count=0。
只公开脱敏结论；服务器地址、实例标识、截图、运行配置和凭据不入 Git。
```
