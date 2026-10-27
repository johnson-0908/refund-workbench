# 会话与权限边界

## 使用范围

默认只在本机 loopback 运行，登录页选择企业和预置角色，不要求密码。这是明确的产品体验便利：它不验证登录者现实身份，不能保证“只有真实财务人员才能选择经办角色”。用户另行授权的容器交付允许通过精确配置的服务器地址访问虚构数据，但没有把免密入口变为生产身份核验。真正的生产上线需以组织 SSO/IdP 和人员授权替换这个入口；已实现的会话、CSRF 和对象级授权仍应保留。

## 权限矩阵

|资源/操作|未登录|本企业查看员|本企业经办|其他企业对象|
|---|---|---|---|---|
|登录页/健康检查|允许|允许|允许|不适用|
|订单列表、详情、退款详情|401|允许|允许|404|
|处理动态|401|只返回本企业|只返回本企业|不接受目标商户参数|
|申请退款|401|403|允许，继续业务约束|404|
|按原退款查询渠道结果|401|403|允许|404|
|注销当前会话|401|允许|允许|不可指定其他会话|
|恢复初始数据（仅显式开启安全维护时）|401|403|需 data:reset 与二次确认|不可指定其他企业|

表中 403/404 指有效会话及合法 CSRF/Origin 的请求；错误 CSRF 先拒绝，不执行业务。对象查询始终带已认证 merchant 条件，客户端不能传入商户/金额/角色改变权限。

## 实现

- 服务端 32 字节随机会话，MySQL 持久化摘要，不存明文 Cookie；8 小时到期，不以 Redis 缓存决定认证。
- Cookie 为 HttpOnly、SameSite=Strict、Path=/；本地 HTTP 默认未设置 Secure，HTTPS Origin 必须配置 `SESSION_COOKIE_SECURE=true`。设置/退出清除 Cookie 使用相同安全属性。新登录轮换旧会话，注销在数据库撤销，重启后仍可验证；提交成功前不发送新 Cookie。
- 写操作验证精确 Origin 和会话绑定的 CSRF token；不从客户端传入的 permissions、query 或转发 IP 取得授权。
- 默认关闭原题固定 Bearer。仅隔离自动评测显式启用，正常 `dev.ps1` 不继承这个兼容开关。有 Authorization 头但兼容关闭时即返回 401，不回退到同时提供的 Cookie。
- 服务端按直接连接来源限制每分钟 20 次登录，进程总计每分钟 120 次；不信任 XFF，来源桶最多 256 个。代理后的用户可能共享来源限流。每个预置身份最多 8 个活跃会话，登录事务串行维护限额并清理撤销/到期记录。接口 no-store / nosniff，不在日志/证据记录原始 Cookie 或 CSRF。
- 首次恢复会话完成前，前端不读取业务。注销、身份切换或失效清空业务状态、取消在途请求、丢弃旧响应；跨标签页同步会话变化。浏览器仅持有服务端返回的 UI 权限，不是授权源。
- 注销阻止后续请求，不撤销已经通过鉴权并开始执行的退款。金融操作是否成功必须查询持久化原单，不能通过刷新/注销认定回滚。
- 数据恢复是独立维护权限，默认服务关闭；开发脚本显式开启且后端检查数据库主机/库名。备份和恢复同事务，只处理当前企业；不以请求中的 merchant 字段定位目标，不 flush 共享缓存或删除共享消息主题。

## 容器部署配置：显式开放，失败关闭

- `HTTP_ADDR` 默认 `127.0.0.1:18080`。非 loopback 的 IP/通配监听要求 `ALLOW_NON_LOOPBACK_HTTP=true` **并且**显式设置非空 `AUTH_ALLOWED_ORIGINS`；不允许非 loopback DNS 名监听。容器内部可显式绑定 `0.0.0.0:18080`，部署只发布前端端口，不发布 backend/MySQL/Redis/Kafka。
- `AUTH_ALLOWED_ORIGINS` 是逗号分隔的完整精确 Origin；显式列表替换本地默认，不追加。拒绝 `*`、空列表、URL 凭据、路径/末尾斜杠、query/fragment、无效端口。请求须恰好一个 Origin 头，scheme/host/port 精确匹配，不基于 Host 或 `X-Forwarded-*` 自动扩张白名单，不反射请求来源。
- HTTPS Origin 与 `SESSION_COOKIE_SECURE=true` 强制一致。Secure=true 与非 loopback HTTP Origin 的组合启动失败；HTTP IP:端口展示使用 Secure=false，**仍然没有传输加密**。不能据此声称公网 HTTP 可保护会话免遭网络窃取。
- 五项安全布尔开关 `ALLOW_NON_LOOPBACK_HTTP`、`SESSION_COOKIE_SECURE`、`ENABLE_DEMO_BEARER`、`ENABLE_DATA_RESET`、`ALLOW_DOCKER_DEMO_RESET` 只能填精确 `true`/`false`，缺省为 false，空值或拼错立即失败。Origin/监听/Cookie 配置先校验，再连接数据库。直接使用库构造 handler 时，非法认证配置返回 503，不建立宽松 fallback。
- 数据恢复的默认来源仍是本地 TCP loopback 与精确库名 `refund_demo` / `refund_test` / `refund_acceptance`。容器扩展只在 `ALLOW_DOCKER_DEMO_RESET=true` 且连接精确为 `tcp(mysql:3306)/refund_demo` 时成立，仍须独立 `ENABLE_DATA_RESET=true`。其他远程主机、端口、库名、Unix socket 不由此获准。CLI `admin reset --confirm` 继续限于本地白名单，不继承这个扩展。
- `MYSQL_DSN` 显式优先；否则 server/admin 共用 `MYSQL_HOST/PORT/DATABASE/USER/PASSWORD` 驱动构造 DSN，防止特殊密码被 shell 手拼破坏。使用任何分字段后 HOST、USER、PASSWORD 必须齐全；PORT 默认 3306、DATABASE 默认 refund_demo。完全未设置时才使用原本地配置。真实部署密码只经受控运行配置传入，不提交或写入镜像。

