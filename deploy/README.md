# 独立服务器容器部署（IP + 端口）

## 交付与边界

`compose.deploy.yaml` 是可单独复制到服务器的 Compose 文件：包含 MySQL、Redis、Kafka、Go API 和 Nginx/Vue；没有源码目录、SQL 脚本或 Nginx 配置的外部挂载依赖。MySQL 官方入口只初始化 `refund_demo` 与 `refundapp`，后端以非破坏性 migration/seed 补齐表和固定订单。服务器不需要 Go、Node、Java 或本地数据库。

应用镜像默认路径（私有仓库）：

```text
crpi-vpvynwydljgcai3w.cn-guangzhou.personal.cr.aliyuncs.com/pandacount/refund-workbench-backend:0.1.1
crpi-vpvynwydljgcai3w.cn-guangzhou.personal.cr.aliyuncs.com/pandacount/refund-workbench-web:0.1.1
```

当前默认版本为 `0.1.1`，平台为 **linux/amd64**：Web 修复 HTTP 首次登录，后端复用 `0.1.0` 已验收镜像的同一 digest；两个版本标签均已推送并回读。精确 digest、本机空卷验收及经授权的公网补丁验证见 [验证记录](../docs/VERIFICATION.md)。镜像发布与目标服务器部署分别记录。后端使用固定官方 Go `1.26.10-alpine3.23` 构建、Alpine `3.23.7` 运行；两者及 MySQL/Redis/Kafka 均锁定 manifest digest。Dockerfile 不包含密码或登录凭据。

应用仍是选择企业/角色的免密虚构业务入口，任何能访问页面的人都能选择经办身份；Cookie、CSRF 和租户隔离不能替代真实身份核验。HTTP IP 访问不加密 Cookie。应只向可信测试人员开放，限制防火墙/安全组来源；生产需另行完成身份提供方、TLS、授权管理和运维保障。启动脚本不会自动修改服务器防火墙或其他现有服务。

## 一键启动：只有 Docker 也能运行

从完整源码目录执行，Docker Engine / Desktop 已启动即可，**不需要宿主机 Go、Node、Java，也不需要先登录私有镜像仓库**：

```powershell
# Windows PowerShell（推荐 PowerShell 7）
./scripts/start.ps1
# 自定义本地空闲端口：
./scripts/start.ps1 -Project refund-local-18088 -Port 18088
```

```sh
# Linux / Bash 4+
bash scripts/start.sh
# 自定义本地空闲端口：
bash scripts/start.sh --project refund-local-18088 --port 18088
```

默认通过 `compose.build.yaml` 在容器内构建后端和前端，使用本地应用镜像，拉取官方基础/中间件镜像仍需网络。项目默认为 `yishuld-refund-app`，与原 `yishuld-refund` 和手工部署项目隔离；默认只绑定 `127.0.0.1:8080`，健康检查全部通过才输出 Ready。

路径包含中文等非 ASCII 字符时，脚本自动把明确白名单的源码复制到 ASCII 构建目录，绕过部分 Docker Buildx 的 gRPC 路径编码缺陷；不会复制 `.runtime`、`.env`、node_modules、日志或数据库。Windows 优先使用工作盘已有 `code` 下的 `refund-workbench-build`，否则用系统临时目录；Linux 用 `/tmp/refund-workbench-build`。可通过 `REFUND_BUILD_ROOT` 明确选择其他可写 ASCII 目录。构建副本保留用于定位，路径记录在 ignored `.runtime/build-<项目名>.*`；原源码不移动，不修改 Docker 全局配置。

首次生成 `.runtime/start-<项目名>.env`，包含两份独立的随机 64 位 hex 数据库密码；Windows 在创建文件时原子设置当前用户专属 ACL，通过校验后才写入密码，Linux Bash 以 0600 创建。脚本不打印密码，第二次使用原文件，不重新生成或覆盖。已有项目卷却丢失配置时拒绝生成新密码，须先找回原配置。保留该文件及卷，不要在同项目切换密码或丢弃配置。重新传入不同地址参数会拒绝覆盖，需明确编辑该配置或用新项目名。权限限制不防护本机管理员或拥有 Docker 权限的账号。

访问服务器 IP 时必须明确给出监听地址和浏览器 Origin；脚本会提醒免密身份边界，但不会修改防火墙，也不以提醒阻塞已明确配置的启动：

```powershell
./scripts/start.ps1 -Project refund-server -Host 0.0.0.0 -Port 8080 -Origin http://实际服务器IP:8080 -Pull
```

```sh
bash scripts/start.sh --project refund-server --host 0.0.0.0 --port 8080 --origin http://实际服务器IP:8080 --pull
```

`-Pull` / `--pull` 不执行源码构建，使用私有仓库预构建镜像；服务器需先完成 `docker login`。不加此选项就是源码构建。`Host` 为 IPv4 监听地址，域名放在 Origin；公网 IP 经 NAT 映射时一般监听 `0.0.0.0` 而非绑定本机并不存在的公网地址。

