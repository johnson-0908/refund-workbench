# 自动化测试执行日志（公开脱敏版）

> 本文件从真实本地自动验收输出按字段白名单导出，不是模拟日志。保留命令、阶段耗时、逐项测试名称与实际结果；不复制原始 stdout/stderr、请求响应、环境变量、路径、容器标识、Cookie 或凭据。

## 1. 执行来源与边界

- 开始：2026-10-29 10:36:16 +0800（Asia/Shanghai）
- 完成：2026-10-29 10:38:39 +0800（Asia/Shanghai）
- 环境：Windows / PowerShell 7 / 本地 Docker Desktop；前后端测试在锁定 Docker 构建阶段执行。
- Docker Engine：29.5.2
- 范围：本轮全新独立 Compose 项目、空业务卷、仅 loopback Web 端口；允许复用下载/构建缓存。
- 整体结果：**PASS**；测试完成后仅停止本轮服务，保留本地证据及数据。
- 源码快照：99 个白名单文件；清单 SHA256：`0d85b6955c679e7bccf7ccc0addfbb364bb150f0da5eb28bdcf0d14f734ca8b6`。
- 导出前已逐文件核对该源码快照与工作区一致。随后仅增加本日志及文档链接；快照摘要不是 Git 提交号。
- 本日志不证明新提交的云端 CI 已通过，也不证明本轮进行过公网真实浏览器 E2E、Go race、依赖漏洞审计或生产验收。

## 2. 实际入口与测试命令

```sh
python scripts/verify.py
```

入口在自己的新项目中执行一键构建、真实容器 HTTP/SQL 验收。Python 守卫在宿主运行，Go/Vitest 在工具容器中运行；以下列出相应命令（省略私有实例参数）：

```sh
python -m unittest discover -v -s scripts -p "test_*.py"
# 生产镜像的前端构建阶段
npm ci --no-audit --no-fund
npm run build
# 同一构建阶段的前端测试
./node_modules/.bin/vitest run --reporter=json --outputFile=/tmp/frontend.json
# Go 构建阶段，真实独立 refund_test / Redis / Kafka
go test -json -count=1 -timeout=10m ./...
go vet ./...
go build ./...
```

构建阶段允许命中 Docker 缓存，上列 npm 安装/构建是锁定 Dockerfile 中的阶段命令，不表示本轮强制重新安装或重新编译；Go/Vitest 测试命令本轮实际执行。Go/前端不允许以 SKIP 充数；组件测试使用 jsdom 与模拟 API，不将其描述为真实浏览器交互。原始报告保留本机，不随仓库分发。

## 3. 阶段记录

|结果|阶段|耗时（秒）|
|---|---|---|
|PASS|[1/6] 检查本地 Docker 与隔离资源|1.17|
|PASS|[2/6] 公开源码预检并冻结本轮源码|1.78|
|PASS|[3/6] 从源码构建并启动全新五服务|33.23|
|PASS|[4/6] HTTP / SQL / 并发 / 重启 / 权限 / 恢复 / Kafka→Redis|67.64|
|PASS|[5/6] 前端组件与异步竞态自动测试|9.2|
|PASS|[6/6] 真实 MySQL/Redis/Kafka Go 集成、vet、build|25.52|
|PASS|仅停止本轮测试容器，保留证据和数据卷|4.73|

## 4. 测试数量

|测试组|PASS|SKIP|说明|
|---|---:|---:|---|
|真实容器 HTTP/SQL|113|0|含幂等、16 个不同键并发、UNKNOWN 时重启、权限、恢复与事件链路|
|Go|123|0|4 个包，数量包含子测试；真实 MySQL/Redis/Kafka；未启用 race|
|前端|103|0|9 个文件；jsdom/模拟 API|
|Python 守卫/契约/公开门禁|55|2|Windows 实际 symlink 创建不可用，2 项明确不计通过|

## 5. 真实 HTTP/SQL 逐项结果

