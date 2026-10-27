import { afterEach, describe, expect, it, vi } from 'vitest'
import { useWorkbench } from '../composables/useWorkbench'
import { ApiError, type WorkbenchApi } from '../lib/api'
import type { Order, Refund } from '../lib/types'
import { authSession, deferred, order, refund, resetResult } from './fixtures'

const workbenches: ReturnType<typeof useWorkbench>[] = []
function readyWorkbench(api: WorkbenchApi) {
  const wb = useWorkbench(api)
  wb.session.value = authSession()
  wb.authLoading.value = false
  workbenches.push(wb)
  return wb
}
afterEach(() => workbenches.splice(0).forEach((wb) => wb.dispose()))

function fakeApi() {
  return {
    login: vi.fn<WorkbenchApi['login']>().mockImplementation(async (id) => authSession(id)),
    session: vi.fn<WorkbenchApi['session']>().mockResolvedValue(authSession()),
    logout: vi.fn<WorkbenchApi['logout']>().mockResolvedValue(undefined),
    reset: vi.fn<WorkbenchApi['reset']>().mockResolvedValue(resetResult()),
    orders: vi.fn<WorkbenchApi['orders']>().mockResolvedValue([order()]),
    order: vi.fn<WorkbenchApi['order']>().mockImplementation(async (id) => order(id)),
    activity: vi.fn<WorkbenchApi['activity']>().mockResolvedValue({ items: [], available: true, data_version: 0 }),
    submit: vi.fn<WorkbenchApi['submit']>().mockResolvedValue(refund()),
    reconcile: vi.fn<WorkbenchApi['reconcile']>().mockResolvedValue(refund()),
  }
}

describe('identity and request isolation', () => {
  it('clears old identity data immediately and ignores late list, detail and activity responses', async () => {
    const api = fakeApi()
    const oldList = deferred<Order[]>(), oldDetail = deferred<Order>(), oldActivity = deferred<{ items: []; available: boolean; data_version: number }>()
    api.orders.mockImplementationOnce(() => oldList.promise).mockResolvedValue([order('B-2001')])
    api.order.mockImplementationOnce(() => oldDetail.promise)
    api.activity.mockImplementationOnce(() => oldActivity.promise)
    const wb = readyWorkbench(api)
    const loading = wb.refreshAll(), selecting = wb.selectOrder('A-1001')
    wb.orders.value = [order()]
    const switching = wb.logout().then(() => wb.login('b-write'))
    expect(wb.orders.value).toEqual([])
    expect(wb.detail.value).toBeNull()
    expect(wb.selectedId.value).toBeNull()
    expect(api.orders.mock.calls[0]![0].signal.aborted).toBe(true)
    await switching
    oldList.resolve([order('A-old')]); oldDetail.resolve(order('A-old')); oldActivity.resolve({ items: [], available: false, data_version: 0 })
    await Promise.all([loading, selecting])
    expect(wb.orders.value.map((x) => x.order_id)).toEqual(['B-2001'])
    expect(wb.detail.value).toBeNull()
    expect(wb.activityAvailable.value).toBe(true)
    expect(api.orders.mock.calls[1]![0].csrfToken).toBe('csrf-b-write')
  })

  it('ignores a late mutation response after identity switch', async () => {
    const api = fakeApi(), pending = deferred<Refund>()
    api.submit.mockImplementation(() => pending.promise)
    const wb = readyWorkbench(api)
    await wb.selectOrder('A-1001')
    const submission = wb.submitRefund('用户取消服务')
    await wb.logout(); await wb.login('b-write')
    pending.resolve(refund())
    await submission
    expect(wb.detail.value).toBeNull()
    expect(wb.notice.value).toBe('')
    expect(wb.actionLoading.value).toBe(false)
  })

  it('keeps the newest order selection when an earlier detail arrives late', async () => {
    const api = fakeApi(), old = deferred<Order>()
    api.order.mockImplementationOnce(() => old.promise).mockResolvedValueOnce(order('A-1002'))
    const wb = readyWorkbench(api)
    const first = wb.selectOrder('A-1001')
    await wb.selectOrder('A-1002')
    old.resolve(order('A-1001')); await first
    expect(wb.detail.value?.order_id).toBe('A-1002')
  })

  it('keeps the latest refresh when an older request finishes last', async () => {
    const api = fakeApi(), old = deferred<Order[]>()
    api.orders.mockImplementationOnce(() => old.promise).mockResolvedValueOnce([order('A-new')])
    const wb = readyWorkbench(api)
    const first = wb.refreshOrders(); await wb.refreshOrders()
    old.resolve([order('A-old')]); await first
    expect(wb.orders.value[0]?.order_id).toBe('A-new')
  })
})