`-Check` / `--check` 可生成或读取私密配置并校验 Docker、Compose 与端口，不启动容器。也会拒绝无效 Origin 端口、HTTPS 配 `SESSION_COOKIE_SECURE=false`，以及非 loopback HTTP 配 Secure Cookie 的冲突配置。占用端口时报错，不自动结束占用进程。Linux 优先使用 `ss`/`lsof` 查端口；缺少两者时使用本地连接预检，最终仍以 Docker 的实际端口绑定为准。重复启动已有同项目服务是幂等操作；命令末尾会打印只停止本项目、不删除卷的 `docker compose ... stop` 命令。

一键脚本使用 `.runtime` 中自己的配置；下面手工部署流程使用 `.env.deploy`，两条流程不要混用配置或项目名。镜像更新后重复启动保持持久数据；不会自动初始化别的项目或清理 Docker 空间。

可复现配置回归：`./scripts/test-start.ps1`（需 Docker 就绪，但不启动容器）检查首次配置、重入不覆盖、Pull 模式、Origin 门禁、端口冲突不杀进程及 Windows 文件 ACL；会在 ignored `.runtime/` 留下独立测试配置。Linux 脚本语法检查为 `bash -n scripts/start.sh`。这些检查不代替空卷容器启动及浏览器业务验收。

## 1. 准备配置

要求 Linux Docker Engine 与现代 Docker Compose v2+（支持 `up --wait`）。仅发布 Web 端口，默认 `127.0.0.1:8080`；MySQL、Redis、Kafka 与 API **无宿主机端口映射**。内部网络与原本本地 `compose.yaml` 项目分开。

在服务器创建专用目录，将根目录的 `compose.deploy.yaml` 放入该目录。可将本目录的 `.env.example` 复制为同目录的 `.env.deploy`；或直接创建以下文件（所有示例值都需按注释处理）：

```dotenv
DEPLOY_NAMESPACE=yishuld-refund-server
REGISTRY=crpi-vpvynwydljgcai3w.cn-guangzhou.personal.cr.aliyuncs.com
IMAGE_NAMESPACE=pandacount
IMAGE_TAG=0.1.1
BIND_IP=0.0.0.0
WEB_PORT=8080
APP_ORIGIN=http://实际服务器IP:8080
SESSION_COOKIE_SECURE=false
MYSQL_ROOT_PASSWORD=填写第一份随机64位十六进制密码
MYSQL_PASSWORD=填写另一份随机64位十六进制密码
ENABLE_DATA_RESET=true
```

- `APP_ORIGIN` 必填且必须与浏览器地址的 **协议、主机、端口** 完全一致；不要填写路径、通配符或末尾 `/`。服务器地址属于私有部署配置，不预设到公开模板。它传入后端 `AUTH_ALLOWED_ORIGINS`；外来 Origin/缺失 CSRF 的写请求仍被拒绝。
- `BIND_IP=0.0.0.0` 是用户明确选择外部 IP 访问时的配置；不需要远程访问时保留默认 `127.0.0.1`。先确认 8080 未被现有服务使用，冲突时改 `WEB_PORT` 和 `APP_ORIGIN`，不要停掉不相关服务。
- 分别执行两次 `openssl rand -hex 32`，把结果填入两个密码字段。容器入口检查均为 64 位十六进制且不同；格式检查不等于能检测熵，必须随机生成，不使用重复字符或公开示例。密码不写入源码、截图或验收证据。
- `.env.deploy` 设为 `chmod 600 .env.deploy`。环境变量对拥有 Docker 权限的管理员可见；这不是加密秘密存储。不要提交此文件，也不要把 `docker compose config` 的完整输出分享出去。
- 命令行环境变量优先于 env 文件，执行前确认没有其他项目导出的同名变量。不要把本地开发 `.env.example` 中的公开密码用于服务器。
- `DEPLOY_NAMESPACE` 决定项目名、网络和三份命名卷；默认卷为 `yishuld-refund-server_mysql-data` / `_redis-data` / `_kafka-data`。升级保持 namespace 不变。独立验收用新的 namespace 和空闲 Web 端口，不能复用业务卷。

## 2. 拉取与启动

私有仓库需要服务器已有该仓库读取权限。没有登录时使用交互式 `docker login`，不要把密码写入命令参数或聊天：

```sh
docker login crpi-vpvynwydljgcai3w.cn-guangzhou.personal.cr.aliyuncs.com
docker compose --env-file .env.deploy -f compose.deploy.yaml config --quiet
docker compose --env-file .env.deploy -f compose.deploy.yaml pull
docker compose --env-file .env.deploy -f compose.deploy.yaml up -d --wait --wait-timeout 300
docker compose --env-file .env.deploy -f compose.deploy.yaml ps
```

`config --quiet` 只校验、不输出插值后的密码。APP_ORIGIN/密码空缺会立即拒绝；密码格式错误会让 MySQL 启动失败并输出不含密码的错误。MySQL/Redis/Kafka 健康后才启动 backend，backend 健康后才启动 web；应用容器均以非 root 身份运行，根文件系统只读、移除 capabilities。健康检查通过仅代表本次就绪，不代表未来持续可用或生产 HA。

