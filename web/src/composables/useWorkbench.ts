import { computed, ref } from 'vue'
import { api as defaultApi, ApiError, type WorkbenchApi } from '../lib/api'
import type { Activity, AuthSession, IdentityId, Order, Permission, Refund } from '../lib/types'

interface Intent { key: string; reason: string }
const storagePrefix = 'yishuld:refund-intent:v1:'
const sessionOwnerKey = 'yishuld:session-owner:v1'

export function useWorkbench(api: WorkbenchApi = defaultApi, storage: Storage | undefined = globalThis.sessionStorage) {
  const session = ref<AuthSession | null>(null)
  const authLoading = ref(true)
  const authBusy = ref(false)
  const authError = ref('')
  const logoutPending = ref(false)
  let pendingLogoutSession: AuthSession | null = null
  let expiryTimer: ReturnType<typeof setTimeout> | undefined
  const orders = ref<Order[]>([])
  const selectedId = ref<string | null>(null)
  const detail = ref<Order | null>(null)
  const activities = ref<Activity[]>([])
  const activityAvailable = ref(true)
  const listLoading = ref(false)
  const detailLoading = ref(false)
  const activityLoading = ref(false)
  const actionLoading = ref(false)
  const resetLoading = ref(false)
  const resetError = ref('')
  const resetNotice = ref('')
  const dataVersion = ref<number | null>(null)
  const listError = ref('')
  const detailError = ref('')
  const actionError = ref('')
  const notice = ref('')
  const pendingIntent = ref<Intent | null>(null)
  const user = computed(() => session.value?.user ?? null)
  const hasPermission = (permission: Permission) => session.value?.user.permissions.includes(permission) ?? false
  const canRead = computed(() => hasPermission('orders:read') && hasPermission('refunds:read'))
  const canCreate = computed(() => hasPermission('refunds:create'))
  const canReconcile = computed(() => hasPermission('refunds:reconcile'))
  const canReset = computed(() => hasPermission('data:reset'))
  let generation = 0
  let listSequence = 0
  let detailSequence = 0
  let activitySequence = 0
  const controllers = new Set<AbortController>()
  const intents = new Map<string, Intent>()
  const knownRefunds = new Map<string, Refund>()

  // A GET started before a mutation can contain an older snapshot. Keep already-observed
  // refund progress monotonic in the UI; this cache never authorizes a financial action.
  function latestRefund(current: Refund | undefined, incoming: Refund | null): Refund | null {
    if (!current) return incoming
    if (!incoming || current.refund_id !== incoming.refund_id) return current
    const terminal = (refund: Refund) => ['SUCCEEDED', 'FAILED'].includes(refund.status)
    if (terminal(current)) {
      if (incoming.status !== current.status) return current
      return Date.parse(incoming.updated_at) > Date.parse(current.updated_at) ? incoming : current
    }
    if (terminal(incoming)) return incoming
    const currentTime = Date.parse(current.updated_at), incomingTime = Date.parse(incoming.updated_at)
    if (incomingTime < currentTime) return current
    if (incomingTime === currentTime && current.status === 'UNKNOWN' && incoming.status === 'PROCESSING') return current
    return incoming
  }
  function rememberOrder(order: Order): Order {
    const refund = latestRefund(knownRefunds.get(order.order_id), order.refund)
    if (refund) knownRefunds.set(order.order_id, refund)
    return { ...order, refund }
  }

  function context() {
    const controller = new AbortController()
    controllers.add(controller)
    let gen = generation
    return {
      ctx: { csrfToken: session.value?.csrf_token, signal: controller.signal },
      current: () => gen === generation,
      adoptGeneration: () => { gen = generation },
      done: () => controllers.delete(controller),
    }
  }

  function intentId(orderId: string) { return `${storagePrefix}${session.value?.user.merchant_id}:${orderId}` }
  function loadIntent(orderId: string): Intent | null {
    const id = intentId(orderId)
    if (intents.has(id)) return intents.get(id)!
    try {
      const raw = storage?.getItem(id)
      if (!raw) return null
      const value: unknown = JSON.parse(raw)
      if (value && typeof value === 'object' && 'key' in value && 'reason' in value && typeof value.key === 'string' && typeof value.reason === 'string') {
        const intent = { key: value.key, reason: value.reason }
        intents.set(id, intent)
        return intent
      }
    } catch { /* Storage may be disabled; the in-memory intent still prevents retry key changes. */ }
    return null
  }
  function saveIntent(orderId: string, intent: Intent) {
    const id = intentId(orderId)
    intents.set(id, intent)
    try { storage?.setItem(id, JSON.stringify(intent)) } catch { /* In-memory fallback. */ }
  }
  function clearIntent(orderId: string) {
    const id = intentId(orderId)
    intents.delete(id)
    try { storage?.removeItem(id) } catch { /* In-memory fallback. */ }
    if (selectedId.value === orderId) pendingIntent.value = null
  }
  function errorMessage(error: unknown) {
    return error instanceof ApiError ? error.message : '网络连接异常，请稍后重试。'
  }
  function isAbort(error: unknown) { return error instanceof Error && error.name === 'AbortError' }
  function acceptDataVersion(version: number, req: ReturnType<typeof context>) {
    if (!Number.isSafeInteger(version) || version < 0) throw new ApiError(200, 'INVALID_RESPONSE', '订单数据版本异常，请刷新重试。')
    if (dataVersion.value !== null && version < dataVersion.value) return 'stale'
    if (dataVersion.value !== null && version > dataVersion.value) {
      clearBusinessForReset()
      authBusy.value = false
      dataVersion.value = version
      // This response is the first authoritative read of the new data generation.
      // Other old reads/writes stay invalidated; only this completed read is adopted.
      req.adoptGeneration()
      return 'advanced'
    }
    dataVersion.value = version
    return 'current'
  }

  async function refreshOrders() {
    if (resetLoading.value) return
    await loadOrders()
  }
  async function loadOrders() {
    if (!canRead.value) return
    const req = context(), seq = ++listSequence
    listLoading.value = true
    listError.value = ''
    try {
      const result = await api.orders(req.ctx)
      if (req.current() && seq === listSequence) {
        if (result.length && result.some((order) => order.data_version !== result[0]!.data_version)) {
          throw new ApiError(200, 'INVALID_RESPONSE', '订单数据版本不一致，请刷新重试。')
        }
        const versionState = result.length ? acceptDataVersion(result[0]!.data_version, req) : 'current'
        if (versionState === 'stale') {
          if (!orders.value.length) listError.value = '订单数据已更新，请刷新订单。'
          return
        }
        orders.value = result.map(rememberOrder)
        // The list endpoint includes complete order/refund data. A global refresh must
        // also close a now-stale application form when another tab completed a refund.
        // Do not replace the separate detail request while it is loading another order.
        if (!detailLoading.value && detail.value?.order_id === selectedId.value) {
          const selected = orders.value.find((order) => order.order_id === selectedId.value)
          if (selected) {
            detail.value = selected
            if (selected.refund) {
              clearIntent(selected.order_id)
              actionError.value = ''
            }
          }
        }
        if (versionState === 'advanced') void refreshActivity()
      }
    } catch (error) {
      if (req.current() && handleUnauthorized(error)) return
      if (req.current() && seq === listSequence && !isAbort(error)) listError.value = errorMessage(error)
    } finally {
      req.done()
      if (req.current() && seq === listSequence) listLoading.value = false
    }
  }

  async function selectOrder(orderId: string | null) {
    if (!canRead.value || resetLoading.value) return
    const req = context(), seq = ++detailSequence
    selectedId.value = orderId
    detail.value = null
    detailError.value = ''
    actionError.value = ''
    notice.value = ''
    pendingIntent.value = orderId ? loadIntent(orderId) : null
    detailLoading.value = !!orderId
    if (!orderId) { req.done(); return }
    try {
      const result = await api.order(orderId, req.ctx)
      if (req.current() && seq === detailSequence) {
        const versionState = acceptDataVersion(result.data_version, req)
        if (versionState === 'stale') {
          detailError.value = '订单数据已更新，请刷新后重试。'
          return
        }
        selectedId.value = orderId
        detail.value = rememberOrder(result)
        const current = detail.value
        orders.value = orders.value.map((order) => order.order_id === current.order_id ? current : order)
        if (detail.value.refund) clearIntent(orderId)
        if (versionState === 'advanced') void refreshAll()
      }
    } catch (error) {
      if (req.current() && handleUnauthorized(error)) return
      if (req.current() && seq === detailSequence && !isAbort(error)) detailError.value = errorMessage(error)
    } finally {
      req.done()
      if (req.current() && seq === detailSequence) detailLoading.value = false
    }
  }

  async function refreshActivity() {
    if (resetLoading.value) return
    await loadActivity()
  }
  async function loadActivity() {
    if (!canRead.value) return
    const req = context(), seq = ++activitySequence
    activityLoading.value = true
    try {
      const result = await api.activity(req.ctx)
      if (req.current() && seq === activitySequence) {
        const versionState = acceptDataVersion(result.data_version, req)
        if (versionState === 'stale') return
        activities.value = result.items
        activityAvailable.value = result.available
        if (versionState === 'advanced') void refreshOrders()
      }
    } catch (error) {
      if (req.current() && handleUnauthorized(error)) return
      if (req.current() && seq === activitySequence && !isAbort(error)) {
        activities.value = []
        activityAvailable.value = false
      }
    } finally {
      req.done()
      if (req.current() && seq === activitySequence) activityLoading.value = false
    }
  }

  function applyRefund(orderId: string, refund: Refund) {
    const latest = latestRefund(knownRefunds.get(orderId), refund)!
    knownRefunds.set(orderId, latest)
    orders.value = orders.value.map((order) => order.order_id === orderId ? { ...order, refund: latest } : order)
    if (detail.value?.order_id === orderId) detail.value = { ...detail.value, refund: latest }
    clearIntent(orderId)
  }

  async function submitRefund(reason: string) {
    const order = detail.value
    if (!order || !canCreate.value || actionLoading.value || resetLoading.value || order.refund || order.status !== 'PAID') return false
    const old = loadIntent(order.order_id)
    const intent = old ?? { key: crypto.randomUUID(), reason }
    if (!intent.reason.trim() || Array.from(intent.reason).length > 200) {
      actionError.value = '请填写 1–200 个字符的退款原因，不能仅为空格。'
      return false
    }
    // Persist BEFORE sending. Unknown transport outcomes must retry the exact original intent.
    saveIntent(order.order_id, intent)
    pendingIntent.value = intent
    const req = context()
    actionLoading.value = true
    actionError.value = ''
    notice.value = ''
    try {
      const refund = await api.submit(order.order_id, intent.reason, intent.key, req.ctx)
      if (!req.current()) return false
      applyRefund(order.order_id, refund)
      if (selectedId.value === order.order_id) notice.value = '申请已记录。请以当前退款状态为准。'
      void refreshActivity()
      return true
    } catch (error) {
      if (req.current() && handleUnauthorized(error)) return false
      if (req.current() && selectedId.value === order.order_id && !isAbort(error)) {
        actionError.value = `${errorMessage(error)} 原申请已保留，请先刷新查询；重试不会创建新的退款申请。`
      }
      return false
    } finally {
      req.done()
      if (req.current()) actionLoading.value = false
    }
  }

  async function reconcile() {
    const order = detail.value
    if (!order?.refund || !canReconcile.value || actionLoading.value || resetLoading.value || !['UNKNOWN', 'PROCESSING'].includes(order.refund.status)) return
    const req = context()
    actionLoading.value = true
    actionError.value = ''
    notice.value = ''
    try {
      const refund = await api.reconcile(order.refund.refund_id, req.ctx)
      if (!req.current()) return
      applyRefund(order.order_id, refund)
      if (selectedId.value === order.order_id) notice.value = '已查询原渠道请求；未重新提交退款。'
      void refreshActivity()
    } catch (error) {
      if (req.current() && handleUnauthorized(error)) return
      if (req.current() && selectedId.value === order.order_id && !isAbort(error)) actionError.value = `${errorMessage(error)} 未取得明确结果，原退款状态不变。`
    } finally {
      req.done()
      if (req.current()) actionLoading.value = false
    }
  }

  async function refreshAll() { await Promise.all([refreshOrders(), refreshActivity()]) }
  function clearBusinessForReset() {
    generation++
    controllers.forEach((controller) => controller.abort())
    controllers.clear()
    knownRefunds.clear()
    clearPrivateIntents()
    orders.value = []
    selectedId.value = null
    detail.value = null
    activities.value = []
    pendingIntent.value = null
    listError.value = detailError.value = actionError.value = notice.value = ''
    listLoading.value = detailLoading.value = actionLoading.value = activityLoading.value = false
    resetLoading.value = false
    activityAvailable.value = true
  }
  async function resetData(confirmation: string) {
    if (!canReset.value || resetLoading.value || actionLoading.value || authBusy.value || authLoading.value) return false
    if (confirmation !== '恢复初始数据') {
      resetError.value = '请输入“恢复初始数据”以确认操作。'
      return false
    }
    clearBusinessForReset()
    resetLoading.value = true
    resetError.value = resetNotice.value = ''
    const req = context(), merchantId = session.value!.user.merchant_id
    try {
      const result = await api.reset(confirmation, req.ctx)
      if (!req.current()) return false
      if (result.merchant_id !== merchantId || !Number.isSafeInteger(result.order_count) || result.order_count < 0 ||
          !Number.isSafeInteger(result.data_version) || result.data_version < 0 ||
          (dataVersion.value !== null && result.data_version <= dataVersion.value)) {
        throw new ApiError(200, 'INVALID_RESPONSE', '恢复结果无法确认。')
      }
      dataVersion.value = result.data_version
      resetNotice.value = `已恢复 ${result.order_count} 笔初始订单。`
      notifyDataReset(merchantId, result.data_version)
      await Promise.all([loadOrders(), loadActivity()])
      return req.current()
    } catch (error) {
      if (req.current() && handleUnauthorized(error)) return false
      if (req.current() && !isAbort(error)) {
        const rejected = error instanceof ApiError && error.status >= 400 && error.status < 500
        resetError.value = rejected ? errorMessage(error) : '恢复结果尚未确认，请刷新订单核实；如需重试，请再次确认。'
      }
      return false
    } finally {
      req.done()
      if (req.current()) resetLoading.value = false
    }
  }
  function clearPrivateIntents() {
    intents.clear()
    try {
      if (!storage) return
      for (let index = storage.length - 1; index >= 0; index--) {
        const key = storage.key(index)
        if (key?.startsWith(storagePrefix) || key === sessionOwnerKey) storage.removeItem(key)
      }
    } catch { /* Storage may be disabled. */ }
  }
  function clearSession(message = '', preserveIntents = false) {
    generation++
    controllers.forEach((controller) => controller.abort())
    controllers.clear()
    knownRefunds.clear()
    if (!preserveIntents) clearPrivateIntents()
    clearTimeout(expiryTimer)
    session.value = null
    dataVersion.value = null
    orders.value = []
    selectedId.value = null
    detail.value = null
    activities.value = []
    pendingIntent.value = null
    listError.value = detailError.value = actionError.value = notice.value = ''
    listLoading.value = detailLoading.value = actionLoading.value = activityLoading.value = false
    resetLoading.value = false
    resetError.value = resetNotice.value = ''
    activityAvailable.value = true
    authLoading.value = false
    authBusy.value = false
    authError.value = message
  }

  const syncKey = 'yishuld:session-change:v1'
  const resetSyncKey = 'yishuld:data-reset:v1'
  const channel = typeof window.BroadcastChannel === 'function' ? new window.BroadcastChannel(syncKey) : null
  function notifySessionChange() {
    const event = { type: 'session-change', nonce: crypto.randomUUID() }
    channel?.postMessage(event)
    try { localStorage.setItem(syncKey, JSON.stringify(event)) } catch { /* BroadcastChannel is the fallback. */ }
  }
  function notifyDataReset(merchantId: string, version: number) {
    const event = { type: 'data-reset', merchant_id: merchantId, data_version: version, nonce: crypto.randomUUID() }
    channel?.postMessage(event)
    try { localStorage.setItem(resetSyncKey, JSON.stringify(event)) } catch { /* BroadcastChannel fallback. */ }
  }
  function remoteDataReset(merchantId: unknown, version: unknown) {
    if (typeof merchantId !== 'string' || merchantId !== user.value?.merchant_id || resetLoading.value) return
    if (typeof version !== 'number' || !Number.isSafeInteger(version) || version < 0) return
    if (dataVersion.value !== null && version <= dataVersion.value) return
    clearBusinessForReset()
    dataVersion.value = version
    // If login was only waiting on business reads, its accepted cookie remains valid.
    authBusy.value = false
    resetError.value = ''
    resetNotice.value = '当前企业数据已在其他页面恢复，正在刷新订单。'
    void refreshAll()
  }
  function remoteSessionChange() {
    if (logoutPending.value) return // Do not race a new login against our unconfirmed logout.
    clearSession('登录状态已在其他页面变更，请重新登录。')
    authBusy.value = false
    logoutPending.value = false
    pendingLogoutSession = null
  }
  function onStorage(event: StorageEvent) {
    if (event.key === syncKey && event.newValue) remoteSessionChange()
    if (event.key === resetSyncKey && event.newValue) {
      try { const data = JSON.parse(event.newValue); remoteDataReset(data.merchant_id, data.data_version) } catch { /* Ignore invalid notifications. */ }
    }
  }
  function revalidateOnFocus() {
    if (session.value && !authBusy.value && !authLoading.value && !resetLoading.value) void restoreSession()
  }
  function onVisibility() { if (document.visibilityState === 'visible') revalidateOnFocus() }
  if (channel) channel.onmessage = (event) => {
    if (event.data?.type === 'data-reset') remoteDataReset(event.data.merchant_id, event.data.data_version)
    else if (event.data?.type === 'session-change') remoteSessionChange()
  }
  window.addEventListener('storage', onStorage)
  window.addEventListener('focus', revalidateOnFocus)
  document.addEventListener('visibilitychange', onVisibility)

  function handleUnauthorized(error: unknown): boolean {
    if (!(error instanceof ApiError) || !(error.status === 401 || error.code === 'SESSION_CHANGED')) return false
    clearSession(error.code === 'SESSION_CHANGED' ? '登录身份已变更，请重新登录。' : '登录已失效，请重新登录。')
    notifySessionChange()
    return true
  }
  function acceptSession(value: AuthSession): boolean {
    const remaining = Date.parse(value.expires_at) - Date.now()
    if (!Number.isFinite(remaining) || remaining <= 0) {
      clearSession('登录已过期，请重新登录。')
      return false
    }
    // A non-secret owner marker preserves a same-session refresh, but never another
    // user's stored refund reason after a cookie/session change.
    const owner = JSON.stringify({ identity: value.user.identity_id, merchant: value.user.merchant_id, expires: value.expires_at })
    try {
      if (storage?.getItem(sessionOwnerKey) !== owner) clearPrivateIntents()
      storage?.setItem(sessionOwnerKey, owner)
    } catch { /* Storage may be disabled. */ }
    session.value = value
    expiryTimer = setTimeout(() => {
      clearSession('登录已过期，请重新登录。')
      notifySessionChange()
    }, Math.min(remaining, 2_147_483_647))
    return true
  }
  async function restoreSession() {
    if (authBusy.value || logoutPending.value || resetLoading.value) return
    clearSession('', true)
    authLoading.value = true
    const req = context()
    try {
      const value = await api.session(req.ctx)
      if (!req.current()) return
      authLoading.value = false
      if (acceptSession(value)) await refreshAll()
    } catch (error) {
      if (req.current() && !isAbort(error)) {
        clearSession(error instanceof ApiError && error.status === 401 ? '' : '暂时无法确认登录状态，请重试或重新登录。')
      }
    } finally {
      req.done()
      if (req.current()) authLoading.value = false
    }
  }
  async function login(identityId: IdentityId) {
    if (authBusy.value || authLoading.value || logoutPending.value) return false
    clearSession()
    authBusy.value = true
    const req = context()
    try {
      const value = await api.login(identityId, req.ctx)
      if (!req.current()) return false
      if (!acceptSession(value)) return false
      notifySessionChange()
      await refreshAll()
      return !!session.value
    } catch (error) {
      if (req.current() && !isAbort(error)) authError.value = errorMessage(error)
      return false
    } finally {
      req.done()
      // An older request must never release a later login's busy state. clearSession
      // releases an invalidated generation itself (including expiry or a business 401).
      if (req.current()) authBusy.value = false
    }
  }
  async function logout() {
    if (authBusy.value) return
    const previous = session.value ?? pendingLogoutSession
    clearSession()
    notifySessionChange()
    if (!previous) return
    pendingLogoutSession = previous
    logoutPending.value = true
    authBusy.value = true
    const req = context()
    try {
      await api.logout({ ...req.ctx, csrfToken: previous.csrf_token })
      if (req.current()) {
        pendingLogoutSession = null
        logoutPending.value = false
      }
    } catch (error) {
      if (!req.current()) return
      if (error instanceof ApiError && error.status === 401) {
        pendingLogoutSession = null
        logoutPending.value = false
      } else if (error instanceof ApiError && error.code === 'CSRF_REJECTED') {
        // Another tab may have rotated the shared cookie. Never revoke that new
        // principal using a recovered token, and never loop forever on the old token.
        try {
          const current = await api.session({ signal: req.ctx.signal })
          if (!req.current()) return
          if (current.csrf_token !== previous.csrf_token) {
            pendingLogoutSession = null
            logoutPending.value = false
            authError.value = '登录身份已在其他页面变更，请重新登录。'
          } else authError.value = '本页已退出，但服务端注销尚未确认。请重试退出后再登录。'
        } catch (checkError) {
          if (!req.current()) return
          if (checkError instanceof ApiError && checkError.status === 401) {
            pendingLogoutSession = null
            logoutPending.value = false
          } else authError.value = '本页已退出，但服务端注销尚未确认。请重试退出后再登录。'
        }
      } else if (!isAbort(error)) {
        authError.value = '本页已退出，但服务端注销尚未确认。请重试退出后再登录。'
      }
    } finally {
      req.done()
      if (req.current()) authBusy.value = false
    }
  }
  function dispose() {
    generation++
    controllers.forEach((controller) => controller.abort())
    controllers.clear()
    clearTimeout(expiryTimer)
    window.removeEventListener('storage', onStorage)
    window.removeEventListener('focus', revalidateOnFocus)
    document.removeEventListener('visibilitychange', onVisibility)
    channel?.close()
  }

  return { session, user, authLoading, authBusy, authError, logoutPending, restoreSession, login, logout,
    orders, selectedId, detail, activities, activityAvailable, listLoading, detailLoading,
    activityLoading, actionLoading, resetLoading, resetError, resetNotice, dataVersion, canReset, resetData, listError, detailError, actionError, notice, pendingIntent, canRead, canCreate, canReconcile,
    refreshOrders, refreshActivity, refreshAll, selectOrder, submitRefund, reconcile, dispose }
}