describe('safe refund actions', () => {
  it('does not allow a late list snapshot without a refund to erase a completed submit', async () => {
    const api = fakeApi(), oldList = deferred<Order[]>()
    api.orders.mockImplementationOnce(() => oldList.promise)
    const wb = readyWorkbench(api)
    await wb.selectOrder('A-1001')
    const refreshing = wb.refreshOrders()
    await wb.submitRefund('用户取消服务')
    oldList.resolve([order()]); await refreshing
    expect(wb.orders.value[0]?.refund?.status).toBe('SUCCEEDED')
    expect(wb.detail.value?.refund?.status).toBe('SUCCEEDED')
  })

  it.each(['PROCESSING', 'UNKNOWN'] as const)('does not allow a late detail snapshot %s to regress a reconciled terminal result', async (status) => {
    const api = fakeApi(), lateDetail = deferred<Order>(), pending = deferred<Refund>()
    api.order.mockResolvedValueOnce({ ...order(), refund: refund('UNKNOWN') }).mockImplementationOnce(() => lateDetail.promise)
    api.reconcile.mockImplementationOnce(() => pending.promise)
    const wb = readyWorkbench(api)
    await wb.selectOrder('A-1001')
    const reconciling = wb.reconcile()
    const refreshing = wb.selectOrder('A-1001')
    pending.resolve(refund('SUCCEEDED')); await reconciling
    lateDetail.resolve({ ...order(), refund: refund(status) }); await refreshing
    expect(wb.detail.value?.refund?.status).toBe('SUCCEEDED')
    expect(wb.detailLoading.value).toBe(false)
  })

  it('prefers newer nonterminal refund timestamps over a late old detail response', async () => {
    const api = fakeApi(), wb = readyWorkbench(api)
    const newer = { ...refund('UNKNOWN'), updated_at: '2026-10-27T03:00:00Z' }
    api.orders.mockResolvedValue([{ ...order(), refund: newer }])
    api.order.mockResolvedValue({ ...order(), refund: refund('PROCESSING') })
    await wb.refreshOrders(); await wb.selectOrder('A-1001')
    expect(wb.detail.value?.refund).toEqual(newer)
  })

  it('allows another selected order to finish loading while an earlier order mutation completes', async () => {
    const api = fakeApi(), oldSubmit = deferred<Refund>(), newDetail = deferred<Order>()
    api.submit.mockImplementationOnce(() => oldSubmit.promise)
    const wb = readyWorkbench(api)
    await wb.selectOrder('A-1001')
    const submitting = wb.submitRefund('用户取消服务')
    api.order.mockImplementationOnce(() => newDetail.promise)
    const selecting = wb.selectOrder('A-1002')
    oldSubmit.resolve(refund()); await submitting
    expect(wb.detailLoading.value).toBe(true)
    newDetail.resolve(order('A-1002')); await selecting
    expect(wb.detail.value?.order_id).toBe('A-1002')
    expect(wb.detail.value?.refund).toBeNull()
    expect(wb.detailLoading.value).toBe(false)
  })

  it('preserves the original idempotency key AND reason after a network failure', async () => {
    const api = fakeApi()
    api.submit.mockRejectedValueOnce(new TypeError('Failed to fetch')).mockResolvedValueOnce(refund())
    const wb = readyWorkbench(api)
    await wb.selectOrder('A-1001')
    expect(await wb.submitRefund('用户取消服务')).toBe(false)
    const key = api.submit.mock.calls[0]![2]
    expect(wb.actionError.value).toContain('原申请已保留')
    expect(wb.detail.value?.refund).toBeNull()
    expect(await wb.submitRefund('不同原因不可静默改参')).toBe(true)
    expect(api.submit.mock.calls[1]![2]).toBe(key)
    expect(api.submit.mock.calls[1]![1]).toBe('用户取消服务')
    expect(wb.detail.value?.refund?.status).toBe('SUCCEEDED')
    expect(wb.pendingIntent.value).toBeNull()
  })

  it('restores an uncertain intent across a page recreation without changing the key', async () => {
    const api = fakeApi()
    api.submit.mockRejectedValueOnce(new TypeError('offline'))
    const first = readyWorkbench(api)
    await first.selectOrder('A-1001'); await first.submitRefund('原始原因')
    const key = api.submit.mock.calls[0]![2]
    first.dispose()
    const restored = readyWorkbench(api)
    await restored.selectOrder('A-1001')
    expect(restored.pendingIntent.value).toEqual({ key, reason: '原始原因' })
    await restored.submitRefund('new')
    expect(api.submit.mock.calls[1]![2]).toBe(key)
    expect(api.submit.mock.calls[1]![1]).toBe('原始原因')
  })

  it('does not submit duplicate in-flight actions', async () => {
    const api = fakeApi(), pending = deferred<Refund>()
    api.submit.mockImplementation(() => pending.promise)
    const wb = readyWorkbench(api)
    await wb.selectOrder('A-1001')
    const first = wb.submitRefund('reason')
    await wb.submitRefund('reason')
    expect(api.submit).toHaveBeenCalledTimes(1)
    pending.resolve(refund()); await first
  })

  it('blocks read-only writes and unpaid order submissions before calling the API', async () => {
    const api = fakeApi(), wb = readyWorkbench(api)
    await wb.logout(); await wb.login('a-read'); await wb.selectOrder('A-1001')
    await wb.submitRefund('reason')
    expect(api.submit).not.toHaveBeenCalled()
    wb.detail.value = { ...order(), refund: refund('UNKNOWN') }
    await wb.reconcile()
    expect(api.reconcile).not.toHaveBeenCalled()
    await wb.logout(); await wb.login('a-write')
    api.order.mockResolvedValueOnce({ ...order(), status: 'UNPAID' })
    await wb.selectOrder('A-1001'); await wb.submitRefund('reason')
    expect(api.submit).not.toHaveBeenCalled()
  })

  it.each(['SUCCEEDED', 'FAILED'] as const)('does not resubmit or reconcile terminal %s', async (status) => {
    const api = fakeApi(), wb = readyWorkbench(api)
    api.order.mockResolvedValue({ ...order(), refund: refund(status) })
    await wb.selectOrder('A-1001'); await wb.submitRefund('reason'); await wb.reconcile()
    expect(api.submit).not.toHaveBeenCalled(); expect(api.reconcile).not.toHaveBeenCalled()
  })

  it.each(['UNKNOWN', 'PROCESSING'] as const)('queries %s through reconcile only', async (status) => {
    const api = fakeApi(), wb = readyWorkbench(api)
    api.order.mockResolvedValue({ ...order(), refund: refund(status) })
    await wb.selectOrder('A-1001'); await wb.reconcile()
    expect(api.reconcile.mock.calls[0]![0]).toBe('rf_001')
    expect(api.submit).not.toHaveBeenCalled()
    expect(wb.detail.value?.refund?.status).toBe('SUCCEEDED')
  })

  it('retains UNKNOWN on a query network failure instead of declaring failure', async () => {
    const api = fakeApi(), wb = readyWorkbench(api)
    api.order.mockResolvedValue({ ...order(), refund: refund('UNKNOWN') })
    api.reconcile.mockRejectedValue(new TypeError('offline'))
    await wb.selectOrder('A-1001'); await wb.reconcile()
    expect(wb.detail.value?.refund?.status).toBe('UNKNOWN')
    expect(wb.actionError.value).toContain('原退款状态不变')
  })

  it('validates blank and Unicode-length reasons', async () => {
    const api = fakeApi(), wb = readyWorkbench(api)
    await wb.selectOrder('A-1001')
    await wb.submitRefund('   '); await wb.submitRefund('字'.repeat(201))
    expect(api.submit).not.toHaveBeenCalled()
    expect(wb.actionError.value).toContain('1–200')
    await wb.submitRefund('😀'.repeat(200))
    expect(api.submit).toHaveBeenCalledTimes(1)
  })
})