```text
PASS 001 project name is explicitly reserved for deployment acceptance
PASS 002 explicit env file stays inside the ignored local runtime directory
PASS 003 HTTP origin is literal loopback and exactly matches the saved deployment
PASS 004 Docker endpoint is local, not a remote daemon
PASS 005 isolated project contains exactly five containers
PASS 006 container belongs to a unique expected running service
PASS 007 web uses a non-root, read-only, capability-dropped runtime
PASS 008 container networks are owned by this isolated project
PASS 009 container networks are owned by this isolated project
PASS 010 container belongs to a unique expected running service
PASS 011 backend uses a non-root, read-only, capability-dropped runtime
PASS 012 container networks are owned by this isolated project
PASS 013 container belongs to a unique expected running service
PASS 014 persistent storage is an owned isolated named volume
PASS 015 volume project label matches this test
PASS 016 container networks are owned by this isolated project
PASS 017 container belongs to a unique expected running service
PASS 018 persistent storage is an owned isolated named volume
PASS 019 volume project label matches this test
PASS 020 Kafka image-declared anonymous config volume is exclusive to this container
PASS 021 Kafka image-declared anonymous config volume is exclusive to this container
PASS 022 container networks are owned by this isolated project
PASS 023 container belongs to a unique expected running service
PASS 024 persistent storage is an owned isolated named volume
PASS 025 volume project label matches this test
PASS 026 container networks are owned by this isolated project
PASS 027 only web is published and its mapping exactly matches the requested origin
PASS 028 backend is bound to the verified private SQL fixture and exact origin
PASS 029 database has exactly the five original orders and no active financial records
PASS 030 SQL fixture scenario guard passes without resetting any data
PASS 031 edge headers are retained on /healthz
PASS 032 edge headers are retained on /
PASS 033 edge headers are retained on /finance/orders
PASS 034 edge headers are retained on /api/orders
PASS 035 HTML references a fingerprinted public JavaScript asset
PASS 036 hashed JavaScript is immutable and gzip encoded
PASS 037 missing assets return non-cacheable 404 rather than HTML
PASS 038 anonymous business access is denied (HTTP 401)
PASS 039 untrusted login origin is rejected (HTTP 403)
PASS 040 login returns authoritative session for a-write (HTTP 200)
PASS 041 login cookie and session policy are secure and non-cacheable
PASS 042 identity role, tenant and permissions are server-owned
PASS 043 login returns authoritative session for a-read (HTTP 200)
PASS 044 login cookie and session policy are secure and non-cacheable
PASS 045 identity role, tenant and permissions are server-owned
PASS 046 login returns authoritative session for b-write (HTTP 200)
PASS 047 login cookie and session policy are secure and non-cacheable
PASS 048 identity role, tenant and permissions are server-owned
PASS 049 merchant views contain exactly A four and B one original orders
PASS 050 missing CSRF rejects financial mutation (HTTP 403)
PASS 051 invalid CSRF rejects financial mutation (HTTP 403)
PASS 052 untrusted Origin rejects financial mutation (HTTP 403)
PASS 053 read-only role cannot create refunds (HTTP 403)
PASS 054 cross-merchant object remains indistinguishable from missing (HTTP 404)
PASS 055 cross-merchant object remains indistinguishable from missing (HTTP 404)
PASS 056 cross-merchant object remains indistinguishable from missing (HTTP 404)
PASS 057 cross-merchant object remains indistinguishable from missing (HTTP 404)
PASS 058 client cannot forge merchant_id (HTTP 400)
PASS 059 client cannot forge amount_cents (HTTP 400)
PASS 060 rejected requests produced no refunds or channel calls
PASS 061 ten concurrent identical requests converge to one refund and one creation
PASS 062 SQL confirms one full-amount refund and one channel effect
PASS 063 another key cannot refund the same order again (HTTP 409)
PASS 064 same key with a changed reason conflicts (HTTP 409)
PASS 065 same key with a changed order conflicts (HTTP 409)
PASS 066 same key replay returns the original current successful refund
PASS 067 replay and changed parameters have no extra financial effects
PASS 068 sixteen concurrent distinct keys produce one creation and fifteen explained conflicts
PASS 069 lost channel response creates an explainable unknown refund (HTTP 201)
PASS 070 transport uncertainty is never treated as final failure
PASS 071 distinct-key concurrency leaves one SQL refund and one channel acceptance
PASS 072 restart target is exactly the verified project's backend
PASS 073 own backend becomes healthy after restart
PASS 074 UNKNOWN and original identities survive a real backend restart before any query
PASS 075 cross-merchant refund access is rejected (HTTP 404)
PASS 076 cross-merchant refund access is rejected (HTTP 404)
PASS 077 viewer cannot reconcile (HTTP 403)
PASS 078 query resolves the original channel result (HTTP 200)
PASS 079 query preserves the original refund and provider identities
PASS 080 explicit channel rejection creates a failed refund (HTTP 201)
PASS 081 only explicit rejection produces a failed terminal state with a readable reason
PASS 082 failed refund cannot be resubmitted with a new key (HTTP 409)
PASS 083 unpaid order is ineligible (HTTP 409)
PASS 084 same idempotency key is independently scoped to B (HTTP 201)
PASS 085 B receives its own full amount
PASS 086 reconcile only queried once and unpaid order never reached channel
PASS 087 A writer and viewer cannot access B refund objects (HTTP 404)
PASS 088 A writer and viewer cannot access B refund objects (HTTP 404)
PASS 089 A writer and viewer cannot access B refund objects (HTTP 404)
PASS 090 A writer and viewer cannot access B refund objects (HTTP 404)
PASS 091 real outbox to Kafka to Redis activity becomes visible
PASS 092 SQL outbox is published and Redis holds the matching business receipts
PASS 093 reset requires write permission (HTTP 403)
PASS 094 reset requires CSRF (HTTP 403)
PASS 095 reset requires the exact confirmation (HTTP 400)
PASS 096 reset cannot override the merchant (HTTP 400)
PASS 097 current-merchant reset succeeds with exact confirmation and CSRF (HTTP 200)
PASS 098 reset advances the current merchant version and restores four orders
PASS 099 atomic archive is durable, current-merchant only and complete
PASS 100 A active refunds are cleared while B business state is unchanged
PASS 101 SQL active financial tables are cleared only for A
PASS 102 reset watermark hides old activity without flushing Redis
PASS 103 restored fixture accepts one new refund using its cleared key (HTTP 201)
PASS 104 new refund is distinct and successful
PASS 105 new activity passes the fence while old refunds remain filtered
PASS 106 restart target is exactly the verified project's backend
PASS 107 own backend becomes healthy after restart
PASS 108 refund, channel and order identities survive backend restart
PASS 109 persistent session survives backend restart
PASS 110 persistent session survives backend restart
PASS 111 persistent session survives backend restart
PASS 112 restart preserves the durable reset fence and B data
PASS 113 bounded read baseline has no HTTP or transport errors
```