## 超时、日志与一致性保护

容器网络边界：Redis/Kafka 不发布宿主端口，依赖本项目专用 private 网络；其内部连接没有额外认证/TLS，且 Web 为访问 API 也加入该网络，因此不是容器间零信任。拥有 Docker 管理权限的账号可以查看运行环境中的秘密，应按主机管理员管理，环境变量不等于加密秘密存储。Nginx `/healthz` 只证明代理存活，API health 只检查 MySQL，Kafka→Redis 的健康须用实际事件链路验收，而不能仅看一个 200。

- 每个 HTTP 请求有 15 秒 context，避免仅设置网络 WriteTimeout 却让数据库等待无限持续。HTTP Header/read/write/idle 超时分别为 5/10/30/60 秒，Header 配置上限 16 KiB；登录/退款申请/恢复 JSON 限 4 KiB UTF-8，拒绝客户端额外字段。
- 渠道调用受 5 秒 context 约束；已经登记受理的状态收尾使用独立、最多 8 秒 context，不随浏览器断开取消。MySQL 是最终依据，超时不能被映射成明确失败，更不能再次提交渠道。
- 同企业写操作先在进程内排队，再占用 MySQL advisory lock 连接；等待最多 5 秒，防止每个排队请求占一条连接耗尽池。跨进程锁覆盖渠道与状态写入全程；释放失败时物理丢弃连接，不带锁归还池。数据库池最多 24 条连接、8 条空闲连接。此串行策略优先保障退款/恢复一致性，不等同于生产高并发容量承诺。
- 恢复先在同事务归档六类业务数据，再删除/重建当前企业数据，更新持久化 watermark 和递增 data_version；归档或删除失败整体回滚。旧 Kafka/Redis 事件按数据库水位过滤；水位查询失败不展示可能过期的活动，前端版本缓存不能让旧退款复活。
- 请求内部错误只记录类型或 MySQL 数字错误码，不记录任意 SQL/driver 错误文本；连接解析/连接失败使用不含原始 DSN/密码的诊断，驱动警告不打印其原始参数。客户端只收到通用错误，不含内部 SQL。配置日志不输出密码、Cookie 或 CSRF。
- `/api/health` 仅返回 MySQL 连通状态或通用 503，不披露数据库、身份、组件地址或凭据。`server healthcheck` 使用 3 秒超时、拒绝重定向，不能绕过启动配置校验或授予业务权限。

## 验证与未覆盖范围

在推荐的 PowerShell 7（`pwsh`）运行 `scripts/test.ps1`，串行执行 Go 测试/vet/build、前端测试/build、原题真实进程验收、会话验收和恢复验收。Go 使用 `refund_test`，三个 HTTP 验收独占 `refund_acceptance`；不重置原 `refund_demo`。`scripts/auth_acceptance.py` 验证真实 HTTP、Cookie、越权、CSRF、撤销/过期和进程重启；`scripts/reset_acceptance.py` 验证跨进程互斥、备份和旧事件屏障。部署专项 Go 测试覆盖 Origin/Secure 冲突、非 loopback opt-in、Docker 数据恢复精确白名单、特殊密码 roundtrip、健康检查、请求 deadline 和日志脱敏。

最新实际结果见 `evidence/validation.json`、`evidence/acceptance.json`、`evidence/auth-acceptance.json`、`evidence/reset-acceptance.json` 和 `evidence/final-regression.json`。最后一份记录本轮实际 Go 工具链、耗时、测试计数/跳过情况以及原 demo 数据前后只读一致性快照哈希；原始数据 dump 不保存。文件不存在、FAIL 或与当前源码不对应时不得称已通过。

上述原始证据不公开，仓库读者请参阅 [脱敏验证摘要](VERIFICATION.md) 并执行相同脚本复现。公开检查的规则扫描不是全面秘密检测或安全审计。

本项目未实现真实身份核验、动态组织成员管理、审批流、长期安全审计、生产 TLS/HA 或正式渗透测试。ERP 样式和严谨的权限边界不等于这些功能已经具备。