默认内存上限：MySQL/Kafka 各 1 GiB、Redis 256 MiB、backend 384 MiB、web 128 MiB，并限制进程数量、轮转日志；建议 Docker 至少分配 4 GiB 可用内存。可按实际观测在 env 中调整 `MYSQL_MEMORY_LIMIT`、`KAFKA_MEMORY_LIMIT`、`REDIS_MEMORY_LIMIT`、`BACKEND_MEMORY_LIMIT`、`WEB_MEMORY_LIMIT`，不把内存上限误认为吞吐保障。此配置不是生产压测结果。

浏览器打开 `http://实际服务器IP:8080`。快速检查：

```sh
curl -fsS http://实际服务器IP:8080/healthz
curl -fsS http://实际服务器IP:8080/api/health
curl -i http://实际服务器IP:8080/api/orders
# 未登录的 orders 应返回 401；再到浏览器选择身份登录。
```

实际交互复核：A 经办只见 4 单，B 经办只见 1 单；只读身份不能申请/查证/恢复；A-1002 首次 UNKNOWN 后查询原退款得到成功；退出后不能读取业务。HTTP 模式 `SESSION_COOKIE_SECURE=false`，不可误设 true 导致浏览器不发送 Cookie；将来改 HTTPS 时应一起修改 Origin、反向代理与 Secure 配置并重新验收。

## 3. 恢复初始数据与持久化

应用默认不启用维护能力；本部署示例通过 `ENABLE_DATA_RESET=true` **显式**开启，可设 false 关闭。经办才获得 `data:reset`，必须输入 `恢复初始数据` 并带有效会话/CSRF/Origin。Docker 环境另有 `ALLOW_DOCKER_DEMO_RESET=true` 严格允许内部 `mysql:3306/refund_demo`，不是任意远程数据库白名单。

页面恢复仅处理当前企业：六表业务快照归档到 `maintenance_resets`，与清理、固定订单重建、watermark/data_version 递增同事务。保留其他企业和登录会话；与退款/查证全程互斥；旧 Kafka/Redis 活动通过持久 watermark 隐藏。它不是生产备份系统，没有历史归档恢复入口。

MySQL、Redis AOF、Kafka 日志分别保存在项目命名卷。正常重启/升级不清空它们。Redis/Kafka 仅辅助投影，退款真相源始终为 MySQL。不要执行 `down -v`、`docker system prune --volumes`、共享 Redis flush 或删共享 topic。不要在本部署运行本地 `scripts/acceptance.py` 等会重置测试库的脚本。

## 4. 日常检查、停止与升级

```sh
docker compose --env-file .env.deploy -f compose.deploy.yaml ps
docker compose --env-file .env.deploy -f compose.deploy.yaml logs --tail 100 backend web
# 仅停此部署，保留容器配置和持久卷：
docker compose --env-file .env.deploy -f compose.deploy.yaml stop
# 再启动同一部署：
docker compose --env-file .env.deploy -f compose.deploy.yaml up -d --wait --wait-timeout 300
```

日志按单容器 10 MB × 3 份轮转。分享诊断前再次检查不含 Cookie、CSRF、数据库密码或个人信息。MySQL 初始化密码变量只对**首次空卷**创建账号生效；修改 env 文件不会自动轮换已有数据库密码，切勿用删除卷“修复密码”。密码轮换需有备份并同步修改账号与应用配置。

新配置默认使用 `0.1.1`；已有 `.runtime/start-<项目>.env` 保留其原有版本，不会悄悄升级。需要升级时显式修改该文件的 `IMAGE_TAG`，若 Compose 已使用 digest 固定，则须显式替换相应 digest。

升级前备份数据库与配置，确认新版本兼容；只调整 `IMAGE_TAG`（或在 Compose 中改为经过确认的应用镜像 digest），然后 `pull`、`up -d --wait`，保持原 namespace/卷/密码。不要把“容器启动成功”作为资金业务验收，也不要无备份回退数据库结构。

## 5. 源码构建（维护人员）

从源码根目录构建，构建过程不读取 `.env.deploy`、不需要数据库凭据：

```sh
docker build -f Dockerfile.backend -t crpi-vpvynwydljgcai3w.cn-guangzhou.personal.cr.aliyuncs.com/pandacount/refund-workbench-backend:0.1.1 .
docker build -f Dockerfile.web -t crpi-vpvynwydljgcai3w.cn-guangzhou.personal.cr.aliyuncs.com/pandacount/refund-workbench-web:0.1.1 .
```

推送必须使用明确授权的仓库身份；此文档中的命令不会自动推送或修改其他仓库。当前 Dockerfile 可针对目标架构构建，是否已交付多架构镜像以实际 manifest 为准，不假定服务器架构。

参考：[Docker Compose 环境变量/必填插值](https://docs.docker.com/compose/how-tos/environment-variables/variable-interpolation/)、[官方 Go 镜像](https://hub.docker.com/_/golang)、[官方 Alpine 镜像](https://hub.docker.com/_/alpine)。