## 6. Go 逐项结果（包含子测试）

```text
PASS TestResetDSNGuard
PASS TestAuthenticate/demo-a-write
PASS TestAuthenticate/demo-a-read
PASS TestAuthenticate/demo-b-write
PASS TestDeploymentServerConfigDefaultsAndExplicitOptIn
PASS TestAuthenticate/#00
PASS TestAuthenticate/forged
PASS TestAuthenticate/demo-b-read
PASS TestAuthenticate
PASS TestDeploymentServerConfigRejectsUnsafeAndMistypedValues
PASS TestDeploymentListenerAllowsOnlyExplicitIPBinding
PASS TestValidateCreate/normal
PASS TestValidateCreate/preserved_spaces
PASS TestValidateCreate/200_unicode
PASS TestValidateCreate/201_unicode
PASS TestValidateCreate/empty_key
PASS TestValidateCreate/long_key
PASS TestValidateCreate/key_limit
PASS TestValidateCreate/unicode_key
PASS TestValidateCreate/space_key
PASS TestValidateCreate/empty_reason
PASS TestValidateCreate/unicode_whitespace
PASS TestValidateCreate/invalid_utf8
PASS TestValidateCreate
PASS TestHTTPInvalidIdentityWithoutDatabase
PASS TestDeploymentHealthcheckRequiresHealthyResponse/healthy
PASS TestDeploymentHealthcheckRequiresHealthyResponse/unavailable
PASS TestDeploymentHealthcheckRequiresHealthyResponse/wrongstatus
PASS TestDeploymentHealthcheckRequiresHealthyResponse/invalidjson
PASS TestDeploymentHealthcheckRequiresHealthyResponse/redirect
PASS TestDeploymentHealthcheckRequiresHealthyResponse
PASS TestPasswordlessServerIsLoopbackOnly
PASS TestRedisProjectionIsDeduplicatedBoundedAndMerchantScoped
PASS TestRedisProjectionTypeErrorCanRecoverWithoutLosingEvent
PASS TestRedisLimiter300PerMinuteAndIdentityIsolation
PASS TestMySQLHTTPInvalidInputs/missing_identity
PASS TestMySQLHTTPInvalidInputs/forged_identity
PASS TestMySQLHTTPInvalidInputs/reader
PASS TestMySQLHTTPInvalidInputs/reader_invalid_body
PASS TestMySQLHTTPInvalidInputs/amount_field
PASS TestMySQLHTTPInvalidInputs/merchant_field
PASS TestMySQLHTTPInvalidInputs/unknown_field
PASS TestMySQLHTTPInvalidInputs/trailing_object
PASS TestMySQLHTTPInvalidInputs/non_string
PASS TestMySQLHTTPInvalidInputs/array
PASS TestMySQLHTTPInvalidInputs/null
PASS TestMySQLHTTPInvalidInputs/empty_reason
PASS TestMySQLHTTPInvalidInputs/missing_key
PASS TestMySQLHTTPInvalidInputs/bad_utf8
PASS TestMySQLHTTPInvalidInputs/oversized_body
PASS TestMySQLHTTPInvalidInputs
PASS TestMySQLNormalIdempotencyAndExactParameters
PASS TestRedisCommitFailureRetriesSameEventWithoutDuplicateActivity
PASS TestMySQLFailureAndUnpaidAreNotRetried
PASS TestMySQLUnknownSurvivesReopenAndSeed
PASS TestMySQLHTTPAuthorizationAndIsolation/GET/api/orders
PASS TestMySQLHTTPAuthorizationAndIsolation/GET/api/orders/A-1001demo-b-write
PASS TestMySQLHTTPAuthorizationAndIsolation/POST/api/orders/A-1001/refundsdemo-b-write
PASS TestMySQLHTTPAuthorizationAndIsolation/GET/api/refunds/rf_YUFIQHNCR2OYCFY6WT6OWMFVPZdemo-b-write
PASS TestMySQLHTTPAuthorizationAndIsolation/POST/api/refunds/rf_YUFIQHNCR2OYCFY6WT6OWMFVPZ/reconciledemo-b-write
PASS TestMySQLHTTPAuthorizationAndIsolation/POST/api/refunds/rf_YUFIQHNCR2OYCFY6WT6OWMFVPZ/reconciledemo-a-read
PASS TestMySQLHTTPAuthorizationAndIsolation/GET/api/refunds/rf_YUFIQHNCR2OYCFY6WT6OWMFVPZdemo-a-read
PASS TestMySQLHTTPAuthorizationAndIsolation/GET/api/orders/A-1001demo-a-read
PASS TestMySQLHTTPAuthorizationAndIsolation/POST/api/orders/A-1002/refundsdemo-a-read
PASS TestMySQLHTTPAuthorizationAndIsolation/GET/api/refunds/nonexistentdemo-a-write
PASS TestMySQLHTTPAuthorizationAndIsolation/POST/api/orders/A-1002/refundsdemo-a-write
PASS TestMySQLHTTPAuthorizationAndIsolation/POST/api/orders/A-1002/refundsdemo-a-write#01
PASS TestMySQLHTTPAuthorizationAndIsolation
PASS TestMySQLReadOnlyCrossMerchantWritesReturn404
PASS TestMySQLHTTPConcurrentSameKey
PASS TestRedisFailureRecoveryDoesNotSkipKafkaMessage
PASS TestMySQLHTTPConcurrentDifferentKeys
PASS TestMySQLQueryFailuresAndMalformedResultsStayUnknown/not_found
PASS TestMySQLQueryFailuresAndMalformedResultsStayUnknown/network_error
PASS TestMySQLQueryFailuresAndMalformedResultsStayUnknown/parse_error
PASS TestMySQLQueryFailuresAndMalformedResultsStayUnknown/wrong_request
PASS TestKafkaRedisRealDuplicateDelivery
PASS TestDecodeEventRejectsInvalidPayloads
PASS TestRedisAbsentDegradesActivityAndFailsOpenLimiter
PASS TestLimiterKeysDoNotStoreCredentialsAndRespectNamespaces
PASS TestOutboxMarksOnlyAfterKafkaAcknowledgement
PASS TestKafkaFailureKeepsOutboxUnpublished
PASS TestMarkFailureRedeliversSameEventID
PASS TestOutboxIdentityMismatchIsNotPublished
PASS TestRedisFailureDoesNotCommitOrFetchNextKafkaMessage
PASS TestMySQLQueryFailuresAndMalformedResultsStayUnknown/wrong_order
PASS TestMySQLQueryFailuresAndMalformedResultsStayUnknown/wrong_amount
PASS TestMySQLQueryFailuresAndMalformedResultsStayUnknown/invalid_status
PASS TestMySQLQueryFailuresAndMalformedResultsStayUnknown
PASS TestMySQLTerminalCannotRegress/A-1001
PASS TestMySQLTerminalCannotRegress/A-1003
PASS TestMySQLTerminalCannotRegress
PASS TestMySQLReconcileSerializesWithInFlightSubmit
PASS TestMySQLChannelIdempotencyAndDurableLostResponse
PASS TestMySQLChannelConcurrentSameRequest
PASS TestMySQLSeedPreservesExistingBusinessAndScenarioValues
PASS TestAuthSecureDefaultsAndTokenEncoding
PASS TestAuthOriginsAreExactAndRequired
PASS TestMySQLAuthLoginSessionPersistsOnlyHashAndRestores
PASS TestMySQLAuthRejectsForgedElevatedAndDisabledLegacyCredentials
PASS TestMySQLAuthCSRFOriginPermissionsAndMerchantIsolation
PASS TestMySQLAuthRotationLogoutExpiryAndCrossTabBinding
PASS TestMySQLAuthLoginRateLimitIgnoresForwardedFor
PASS TestMySQLAuthConcurrentLoginSessionsRemainBounded
PASS TestMySQLAuthOuterMiddlewareProtectsProjectionAndCommitFailure
PASS TestDeploymentOriginsRejectUnsafeConfiguration
PASS TestDeploymentCookieTransportConfiguration
PASS TestDeploymentRequestDeadlineBoundsDatabaseWork
PASS TestDeploymentDiagnosticsNeverEchoSecrets
PASS TestDeploymentOriginsReplaceDefaultsAndIgnoreForwardedHeaders
PASS TestDeploymentDatabaseFieldsAndSecretSafeErrors
PASS TestDeploymentResetProvenanceExactDockerOptIn
PASS TestMySQLDeploymentSecureCookieAndCustomOrigin
PASS TestResetDSNProvenanceIsLocalAndAllowlisted
PASS TestMySQLResetDefaultOffAndOptInPermissions
PASS TestMySQLResetArchivesExactMerchantAndRestoresFixtures
PASS TestMySQLResetBackupAndDeleteFailuresRollBackAtomically/backup
PASS TestMySQLResetBackupAndDeleteFailuresRollBackAtomically/delete
PASS TestMySQLResetBackupAndDeleteFailuresRollBackAtomically
PASS TestMySQLResetWaitsForChannelAndArchivesFinalOutcome
PASS TestMySQLResetAdvisoryLockIsCrossConnectionAndBusyIsExplicit
PASS TestMySQLResetReadsHaveConsistentVersionAndPersistWatermark
PASS TestMySQLResetDoesNotStarvePoolWithManyWaitingMutations
```

