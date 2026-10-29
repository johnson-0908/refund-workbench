# 易数 ERP · 财务管理

面向企业财务操作的退款工作台：Go、Vue 3 / TypeScript、Ant Design Vue，使用 MySQL 8.4、Redis、Kafka。[工程能力实操 V1.0](https://www.yishuld.com/recruitment/fullstack/#ai)的退款约束保持不变，增加登录工作区、租户/角色隔离和可重复验证的数据恢复。

## 一键启动

仅需已启动的 Docker Engine / Desktop 和 Docker Compose v2+；不需要在宿主机安装 Go、Node、Java 或数据库。建议 Docker 至少有 4 GiB 可用内存。

锁定构建环境：Go 1.26.10、Node 24.22.1、Nginx 1.30.6；MySQL 8.4、Redis 7.4、Kafka 4.1.1。基础镜像同时固定 digest，应用依赖由 `go.sum` / `package-lock.json` 锁定。

```powershell
# Windows（推荐 PowerShell 7）
./scripts/start.ps1
```

**Mac：双击项目根目录的 `start.command`**，自动打开 Docker Desktop，服务就绪后打开浏览器；无需 Homebrew 或另外安装 Bash。首次使用需先安装 Docker Desktop。也可在项目目录执行：

```sh
./start.command
```

```sh
# Linux / macOS（Bash 3.2+）
bash scripts/start.sh
```

打开 **http://127.0.0.1:8080**。脚本在容器内构建前后端，一次启动全部五个服务并等待健康检查。初次拉取/构建需要网络，不需要私有镜像仓库账号。

- 默认项目 `yishuld-refund-app`，与开发/验收项目隔离；只有 Web 发布宿主端口。
- 首次自动生成两份独立随机数据库密码，保存在私有 `.runtime/start-<项目>.env`；重复启动沿用配置和数据。
- 端口冲突时报错，不结束其他进程；可使用 `-Project refund-local-18088 -Port 18088` 或 `--project refund-local-18088 --port 18088`。
- 不要删除配置或数据卷来“重新启动”。启动不清空业务；页面的恢复功能才执行显式、当前企业范围的初始化。
- 只检查配置：`./scripts/start.ps1 -Check` / `bash scripts/start.sh --check`。

### 服务器 IP + 端口

[compose.deploy.yaml](compose.deploy.yaml) 包含前端、后端和全部中间件，无外部源码/SQL/Nginx 挂载，支持单独复制部署。私有镜像先交互登录，再一键拉取启动：

```sh
docker login crpi-vpvynwydljgcai3w.cn-guangzhou.personal.cr.aliyuncs.com
bash scripts/start.sh --project refund-server --host 0.0.0.0 --port 8080 --origin http://实际服务器IP:8080 --pull
```

Windows 对应 `./scripts/start.ps1 -Project refund-server -Host 0.0.0.0 -Port 8080 -Origin http://实际服务器IP:8080 -Pull`。未加 Pull 则从源码构建。Origin 必须与浏览器地址完全一致，不带路径或末尾斜杠；具体 IP 由部署者填写。详细配置、停止/升级、备份边界见 [deploy/README.md](deploy/README.md)。

> 选择预置身份是便捷的虚构业务体验入口，不是生产身份核验。任何能访问页面的人都能选择经办角色，HTTP IP 访问也不加密会话。服务器体验应限制可信来源；真实业务上线须接入 IdP/SSO、人员授权、TLS 和运维保障。本项目没有连接真实资金。

## 登录与业务

|登录身份|企业|权限|
|---|---|---|
|财务经办 / a-write|华东商贸（A）|查询、申请、查证；维护开启时可恢复|
|财务查看 / a-read|华东商贸（A）|仅查询|
|财务经办 / b-write|华南商贸（B）|查询、申请、查证；维护开启时可恢复|

先登录再获取业务数据。后端签发 8 小时 HttpOnly / SameSite Cookie，MySQL 只存会话摘要，支持过期、轮换、退出撤销与重启恢复。所有写请求校验精确 Origin + CSRF；商户和权限由后端确定。切企业、注销、失效会清空旧状态，取消请求并废弃迟到响应，标签页间同步失效。

|订单|金额|复测流程|
|---|---:|---|
|A-1001|¥199.00|申请后立即成功|
|A-1002|¥88.00|先显示待确认 UNKNOWN，再查询原退款得到成功|
|A-1003|¥56.00|渠道明确拒绝；失败后也不能申请第二笔|
|A-1004|¥129.00|未支付，不能退款、不调用渠道|
|B-2001|¥256.00|只有 B 可见；A 访问其对象返回 404|

退款只能一次全额，金额为整数分，不接受客户端改金额/商户。相同幂等键同参数返回原退款当前状态，异参 409；更换键也不能重复退款。网络异常不是失败，保留原意图先查询；UNKNOWN 只能查原请求，不能补发。MySQL 是业务真相，Redis/Kafka 动态是最终一致的辅助信息。

### 恢复初始数据

一键脚本显式启用复测维护能力。经办点击 **恢复初始数据**，输入同名确认文字；只恢复当前企业的固定订单，清理其本轮退款/幂等/模拟渠道记录，保留其他企业和登录会话。恢复前业务快照归档与清理/重建同一事务提交；失败完整回滚。

恢复与申请/查证全程互斥；前端数据版本阻止旧响应覆盖，持久水位过滤迟到的旧 Kafka/Redis 动态。断网需刷新确认，不以弹窗消失判断成功。归档存于 MySQL `maintenance_resets`，当前没有恢复历史归档的 UI，不是生产备份系统。

Go 服务本身默认关闭维护；一键配置可将 `ENABLE_DATA_RESET=false` 后重新启动。容器额外通过 `ALLOW_DOCKER_DEMO_RESET` 和精确数据库白名单限制，见 [安全说明](docs/SECURITY.md)。

## 设计与文档

- [DESIGN.md](DESIGN.md)：状态机、幂等/事务、故障窗口、事件与维护设计。
- [docs/API.md](docs/API.md)：接口、错误码、配置、固定数据。
- [docs/SECURITY.md](docs/SECURITY.md)：会话、越权、CSRF、容器与已知风险。
- [docs/VERIFICATION.md](docs/VERIFICATION.md)：脱敏验证摘要、性能口径与未覆盖边界。
- [docs/TEST_EXECUTION_2026-10-29.md](docs/TEST_EXECUTION_2026-10-29.md)：真实自动验收的公开脱敏日志，含逐项结果、耗时及 SKIP。
- [AI_USAGE.md](AI_USAGE.md)：真实 AI 分工、调整过程与三项关键决策。
- [AGENTS.md](AGENTS.md)：维护者/agent 的工程约束与发布门禁。

## 开发与测试

### 一键自动验收（推荐复测入口）

先启动 Docker，宿主只需 Python 3.11+、Git 和 PowerShell 7（Windows）/ Bash 4+（Linux），不需要宿主 Go、Node 或手动启动中间件：

```powershell
python scripts/verify.py
# Windows 也可：./scripts/verify.ps1
```

脚本自动冻结本轮源码、随机生成隔离项目/空卷/空闲 loopback 端口，构建并启动前端、后端、MySQL、Redis、Kafka，执行公开清单对应的 HTTP/SQL 业务断言、16 个不同键并发、UNKNOWN 时重启、权限、恢复和事件链路，以及真实依赖 Go 测试、前端组件测试、类型检查和构建。

完成后打开打印的 `evidence/checklist-<时间-随机值>/report.html`，也有 JSON/Markdown 和分阶段日志。失败返回非零退出码；未执行项明确标记 NOT_RUN，Go/前端 SKIP 不计通过。前端采用 jsdom/模拟 API，不冒充公网真实浏览器 E2E。

**每次使用新项目，不接受公网 URL，不重置已有演示/服务器数据库。** 成功后只停止本轮容器，保留配置、卷与证据；失败保留现场。`python scripts/verify.py --keep-running` 可保留本轮测试站点供手动查看。重复运行会增加本地测试卷，请按报告项目名人工管理，禁止全局 prune。完整范围、手动 UI 流程和诊断见 [自动验收说明](docs/AUTOMATED_ACCEPTANCE.md)。

### 开发环境的完整回归

需要热更新时才使用以下开发模式（宿主需 Go / Node / Python / PowerShell，版本见 Dockerfile 与锁文件）。它与 8080 全容器模式独立，不要把两套数据库混为一谈：

```powershell
./scripts/setup.ps1   # 开发专用中间件、依赖和非破坏性 seed
./scripts/dev.ps1     # API 18080 / Vite 5173，后台进程归本项目管理
./scripts/test.ps1    # 真实依赖、Go、前端、三套 HTTP 验收
./scripts/stop-dev.ps1
```

开发 Compose 为 `yishuld-refund`，仅 loopback 发布 MySQL 3307 / Redis 6379 / Kafka 19092。其 `.env.example` 的公开虚构密码仅供隔离开发，**不得复用到服务器**。开发进程不会自动读取 .env；脚本/进程环境为准。

完整回归包括：
- 真实 MySQL 业务/会话/维护测试、Redis/Kafka 集成、`go vet` / build；
- 前端单测、TypeScript 和生产构建；
- `scripts/acceptance.py`、`auth_acceptance.py`、`reset_acceptance.py`：真实 HTTP、并发、进程退出/重启、权限和恢复。
- `scripts/test-start.ps1`、`scripts/test_web_container.py`、公开源码门禁及其测试。

Go 集成独占 `refund_test`；三套 HTTP 验收串行独占 `refund_acceptance`。不修改 `refund_demo`；独立 Kafka topic/group 和 Redis prefix，不清理共享数据。真实依赖环境未配置时 Go 会 SKIP，不能称为完整通过。CI 使用独立 runner、固定 action SHA、只读权限；不持有部署秘密、不自动推镜像。

原始验收日志生成于 ignored `evidence/` 和 `.runtime/`，不公开；公开摘要只记录脱敏结果及复现方法。测试通过不等于生产容量、资金安全审计或人工签收。

## 停止、检查与交付

**Mac：双击根目录 `stop.command`**，或在项目目录执行 `./stop.command`。只停止默认项目，保留容器、配置和数据，下次双击 `start.command` 即可继续使用；关闭网页或终端不会停止后台服务，也无需退出整个 Docker Desktop。

自定义项目对应执行 `./stop.command --project refund-local-18088`，使用启动时相同的项目名称。一键启动结束也会打印对应项目的停止命令：

```sh
docker compose --project-name yishuld-refund-app --env-file .runtime/start-yishuld-refund-app.env -f compose.deploy.yaml stop
```

只停止该项目并保留卷。恢复运行再次执行同一个一键命令；不得用 `down -v`、共享 Redis flush 或 Docker 全局清理替代正常维护。

`backend/cmd/admin` 提供非破坏性 `seed`、只读 `inspect` 和需停服/精确白名单/`--confirm` 的全库测试重置。CLI 全库重置与页面当前企业恢复不同，日常复测优先页面入口。

```text
backend/       HTTP、SQL 业务、持久模拟渠道、事件 worker、测试
web/           ERP 界面、API 客户端、异步状态防护、测试
deploy/        Nginx、配置示例、独立部署说明
scripts/       一键启动、回归、容器验收、公开检查、打包
docs/          契约、安全和脱敏验证摘要
```

`python scripts/package.py` 检查同一份公开源码字节后生成 ZIP 和 SHA256，排除依赖、二进制、运行配置、密钥、原始证据及数据卷。服务器镜像发布状态见验证摘要；源代码、镜像发布与远端部署是不同交付阶段。

### 运行边界

当前只有五笔虚构订单、模拟渠道、单节点 Compose/单 outbox worker，没有真实支付、SSO、TLS、HA 或生产监控。创建 PROCESSING 后、渠道收到请求前崩溃，原请求可能查不到并保持 UNKNOWN，不自动补发。商户级写互斥有意优先正确性；动态列表最多 100 条，去重索引无自动过期，Kafka 保留 7 天；长期运行需要清理/重建与审计归档方案。
