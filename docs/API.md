# API contract

本地 Base URL: `http://127.0.0.1:18080`；容器交付通过前端同源反向代理访问 `/api`，不发布后端/数据库/中间件宿主端口。时间均 RFC3339 UTC。JSON 金额为整数分。

默认使用服务端会话，浏览器不保存 Bearer Token。健康检查和登录入口公开，其余业务路由需要有效会话。

## 登录与授权
`POST /api/auth/login`：`{"identity_id":"a-write"}`，仅允许 `a-write`、`a-read`、`b-write`。拒绝未知字段。请求必须有且只有一个允许的 `Origin`，本地默认 `http://127.0.0.1:5173`、`http://localhost:5173`、`http://127.0.0.1:18080`。服务端设置 `erp_session` Cookie（HttpOnly / SameSite=Strict / Path=/，8 小时，本地 HTTP 默认不设 Secure），轮换并撤销旧会话；新 Cookie 只在 MySQL 提交成功后发送。部署时的 Origin 与 Secure 规则见下表。

`GET /api/auth/session`：返回当前会话；缺失/伪造/过期/撤销返回 401。登录和恢复的成功响应：
```ts
type Session = {
  user: {
    identity_id: 'a-write'|'a-read'|'b-write';
    merchant_id: 'A'|'B'; merchant_name: string; display_name: string;
    role: 'operator'|'viewer';
    permissions: ('orders:read'|'refunds:read'|'refunds:create'|'refunds:reconcile'|'data:reset')[];
  };
  csrf_token: string;
  expires_at: string;
}
```
查看员仅有两项 read 权限。权限由后端映射，不接受客户端 merchant/role/permissions。会话持久化于 MySQL，仅存随机 session secret 的摘要。

`POST /api/auth/logout`：服务端撤销当前会话并清除 Cookie。所有带 Cookie 的 POST（包括退出、申请、查证）必须同时携带允许的 `Origin` 和 `X-CSRF-Token: <session.csrf_token>`。缺失或错误返回 403。业务请求需要 `credentials: 'same-origin'`。

前端 GET 业务请求也携带 `X-CSRF-Token`：若 Cookie 已在其他标签页轮换而请求仍使用旧 token，返回 403 `SESSION_CHANGED`，不能把新企业数据送入旧会话页面。首次 `GET /api/auth/session` 不需要这个头。POST 的 CSRF/来源错误为 `CSRF_REJECTED`。

这是用户指定的预置身份免密体验入口，不是生产身份核验；显式开放容器访问不会改变这一边界。原题固定 `Bearer demo-a-write` / `demo-a-read` / `demo-b-write` 仅在后端显式 `ENABLE_DEMO_BEARER=true` 时可用；默认和 `dev.ps1` 均禁用。兼容模式只用于隔离 CLI 评测，不在浏览器启用，不能用于生产。默认模式只要出现 `Authorization` 头即返回 401，即使同时有有效 Cookie，也不降级使用 Cookie 身份。

## 监听、来源与数据库配置

|环境变量|默认/规则|
|---|---|
|`HTTP_ADDR`|`127.0.0.1:18080`；非 loopback 的 IP/通配监听须同时满足下面两个显式配置；不接受非 loopback DNS 监听名|
|`ALLOW_NON_LOOPBACK_HTTP`|默认 `false`；容器私网监听 `0.0.0.0:18080` 必须显式 `true`|
|`AUTH_ALLOWED_ORIGINS`|未设置时使用上文三个本地地址；设置后逗号分隔的精确白名单**替换**默认列表，不能是空值|
|`SESSION_COOKIE_SECURE`|默认 `false`；HTTPS Origin 必须 `true`；`true` 不允许非 loopback HTTP Origin，防止不可用/不安全的组合|
|`ENABLE_DEMO_BEARER`|默认 `false`；仅隔离评测显式开启|
|`ENABLE_DATA_RESET`|默认 `false`；独立控制维护入口和 `data:reset` 权限|
|`ALLOW_DOCKER_DEMO_RESET`|默认 `false`；仅对精确 `tcp(mysql:3306)/refund_demo` 扩展维护数据库白名单，不会自行开启维护功能|