## 7. 前端逐项结果

```text
PASS sends same-origin cookies, CSRF, signal and idempotency key, never Bearer or client amount/merchant
PASS binds business GETs to the current CSRF/session and does not send auth header on restoration
PASS only sends an identity selection during login and attaches CSRF to logout
PASS preserves server error code and status
PASS sends only the explicit confirmation to the merchant-scoped reset endpoint
PASS treats unparseable response as an error, not a successful refund
PASS uses the official local wordmark on the visible login/sidebar entry (authenticated: false)
PASS uses the official local wordmark on the visible login/sidebar entry (authenticated: true)
PASS shows an independent login page without loading any business data when no session exists
PASS does not expose business UI while session restoration is pending, then renders real empty components
PASS submits the selected read-only identity and hides refund write actions using server permissions
PASS loads orders on the first real login without a page refresh (randomUUID available: true)
PASS loads orders on the first real login without a page refresh (randomUUID available: false)
PASS hides channel reconciliation for read-only users even with an unresolved refund
PASS clears the ERP immediately on logout and offers retry without pretending failed revocation succeeded
PASS returns to login when a business API says the session expired
PASS does not render a late old-session order after a cross-tab identity change
PASS renders API failures in the real ERP without crashing AntD empty state
PASS requires typed confirmation in the real reset modal and refreshes the ERP without logging out
PASS does not show data reset to viewers or operators without the server permission
PASS never reads business data before session restoration succeeds
PASS returns to login on an absent or expired session without business requests
PASS logs in with server permissions rather than trusting the selected role
PASS completes login, initial reads and server logout when HTTP has no randomUUID
PASS retains one cryptographic refund key across HTTP network retries without randomUUID
PASS does not let unavailable notification transports interrupt accepted login or server logout
PASS does not send a refund when no cryptographic random source is available
PASS does not let a late invalidated login release the busy state owned by a newer login
PASS releases login busy when the immediately following business read expires its session
PASS checks create and reconcile permissions independently
PASS clears and aborts immediately on logout; blocks login until server revocation completes
PASS keeps the user signed out and blocks new login until failed logout is retried
PASS clears the session and all business data on 401 UNAUTHORIZED
PASS clears the session and all business data on 403 SESSION_CHANGED
PASS finishes local logout without revoking a different shared-cookie principal after stale-CSRF rejection
PASS keeps logout pending if CSRF rejection belongs to the same server session
PASS keeps a valid session on a normal FORBIDDEN response
PASS clears immediately on a cross-tab login or logout notification and ignores late data
PASS does not cancel an unconfirmed logout when another tab broadcasts a change
PASS clears old business data before revalidating on window focus
PASS expires a session locally and removes protected data
PASS preserves same-session retry keys on refresh, but deletes only private intent keys on logout
PASS renders the original local image in the login variant without a duplicate wordmark
PASS renders the original local image in the sidebar variant without a duplicate wordmark
PASS keeps the wordmark contained and unmodified at responsive widths
PASS references the same local PNG for the Vite-processed favicon
PASS formats integer cents 19900 as ¥199.00
PASS formats integer cents 8800 as ¥88.00
PASS formats integer cents 1 as ¥0.01
PASS formats integer cents 0 as ¥0.00
PASS formats integer cents 123456789 as ¥1,234,567.89
PASS does not silently round invalid cents
PASS displays timestamps explicitly in Beijing time
PASS renders PROCESSING with a human-readable status
PASS renders UNKNOWN with a human-readable status
PASS renders SUCCEEDED with a human-readable status
PASS renders FAILED with a human-readable status
PASS renders no-refund status without confusing it with pending
PASS reloads confirmed reset data on HTTP even when notification transports fail: false
PASS reloads confirmed reset data on HTTP even when notification transports fail: true
PASS requires the server permission and the exact confirmation phrase
PASS blocks reset while a refund is in flight
PASS keeps the session, clears caches/drafts, aborts old reads and reloads the new version after success
PASS never claims success after an uncertain network result, and lets a fresh GET resolve the visible state
PASS shows an explicit server refusal rather than claiming a reset succeeded
PASS ignores an old reset result after logout and another merchant login
PASS accepts a higher GET version even when a reset notification was missed, clearing cached terminal refunds
PASS rejects lower-version list, detail and activity responses instead of reviving deleted refunds
PASS invalidates a late old refund write when a newer GET detects a reset
PASS handles a same-merchant reset broadcast immediately and ignores an older late write
PASS ignores other-merchant or duplicate reset broadcasts
PASS does not retain a reset loading lock if a concurrent later reset is detected during refresh
PASS registers every Ant Design template component without a whole-library plugin
PASS keeps programmatic dialogs and unused input variants out of the registered surface
PASS uses the native UUID implementation when available
PASS constructs RFC 4122 version and variant bits from 16 cryptographically random bytes
PASS fails closed without secure randomness rather than creating weak idempotency keys
PASS clears old identity data immediately and ignores late list, detail and activity responses
PASS ignores a late mutation response after identity switch
PASS keeps the newest order selection when an earlier detail arrives late
PASS keeps the latest refresh when an older request finishes last
PASS does not allow a late list snapshot without a refund to erase a completed submit
PASS does not allow a late detail snapshot PROCESSING to regress a reconciled terminal result
PASS does not allow a late detail snapshot UNKNOWN to regress a reconciled terminal result
PASS prefers newer nonterminal refund timestamps over a late old detail response
PASS allows another selected order to finish loading while an earlier order mutation completes
PASS preserves the original idempotency key AND reason after a network failure
PASS restores an uncertain intent across a page recreation without changing the key
PASS does not submit duplicate in-flight actions
PASS blocks read-only writes and unpaid order submissions before calling the API
PASS does not resubmit or reconcile terminal SUCCEEDED
PASS does not resubmit or reconcile terminal FAILED
PASS queries UNKNOWN through reconcile only
PASS queries PROCESSING through reconcile only
PASS retains UNKNOWN on a query network failure instead of declaring failure
PASS validates blank and Unicode-length reasons
PASS updates the selected detail on global refresh when another tab completed a refund
PASS updates list rows and counts when the selected detail discovers an external terminal result
PASS does not overwrite or finish a different order detail that is still loading during global refresh
PASS keeps both views terminal when a stale list refresh arrives after a newer detail
PASS accepts an empty order list
PASS shows API errors without hiding them behind a fake empty-success state
PASS does not change business data when the auxiliary activity endpoint fails
```

