import { afterEach, describe, expect, it, vi } from 'vitest'
import { useWorkbench } from '../composables/useWorkbench'
import { ApiError, type WorkbenchApi } from '../lib/api'
import type { AuthSession, Order } from '../lib/types'
import { authSession, deferred, order, refund, resetResult } from './fixtures'

const instances: ReturnType<typeof useWorkbench>[] = []
function setup() {
  const api = {
    login: vi.fn<WorkbenchApi['login']>().mockImplementation(async (id) => authSession(id)),
    session: vi.fn<WorkbenchApi['session']>().mockResolvedValue(authSession()),
    logout: vi.fn<WorkbenchApi['logout']>().mockResolvedValue(undefined),
    reset: vi.fn<WorkbenchApi['reset']>().mockResolvedValue(resetResult()),
    orders: vi.fn<WorkbenchApi['orders']>().mockResolvedValue([order()]),
    order: vi.fn<WorkbenchApi['order']>().mockResolvedValue(order()),
    activity: vi.fn<WorkbenchApi['activity']>().mockResolvedValue({ items: [], available: true, data_version: 0 }),
    submit: vi.fn<WorkbenchApi['submit']>().mockResolvedValue(refund()),
    reconcile: vi.fn<WorkbenchApi['reconcile']>().mockResolvedValue(refund()),
  }
  const wb = useWorkbench(api); instances.push(wb)
  return { api, wb }
}
afterEach(() => { instances.splice(0).forEach((wb) => wb.dispose()); vi.useRealTimers(); vi.unstubAllGlobals() })