布尔变量只能使用小写 `true` / `false`；显式空值、`1`、`TRUE` 等均启动失败。非 loopback 监听还必须显式配置 `AUTH_ALLOWED_ORIGINS`。所有配置先校验再连接数据库；校验错误不输出原始 DSN 或密码。

Origin 必须是完整 `http(s)://host[:port]`，不能有通配符、userinfo、路径（包括末尾 `/`）、query 或 fragment。主机使用 ASCII/punycode；端口范围 1..65535。请求中的 scheme、host、port 必须与配置完全相同，不规范化后放宽匹配，不反射 Origin，不信任 `X-Forwarded-*` 替代来源。前端与 API 通过同源代理使用 Cookie，不提供任意跨域 CORS。部署 Compose 的 `APP_ORIGIN` 映射到后端 `AUTH_ALLOWED_ORIGINS`，不是另一项后端授权来源。

数据库配置由 server/admin 共用：
- 若设置 `MYSQL_DSN`，优先使用该非空、可解析的 DSN。
- 否则可以设置 `MYSQL_HOST` / `MYSQL_PORT` / `MYSQL_DATABASE` / `MYSQL_USER` / `MYSQL_PASSWORD`；一旦使用任何分字段，HOST、USER、PASSWORD 必须齐全且非空。PORT 默认 `3306`，DATABASE 默认 `refund_demo`。
- HOST 只填主机/IP，不带端口；IPv6 不加方括号。字段经 MySQL 驱动构造 DSN，支持密码中的特殊字符，无需 shell 手拼或 URL 转义。
- 完全没有数据库环境配置时沿用本地 `127.0.0.1:3307` / `refund_demo` 配置。分字段方式启用 `parseTime=true` 和 UTC。

## 请求与健康边界

- 登录、申请退款和恢复的 JSON body 上限为 4 KiB UTF-8，拒绝额外字段；退款金额和商户始终服务端确定。
- 每个 HTTP 请求 context 最长 15 秒；读取 Header 5 秒、读取请求 10 秒、写响应 30 秒、空闲连接 60 秒，Header 配置上限 16 KiB。
- 渠道调用上限 5 秒。已经受理的操作即使请求取消，仍最多使用独立 8 秒 context 写入明确结果或未知状态，并保留商户互斥锁直到收尾；HTTP 超时/断线不代表退款失败或撤销。
- `server healthcheck` 用 3 秒超时请求自身 `/api/health`，通配监听通过 loopback 检查；只接受 HTTP 200 且 JSON `status=ok`，不跟随重定向，不携带身份信息。

## 数据
```ts
type Refund = {
  refund_id: string; order_id: string; amount_cents: number;
  provider_request_id: string; reason: string;
  status: 'PROCESSING'|'UNKNOWN'|'SUCCEEDED'|'FAILED';
  message: string; created_at: string; updated_at: string;
}
type Order = {
  order_id: string; merchant_id: string; amount_cents: number;
  status: 'PAID'|'UNPAID'; refund: Refund | null; data_version: number;
}
```

- `GET /api/orders` → `200 {items: Order[]}`。
- `GET /api/orders/:id` → `200 Order`。
- `POST /api/orders/:id/refunds`，`Idempotency-Key: <1..128 ASCII safe chars>`，body `{"reason":"用户取消服务"}`（1..200 Unicode 字符，非全空白，拒绝额外字段）。首次 `201 Refund`，同键同参 `200 Refund`，已存在退款换键 `409 REFUND_EXISTS`。
- `GET /api/refunds/:id` → `200 Refund`（包括 FAILED/UNKNOWN）。
- `POST /api/refunds/:id/reconcile` → `200 Refund`；仅写身份可用，只查原渠道请求，不提交。
- `GET /api/activity` → `200 {items: Activity[], available: boolean, data_version: number}`；Redis 不可用或 MySQL 恢复水位查询失败返回 items=[] / available=false，前端展示动态暂不可用，不宣告退款失败。
- `GET /api/health` → `{status:"ok"}`，只检查 MySQL 连通性；不可用则 503 通用错误，不返回 DSN、数据库内容、组件地址或凭据。