## 8. Python 守卫逐项结果

```text
PASS test_public_release_check.CIContractTests.test_workflow_is_read_only_pinned_and_does_not_deploy
PASS test_public_release_check.CIContractTests.test_workflow_runs_linux_start_and_scoped_real_deployment_acceptance
PASS test_public_release_check.ContentTests.test_absolute_user_paths
PASS test_public_release_check.ContentTests.test_binary_non_utf8_oversize_and_unknown_source_fail_closed
PASS test_public_release_check.ContentTests.test_demo_passwords_hashes_placeholders_urls_are_not_secrets
PASS test_public_release_check.ContentTests.test_high_confidence_credential_patterns
PASS test_public_release_check.ContentTests.test_reports_do_not_contain_matched_values_or_line_contents
PASS test_public_release_check.ContentTests.test_sensitive_paths_are_rejected_without_examining_contents
PASS test_public_release_check.ContentTests.test_unsafe_filenames_cannot_inject_output_or_escape
PASS test_public_release_check.GitIndexTests.test_allowed_source_is_scanned
PASS test_public_release_check.GitIndexTests.test_empty_index_and_ancestor_repository_fail_closed
PASS test_public_release_check.GitIndexTests.test_index_symlink_is_rejected_without_reading_target
PASS test_public_release_check.GitIndexTests.test_missing_git_index_does_not_fall_back_to_worktree
PASS test_public_release_check.GitIndexTests.test_packaging_api_returns_checked_bytes_not_later_worktree_content
SKIP test_public_release_check.GitIndexTests.test_source_symlink_is_rejected_without_following -- symlinks unavailable on this platform
PASS test_public_release_check.GitIndexTests.test_staged_private_path_is_rejected_even_if_worktree_copy_is_absent
PASS test_public_release_check.GitIndexTests.test_staged_secret_is_detected_when_worktree_has_been_cleaned
PASS test_public_release_check.GitIndexTests.test_unresolved_index_stages_are_not_published
PASS test_public_release_check.GitIndexTests.test_unstaged_secret_does_not_replace_the_safe_index_blob
PASS test_public_release_check.GitIndexTests.test_worktree_is_explicit_allowlist_and_does_not_read_private_data
PASS test_public_release_check.PinnedBrandAssetTests.test_binary_in_regular_source_remains_rejected
PASS test_public_release_check.PinnedBrandAssetTests.test_docker_context_has_only_exact_image_exception
PASS test_public_release_check.PinnedBrandAssetTests.test_exact_original_is_the_only_pinned_binary
PASS test_public_release_check.PinnedBrandAssetTests.test_index_checks_index_bytes_not_safe_or_changed_worktree
PASS test_public_release_check.PinnedBrandAssetTests.test_other_image_paths_do_not_inherit_exception
PASS test_public_release_check.PinnedBrandAssetTests.test_replaced_truncated_appended_and_same_length_changed_content_is_rejected
PASS test_public_release_check.PinnedBrandAssetTests.test_worktree_packaging_returns_original_bytes_and_rejects_replacement
PASS test_public_release_check.WorktreeTests.test_allowed_source_is_scanned
PASS test_public_release_check.WorktreeTests.test_missing_git_index_does_not_fall_back_to_worktree
PASS test_public_release_check.WorktreeTests.test_packaging_api_returns_checked_bytes_not_later_worktree_content
SKIP test_public_release_check.WorktreeTests.test_source_symlink_is_rejected_without_following -- symlinks unavailable on this platform
PASS test_public_release_check.WorktreeTests.test_worktree_is_explicit_allowlist_and_does_not_read_private_data
PASS test_verify.ReportTests.test_complete_checklist_requires_frontend_and_go
PASS test_verify.ReportTests.test_frontend_failed_phase_is_fail_not_not_run
PASS test_verify.ReportTests.test_frontend_failure_skip_empty_and_missing_cases_rejected
PASS test_verify.ReportTests.test_frontend_pass_is_component_not_browser
PASS test_verify.ReportTests.test_go_pass_and_real_middleware_assertion_required
PASS test_verify.ReportTests.test_go_skip_failure_empty_or_incomplete_is_not_pass
PASS test_verify.ReportTests.test_repeated_assertion_failure_cannot_be_hidden_by_later_pass
PASS test_verify.ReportTests.test_unexecuted_checklist_is_not_pass
PASS test_verify.SafetyTests.test_container_cleanup_refuses_wrong_owner
PASS test_verify.SafetyTests.test_existing_project_refused
PASS test_verify.SafetyTests.test_generated_passwords_are_redacted
PASS test_verify.SafetyTests.test_generated_project_is_reserved_unique_and_has_no_target_argument
PASS test_verify.SafetyTests.test_inherited_compose_and_test_overrides_are_removed
PASS test_verify.SafetyTests.test_linked_runtime_refused_without_following_it
PASS test_verify.SafetyTests.test_remote_docker_refused_before_info_or_any_resource_creation
PASS test_verify.SafetyTests.test_sql_inspection_does_not_migrate_and_has_canonical_order
PASS test_verify.SafetyTests.test_sql_refuses_unverified_target
PASS test_verify.SafetyTests.test_stop_never_removes_volumes_or_stops_a_foreign_project
PASS test_web_container.WebContainerContractTests.test_compression_applies_only_to_public_static_assets
PASS test_web_container.WebContainerContractTests.test_context_is_a_closed_allowlist_with_sensitive_paths_excluded
PASS test_web_container.WebContainerContractTests.test_locked_frontend_build_and_minimal_nonroot_runtime
PASS test_web_container.WebContainerContractTests.test_official_bases_are_tag_and_digest_pinned
PASS test_web_container.WebContainerContractTests.test_proxy_preserves_paths_and_never_retries_or_reflects_origin
PASS test_web_container.WebContainerContractTests.test_readonly_compatible_paths_and_healthcheck
PASS test_web_container.WebContainerContractTests.test_security_headers_and_selective_asset_caching
```

## 9. 有界只读基线

- 数据量：固定 5 笔虚构订单（A 4 / B 1）。
- 接口：`GET /api/orders`；请求数 160，并发 8；错误 0，错误率 0%。
- 延迟（nearest-rank）：p50 5.804 ms，p95 11.351 ms，最大 22.083 ms。
- 请求段耗时 0.138 秒；前置限流窗口等待 51.473 秒不计入请求段。
- 仅本机小样本只读基线，不能外推退款写入吞吐或生产 SLA。

## 10. 复现与隐私

- 复现和九项清单的 UI/API 分工：[自动验收说明](AUTOMATED_ACCEPTANCE.md)。
- 本公开副本采用字段白名单，不是简单将原始日志改名。未发布实际服务器地址、用户路径、会话、云/Registry 密钥、数据库密码或环境文件。
- 原始 `.runtime/`、`evidence/`、日志/数据库文件仍被忽略，不会为了提交日志扩大公开源码白名单。