describe('session lifecycle and permissions', () => {
  it('never reads business data before session restoration succeeds', async () => {
    const { api, wb } = setup(), pending = deferred<AuthSession>()
    api.session.mockImplementationOnce(() => pending.promise)
    const restoring = wb.restoreSession()
    await wb.refreshAll(); await wb.selectOrder('A-1001')
    expect(api.orders).not.toHaveBeenCalled(); expect(api.order).not.toHaveBeenCalled()
    expect(wb.authLoading.value).toBe(true)
    pending.resolve(authSession()); await restoring
    expect(api.orders).toHaveBeenCalledOnce(); expect(wb.user.value?.merchant_name).toBe('华东商贸')
  })
  it('returns to login on an absent or expired session without business requests', async () => {
    const { api, wb } = setup()
    api.session.mockRejectedValue(new ApiError(401, 'UNAUTHORIZED', '无会话'))
    await wb.restoreSession()
    expect(wb.session.value).toBeNull(); expect(wb.authLoading.value).toBe(false)
    expect(api.orders).not.toHaveBeenCalled()
  })
  it('logs in with server permissions rather than trusting the selected role', async () => {
    const { api, wb } = setup()
    api.session.mockRejectedValueOnce(new ApiError(401, 'UNAUTHORIZED', ''))
    await wb.restoreSession()
    api.login.mockResolvedValueOnce(authSession('a-read'))
    await wb.login('a-write')
    expect(wb.canRead.value).toBe(true); expect(wb.canCreate.value).toBe(false); expect(wb.canReconcile.value).toBe(false)
    await wb.selectOrder('A-1001'); await wb.submitRefund('reason')
    expect(api.submit).not.toHaveBeenCalled()
  })
  it('completes login, initial reads and server logout when HTTP has no randomUUID', async () => {
    vi.stubGlobal('crypto', { getRandomValues: globalThis.crypto.getRandomValues.bind(globalThis.crypto) })
    const { api, wb } = setup()
    api.session.mockRejectedValueOnce(new ApiError(401, 'UNAUTHORIZED', ''))
    await wb.restoreSession()
    expect(await wb.login('a-write')).toBe(true)
    expect(api.orders).toHaveBeenCalledOnce()
    expect(wb.orders.value).toEqual([order()])
    expect(wb.authError.value).toBe('')
    await wb.logout()
    expect(api.logout).toHaveBeenCalledOnce()
    expect(wb.session.value).toBeNull()
    expect(wb.logoutPending.value).toBe(false)
  })
  it('retains one cryptographic refund key across HTTP network retries without randomUUID', async () => {
    vi.stubGlobal('crypto', { getRandomValues: globalThis.crypto.getRandomValues.bind(globalThis.crypto) })
    const { api, wb } = setup()
    await wb.restoreSession(); await wb.selectOrder('A-1001')
    api.submit.mockRejectedValueOnce(new TypeError('lost response'))
    expect(await wb.submitRefund('客户取消')).toBe(false)
    const key = api.submit.mock.calls[0]![2]
    expect(key).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    expect(await wb.submitRefund('不得改变原申请')).toBe(true)
    expect(api.submit.mock.calls[1]![2]).toBe(key)
    expect(api.submit.mock.calls[1]![1]).toBe('客户取消')
  })
  it('does not let unavailable notification transports interrupt accepted login or server logout', async () => {
    const postMessage = vi.fn(() => { throw new DOMException('blocked', 'InvalidStateError') })
    vi.stubGlobal('BroadcastChannel', class { onmessage = null; postMessage = postMessage; close() {} })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new DOMException('blocked', 'SecurityError') })
    const { api, wb } = setup()
    api.session.mockRejectedValueOnce(new ApiError(401, 'UNAUTHORIZED', ''))
    await wb.restoreSession()
    expect(await wb.login('a-write')).toBe(true)
    expect(wb.orders.value).toEqual([order()])
    await wb.logout()
    expect(api.logout).toHaveBeenCalledOnce()
    expect(wb.session.value).toBeNull()
    expect(wb.logoutPending.value).toBe(false)
    expect(postMessage).toHaveBeenCalledTimes(2)
  })
  it('does not send a refund when no cryptographic random source is available', async () => {
    vi.stubGlobal('crypto', {})
    const { api, wb } = setup()
    await wb.restoreSession(); await wb.selectOrder('A-1001')
    expect(await wb.submitRefund('客户取消')).toBe(false)
    expect(api.submit).not.toHaveBeenCalled()
    expect(wb.actionError.value).toContain('无法生成安全请求标识')
    expect(wb.pendingIntent.value).toBeNull()
  })
  it('does not let a late invalidated login release the busy state owned by a newer login', async () => {
    const { api, wb } = setup(), first = deferred<AuthSession>(), second = deferred<AuthSession>()
    api.session.mockRejectedValueOnce(new ApiError(401, 'UNAUTHORIZED', ''))
    await wb.restoreSession()
    api.login.mockImplementationOnce(() => first.promise).mockImplementationOnce(() => second.promise)
    const oldLogin = wb.login('a-write')
    window.dispatchEvent(new StorageEvent('storage', { key: 'yishuld:session-change:v1', newValue: '{}' }))
    const newLogin = wb.login('b-write')
    first.reject(new TypeError('old aborted response')); await oldLogin
    expect(wb.authBusy.value).toBe(true)
    await wb.login('a-read'); expect(api.login).toHaveBeenCalledTimes(2)
    second.resolve(authSession('b-write')); await newLogin
    expect(wb.authBusy.value).toBe(false)
    expect(wb.user.value?.merchant_id).toBe('B')
  })
  it('releases login busy when the immediately following business read expires its session', async () => {
    const { api, wb } = setup()
    api.session.mockRejectedValueOnce(new ApiError(401, 'UNAUTHORIZED', ''))
    await wb.restoreSession()
    api.orders.mockRejectedValueOnce(new ApiError(401, 'UNAUTHORIZED', 'expired'))
    await wb.login('a-write')
    expect(wb.session.value).toBeNull()
    expect(wb.authBusy.value).toBe(false)
    await wb.login('b-write')
    expect(wb.user.value?.merchant_id).toBe('B')
  })
  it('checks create and reconcile permissions independently', async () => {
    const { api, wb } = setup(), partial = authSession()
    partial.user.permissions = ['orders:read', 'refunds:read', 'refunds:reconcile']
    api.session.mockResolvedValue(partial)
    await wb.restoreSession()
    expect(wb.canCreate.value).toBe(false); expect(wb.canReconcile.value).toBe(true)
  })
  it('clears and aborts immediately on logout; blocks login until server revocation completes', async () => {
    const { api, wb } = setup(), leaving = deferred<void>(), old = deferred<Order[]>()
    await wb.restoreSession(); await wb.selectOrder('A-1001')
    api.orders.mockImplementationOnce(() => old.promise)
    const refreshing = wb.refreshOrders()
    api.logout.mockImplementationOnce(() => leaving.promise)
    const logout = wb.logout()
    expect(wb.session.value).toBeNull(); expect(wb.orders.value).toEqual([]); expect(wb.detail.value).toBeNull()
    expect(api.orders.mock.calls[1]![0].signal.aborted).toBe(true)
    await wb.login('b-write'); expect(api.login).not.toHaveBeenCalled()
    leaving.resolve(); await logout
    api.orders.mockResolvedValue([order('B-2001')]); await wb.login('b-write')
    old.resolve([order('A-old')]); await refreshing
    expect(wb.orders.value[0]?.order_id).toBe('B-2001')
    expect(api.logout.mock.calls[0]![0].csrfToken).toBe('csrf-a-write')
  })
  it('keeps the user signed out and blocks new login until failed logout is retried', async () => {
    const { api, wb } = setup()
    await wb.restoreSession()
    api.logout.mockRejectedValueOnce(new Error('offline'))
    await wb.logout(); await wb.login('b-write')
    expect(api.login).not.toHaveBeenCalled(); expect(wb.logoutPending.value).toBe(true)
    expect(wb.authError.value).toContain('服务端注销尚未确认')
    await wb.logout()
    expect(wb.logoutPending.value).toBe(false)
    await wb.login('b-write'); expect(wb.user.value?.merchant_id).toBe('B')
  })
  it.each([[401, 'UNAUTHORIZED'], [403, 'SESSION_CHANGED']] as const)('clears the session and all business data on %s %s', async (status, code) => {
    const { api, wb } = setup()
    await wb.restoreSession(); await wb.selectOrder('A-1001')
    api.orders.mockRejectedValueOnce(new ApiError(status, code, '会话失效'))
    await wb.refreshOrders()
    expect(wb.session.value).toBeNull(); expect(wb.orders.value).toEqual([]); expect(wb.detail.value).toBeNull()
    expect(wb.authError.value).toContain('重新登录')
  })
  it('finishes local logout without revoking a different shared-cookie principal after stale-CSRF rejection', async () => {
    const { api, wb } = setup()
    await wb.restoreSession()
    api.logout.mockRejectedValueOnce(new ApiError(403, 'CSRF_REJECTED', '旧会话'))
    api.session.mockResolvedValueOnce(authSession('b-write'))
    await wb.logout()
    expect(wb.logoutPending.value).toBe(false)
    expect(wb.session.value).toBeNull()
    expect(api.logout).toHaveBeenCalledOnce()
    expect(api.session.mock.calls[1]![0].csrfToken).toBeUndefined()
    expect(wb.authError.value).toContain('身份已在其他页面变更')
  })
  it('keeps logout pending if CSRF rejection belongs to the same server session', async () => {
    const { api, wb } = setup()
    await wb.restoreSession()
    api.logout.mockRejectedValueOnce(new ApiError(403, 'CSRF_REJECTED', '拒绝注销'))
    await wb.logout(); await wb.login('b-write')
    expect(wb.logoutPending.value).toBe(true)
    expect(api.login).not.toHaveBeenCalled()
  })
  it('keeps a valid session on a normal FORBIDDEN response', async () => {
    const { api, wb } = setup()
    await wb.restoreSession(); await wb.selectOrder('A-1001')
    api.submit.mockRejectedValueOnce(new ApiError(403, 'FORBIDDEN', '无操作权限'))
    await wb.submitRefund('reason')
    expect(wb.session.value).not.toBeNull(); expect(wb.actionError.value).toContain('无操作权限')
  })
  it('clears immediately on a cross-tab login or logout notification and ignores late data', async () => {
    const { api, wb } = setup(), old = deferred<Order[]>()
    await wb.restoreSession()
    api.orders.mockImplementationOnce(() => old.promise)
    const refreshing = wb.refreshOrders()
    window.dispatchEvent(new StorageEvent('storage', { key: 'yishuld:session-change:v1', newValue: '{"type":"session-change"}' }))
    expect(wb.orders.value).toEqual([]); expect(wb.session.value).toBeNull()
    old.resolve([order('A-old')]); await refreshing
    expect(wb.orders.value).toEqual([])
  })
  it('does not cancel an unconfirmed logout when another tab broadcasts a change', async () => {
    const { api, wb } = setup(), pending = deferred<void>()
    await wb.restoreSession(); api.logout.mockImplementationOnce(() => pending.promise)
    const leaving = wb.logout()
    window.dispatchEvent(new StorageEvent('storage', { key: 'yishuld:session-change:v1', newValue: '{}' }))
    await wb.login('b-write')
    expect(api.login).not.toHaveBeenCalled(); expect(api.logout.mock.calls[0]![0].signal.aborted).toBe(false)
    pending.resolve(); await leaving
  })
  it('clears old business data before revalidating on window focus', async () => {
    const { api, wb } = setup(), pending = deferred<AuthSession>()
    await wb.restoreSession(); api.session.mockImplementationOnce(() => pending.promise)
    window.dispatchEvent(new Event('focus'))
    expect(wb.orders.value).toEqual([]); expect(wb.authLoading.value).toBe(true)
    pending.resolve(authSession('b-write'))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(wb.user.value?.merchant_id).toBe('B')
  })
  it('expires a session locally and removes protected data', async () => {
    vi.useFakeTimers()
    const { api, wb } = setup(), value = authSession()
    value.expires_at = new Date(Date.now() + 1000).toISOString(); api.session.mockResolvedValue(value)
    await wb.restoreSession(); await vi.advanceTimersByTimeAsync(1001)
    expect(wb.session.value).toBeNull(); expect(wb.orders.value).toEqual([])
  })
  it('preserves same-session retry keys on refresh, but deletes only private intent keys on logout', async () => {
    const { api, wb } = setup(), value = authSession()
    api.session.mockResolvedValue(value)
    await wb.restoreSession(); await wb.selectOrder('A-1001')
    api.submit.mockRejectedValueOnce(new Error('offline')); await wb.submitRefund('客户私密原因')
    const key = api.submit.mock.calls[0]![2]
    await wb.restoreSession(); await wb.selectOrder('A-1001')
    expect(wb.pendingIntent.value?.key).toBe(key)
    sessionStorage.setItem('other-app', 'keep')
    await wb.logout()
    expect(sessionStorage.getItem('yishuld:refund-intent:v1:A:A-1001')).toBeNull()
    expect(sessionStorage.getItem('other-app')).toBe('keep')
    expect(JSON.stringify(sessionStorage)).not.toContain(value.csrf_token)
  })
})