describe('list and detail consistency after external changes', () => {
  it('updates the selected detail on global refresh when another tab completed a refund', async () => {
    const api = fakeApi(), wb = readyWorkbench(api)
    await wb.refreshAll(); await wb.selectOrder('A-1001')
    expect(wb.detail.value?.refund).toBeNull()
    api.orders.mockResolvedValue([{ ...order(), refund: refund('SUCCEEDED') }])
    await wb.refreshAll()
    expect(wb.detail.value?.refund?.status).toBe('SUCCEEDED')
    expect(wb.orders.value[0]?.refund).toEqual(wb.detail.value?.refund)
    await wb.submitRefund('no longer eligible')
    expect(api.submit).not.toHaveBeenCalled()
  })

  it('updates list rows and counts when the selected detail discovers an external terminal result', async () => {
    const api = fakeApi(), wb = readyWorkbench(api)
    await wb.refreshAll(); await wb.selectOrder('A-1001')
    api.order.mockResolvedValue({ ...order(), refund: refund('SUCCEEDED') })
    await wb.selectOrder('A-1001')
    expect(wb.orders.value[0]?.refund).toEqual(wb.detail.value?.refund)
    expect(wb.orders.value.filter((order) => order.refund?.status === 'SUCCEEDED')).toHaveLength(1)
    expect(wb.orders.value.filter((order) => order.status === 'PAID' && !order.refund)).toHaveLength(0)
  })

  it('does not overwrite or finish a different order detail that is still loading during global refresh', async () => {
    const api = fakeApi(), wb = readyWorkbench(api), next = deferred<Order>()
    await wb.refreshAll(); await wb.selectOrder('A-1001')
    api.order.mockImplementationOnce(() => next.promise)
    const selecting = wb.selectOrder('A-1002')
    api.orders.mockResolvedValue([{ ...order(), refund: refund() }, order('A-1002')])
    await wb.refreshAll()
    expect(wb.selectedId.value).toBe('A-1002')
    expect(wb.detail.value).toBeNull()
    expect(wb.detailLoading.value).toBe(true)
    next.resolve(order('A-1002')); await selecting
    expect(wb.detail.value?.order_id).toBe('A-1002')
    expect(wb.detailLoading.value).toBe(false)
  })

  it('keeps both views terminal when a stale list refresh arrives after a newer detail', async () => {
    const api = fakeApi(), wb = readyWorkbench(api), stale = deferred<Order[]>()
    await wb.refreshAll(); await wb.selectOrder('A-1001')
    api.orders.mockImplementationOnce(() => stale.promise)
    const refreshing = wb.refreshAll()
    api.order.mockResolvedValue({ ...order(), refund: refund('SUCCEEDED') })
    await wb.selectOrder('A-1001')
    stale.resolve([order()]); await refreshing
    expect(wb.orders.value[0]?.refund?.status).toBe('SUCCEEDED')
    expect(wb.detail.value?.refund?.status).toBe('SUCCEEDED')
  })
})

describe('empty and degraded states', () => {
  it('accepts an empty order list', async () => {
    const api = fakeApi(), wb = readyWorkbench(api)
    api.orders.mockResolvedValue([])
    await wb.refreshOrders()
    expect(wb.orders.value).toEqual([]); expect(wb.listError.value).toBe('')
  })
  it('shows API errors without hiding them behind a fake empty-success state', async () => {
    const api = fakeApi(), wb = readyWorkbench(api)
    api.orders.mockRejectedValue(new ApiError(401, 'UNAUTHORIZED', '身份无效'))
    await wb.refreshOrders()
    expect(wb.session.value).toBeNull(); expect(wb.authError.value).toContain('重新登录'); expect(wb.listLoading.value).toBe(false)
  })
  it('does not change business data when the auxiliary activity endpoint fails', async () => {
    const api = fakeApi(), wb = readyWorkbench(api)
    api.activity.mockRejectedValue(new Error('redis down'))
    await wb.refreshAll()
    expect(wb.orders.value).toEqual([order()]); expect(wb.activityAvailable.value).toBe(false)
    expect(wb.listError.value).toBe('')
  })
})