错误：`{error:{code:string,message:string}}`。401 `UNAUTHORIZED`，403 `FORBIDDEN`，404 `NOT_FOUND`，409 `IDEMPOTENCY_CONFLICT` / `REFUND_EXISTS` / `ORDER_NOT_PAID` / `RESET_BUSY`，400 `VALIDATION_ERROR`，429 `RATE_LIMITED`，503 `UNAVAILABLE`。申请、查证或恢复的互斥锁等待超过 5 秒，或写请求排队期间数据被恢复，返回 `RESET_BUSY`，应先刷新核实后重试。

## 当前企业恢复初始数据

`POST /api/maintenance/reset`，body `{"confirmation":"恢复初始数据"}`，Cookie + CSRF + Origin；不能传 merchant/amount 等覆盖字段。仅显式 `ENABLE_DATA_RESET=true` 且数据库来源验证通过时，经办会话获得 `data:reset`；只读没有此权限。关闭时返回 403 `RESET_DISABLED`。

数据库允许范围：本地 TCP 主机 `127.0.0.1` / `localhost` / `::1` 且库名精确为 `refund_demo`、`refund_test`、`refund_acceptance`；或额外显式 `ALLOW_DOCKER_DEMO_RESET=true` 且地址/库名精确为 `mysql:3306/refund_demo`。不会泛化允许远程 IP、其他 Docker 主机/端口/库名。开启维护但来源不符时 server 启动失败；仅构造 HTTP handler 也不会授予维护权限。CLI `admin reset --confirm` 仍仅允许本地白名单数据库，不因 Docker 开关而放宽，应停服后用于隔离测试。

成功响应：`{reset_id,merchant_id,order_count,reset_at,data_version,message}`。A 恢复 4 单，B 恢复 1 单；仅当前会话企业的旧退款、幂等、渠道和 outbox 数据先归档再移除，恢复固定订单与渠道模式，保留会话和其他企业。所有数据修改与备份同事务；恢复与申请/查证全程互斥。

`data_version` 从 0 开始，每次页面当前企业恢复单调递增；停服 CLI 全库测试重置会清除水位，开启版本 0 的新轮次。前端收到更高版本清理已观察到的退款终态缓存，不接受旧版本响应覆盖；跨标签通知加速同步，但不是唯一正确性保障。动态接口基于持久化恢复时间过滤旧事件，Redis/Kafka 的残留与迟到投递不能复活旧业务动态。

Activity / Kafka payload:
```json
{"event_id":"ev_x","merchant_id":"A","refund_id":"rf_x","order_id":"A-1002","amount_cents":8800,"status":"UNKNOWN","message":"渠道处理结果待确认，请查询原退款","occurred_at":"2026-10-27T01:00:00Z"}
```

## Outbox SQL integration contract
`outbox_events(id BIGINT AUTO_INCREMENT PRIMARY KEY, event_id VARCHAR(40) UNIQUE, merchant_id VARCHAR(16), refund_id VARCHAR(40), payload JSON NOT NULL, created_at DATETIME(6) NOT NULL, published_at DATETIME(6) NULL)`；业务事务写入，middleware worker 只发布/标记。

## 初始化数据
|订单|商户|分|状态|渠道模式|
|---|---|---:|---|---|
|A-1001|A|19900|PAID|SUCCESS|
|A-1002|A|8800|PAID|LOST_RESPONSE|
|A-1003|A|5600|PAID|REJECT|
|A-1004|A|12900|UNPAID|SUCCESS (不得调用)|
|B-2001|B|25600|PAID|SUCCESS|
