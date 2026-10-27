import { afterEach, describe, expect, it, vi } from 'vitest'
import { useWorkbench } from '../composables/useWorkbench'
import { ApiError, type WorkbenchApi } from '../lib/api'
import type { Order, Refund, ResetResult } from '../lib/types'
import { authSession, deferred, order, refund, resetResult } from './fixtures'

const instances: ReturnType<typeof useWorkbench>[] = []
function setup(allowReset = true) {
  const session = authSession()
  if (allowReset) session.user.permissions.push('data:reset')
  const api = {
    login: vi.fn<WorkbenchApi['login']>().mockImplementation(async (id) => authSession(id)),
    session: vi.fn<WorkbenchApi['session']>().mockResolvedValue(session),
    logout: vi.fn<WorkbenchApi['logout']>().mockResolvedValue(undefined),
    reset: vi.fn<WorkbenchApi['reset']>().mockResolvedValue(resetResult()),
    orders: vi.fn<WorkbenchApi['orders']>().mockResolvedValue([order()]),
    order: vi.fn<WorkbenchApi['order']>().mockResolvedValue(order()),
    activity: vi.fn<WorkbenchApi['activity']>().mockResolvedValue({ items: [], available: true, data_version: 0 }),
    submit: vi.fn<WorkbenchApi['submit']>().mockResolvedValue(refund()),
    reconcile: vi.fn<WorkbenchApi['reconcile']>().mockResolvedValue(refund()),
  }
  const wb = useWorkbench(api); instances.push(wb)
  function setVersion(version: number) {
    api.orders.mockResolvedValue([{ ...order(), data_version: version }])
    api.order.mockResolvedValue({ ...order(), data_version: version })
    api.activity.mockResolvedValue({ items: [], available: true, data_version: version })
  }
  return { api, wb, setVersion }
}
afterEach(() => instances.splice(0).forEach((wb) => wb.dispose()))

describe('merchant-scoped initial data restoration', () => {
  it('requires the server permission and the exact confirmation phrase', async () => {
    const denied = setup(false); await denied.wb.restoreSession()
    await denied.wb.resetData('恢复初始数据')
    expect(denied.api.reset).not.toHaveBeenCalled()
    const allowed = setup(); await allowed.wb.restoreSession()
    await allowed.wb.resetData('恢复'); await allowed.wb.resetData('恢复初始数据 ')
    expect(allowed.api.reset).not.toHaveBeenCalled()
    expect(allowed.wb.resetError.value).toContain('请输入')
  })
  it('blocks reset while a refund is in flight', async () => {
    const { api, wb } = setup(), pending = deferred<Refund>()
    await wb.restoreSession(); await wb.selectOrder('A-1001')
    api.submit.mockImplementationOnce(() => pending.promise)
    const submit = wb.submitRefund('reason')
    await wb.resetData('恢复初始数据'); expect(api.reset).not.toHaveBeenCalled()
    pending.resolve(refund()); await submit
  })
  it('keeps the session, clears caches/drafts, aborts old reads and reloads the new version after success', async () => {
    const { api, wb, setVersion } = setup(), old = deferred<Order[]>(), pending = deferred<ResetResult>()
    api.orders.mockResolvedValue([{ ...order(), refund: refund() }])
    api.order.mockResolvedValue({ ...order(), refund: refund() })
    await wb.restoreSession(); await wb.selectOrder('A-1001')
    sessionStorage.setItem('yishuld:refund-intent:v1:A:A-1001', JSON.stringify({ key: 'old', reason: 'private' }))
    sessionStorage.setItem('unrelated', 'keep')
    api.orders.mockImplementationOnce(() => old.promise)
    const reading = wb.refreshOrders()
    api.reset.mockImplementationOnce(() => pending.promise)
    const resetting = wb.resetData('恢复初始数据')
    expect(wb.resetLoading.value).toBe(true)
    expect(wb.orders.value).toEqual([]); expect(wb.detail.value).toBeNull()
    expect(api.orders.mock.calls[1]![0].signal.aborted).toBe(true)
    expect(sessionStorage.getItem('yishuld:refund-intent:v1:A:A-1001')).toBeNull()
    expect(sessionStorage.getItem('unrelated')).toBe('keep')
    await wb.resetData('恢复初始数据'); await wb.submitRefund('blocked'); await wb.reconcile(); await wb.refreshAll()
    expect(api.reset).toHaveBeenCalledOnce(); expect(api.submit).not.toHaveBeenCalled(); expect(api.reconcile).not.toHaveBeenCalled()
    setVersion(1); pending.resolve(resetResult()); await resetting
    old.resolve([{ ...order(), refund: refund() }]); await reading
    expect(wb.session.value).not.toBeNull(); expect(wb.orders.value[0]?.refund).toBeNull()
    expect(wb.dataVersion.value).toBe(1); expect(wb.resetNotice.value).toBe('已恢复 4 笔初始订单。')
    expect(wb.resetLoading.value).toBe(false)
    await wb.selectOrder('A-1001'); expect(wb.detail.value?.refund).toBeNull()
    expect(api.reset.mock.calls[0]![1].csrfToken).toBe('csrf-a-write')
  })
  it('never claims success after an uncertain network result, and lets a fresh GET resolve the visible state', async () => {
    const { api, wb, setVersion } = setup()
    await wb.restoreSession(); api.reset.mockRejectedValueOnce(new TypeError('lost response'))
    await wb.resetData('恢复初始数据')
    expect(wb.resetNotice.value).toBe(''); expect(wb.resetError.value).toContain('尚未确认')
    expect(wb.session.value).not.toBeNull(); expect(wb.resetLoading.value).toBe(false)
    setVersion(1); await wb.refreshAll()
    expect(wb.orders.value[0]?.data_version).toBe(1)
    expect(wb.resetNotice.value).toBe('')
  })
  it('shows an explicit server refusal rather than claiming a reset succeeded', async () => {
    const { api, wb } = setup(); await wb.restoreSession()
    api.reset.mockRejectedValueOnce(new ApiError(409, 'RESET_BUSY', '当前企业仍有处理中操作，请稍后重试'))
    await wb.resetData('恢复初始数据')
    expect(wb.resetError.value).toContain('处理中操作')
    expect(wb.resetNotice.value).toBe('')
  })
  it('ignores an old reset result after logout and another merchant login', async () => {
    const { api, wb } = setup(), pending = deferred<ResetResult>()
    await wb.restoreSession(); api.reset.mockImplementationOnce(() => pending.promise)
    const resetting = wb.resetData('恢复初始数据')
    await wb.logout(); api.orders.mockResolvedValue([order('B-2001')]); await wb.login('b-write')
    pending.resolve(resetResult()); await resetting
    expect(wb.user.value?.merchant_id).toBe('B'); expect(wb.orders.value[0]?.order_id).toBe('B-2001')
    expect(wb.resetNotice.value).toBe(''); expect(wb.resetLoading.value).toBe(false)
  })
})

describe('data version fencing across reset and sleeping tabs', () => {
  it('accepts a higher GET version even when a reset notification was missed, clearing cached terminal refunds', async () => {
    const { api, wb, setVersion } = setup()
    api.orders.mockResolvedValue([{ ...order(), refund: refund('SUCCEEDED') }])
    api.order.mockResolvedValue({ ...order(), refund: refund('SUCCEEDED') })
    await wb.restoreSession(); await wb.selectOrder('A-1001')
    expect(wb.detail.value?.refund?.status).toBe('SUCCEEDED')
    setVersion(1); await wb.refreshOrders()
    expect(wb.orders.value[0]?.refund).toBeNull(); expect(wb.detail.value).toBeNull()
    expect(wb.dataVersion.value).toBe(1)
  })
  it('rejects lower-version list, detail and activity responses instead of reviving deleted refunds', async () => {
    const { api, wb, setVersion } = setup()
    setVersion(1); await wb.restoreSession()
    api.orders.mockResolvedValue([{ ...order(), refund: refund() }])
    api.order.mockResolvedValue({ ...order(), refund: refund() })
    api.activity.mockResolvedValue({ items: [], available: false, data_version: 0 })
    await wb.refreshAll(); await wb.selectOrder('A-1001')
    expect(wb.orders.value[0]?.refund).toBeNull(); expect(wb.orders.value[0]?.data_version).toBe(1)
    expect(wb.detail.value).toBeNull(); expect(wb.detailError.value).toContain('已更新')
    expect(wb.activityAvailable.value).toBe(true)
  })
  it('invalidates a late old refund write when a newer GET detects a reset', async () => {
    const { api, wb, setVersion } = setup(), pending = deferred<Refund>()
    await wb.restoreSession(); await wb.selectOrder('A-1001')
    api.submit.mockImplementationOnce(() => pending.promise)
    const submitting = wb.submitRefund('old generation')
    setVersion(1); await wb.refreshOrders()
    pending.resolve(refund('SUCCEEDED')); await submitting
    expect(wb.orders.value[0]?.refund).toBeNull(); expect(wb.actionLoading.value).toBe(false)
    expect(wb.notice.value).toBe('')
  })
  it('handles a same-merchant reset broadcast immediately and ignores an older late write', async () => {
    const { api, wb, setVersion } = setup(), pending = deferred<Refund>()
    await wb.restoreSession(); await wb.selectOrder('A-1001')
    api.submit.mockImplementationOnce(() => pending.promise)
    const submitting = wb.submitRefund('old generation')
    setVersion(1)
    window.dispatchEvent(new StorageEvent('storage', { key: 'yishuld:data-reset:v1', newValue: JSON.stringify({ merchant_id: 'A', data_version: 1 }) }))
    expect(wb.detail.value).toBeNull(); expect(wb.actionLoading.value).toBe(false)
    pending.resolve(refund()); await submitting; await wb.refreshAll()
    expect(wb.orders.value[0]?.refund).toBeNull(); expect(wb.session.value).not.toBeNull()
  })
  it('ignores other-merchant or duplicate reset broadcasts', async () => {
    const { wb } = setup(); await wb.restoreSession(); await wb.selectOrder('A-1001')
    window.dispatchEvent(new StorageEvent('storage', { key: 'yishuld:data-reset:v1', newValue: JSON.stringify({ merchant_id: 'B', data_version: 1 }) }))
    window.dispatchEvent(new StorageEvent('storage', { key: 'yishuld:data-reset:v1', newValue: JSON.stringify({ merchant_id: 'A', data_version: 0 }) }))
    expect(wb.detail.value?.order_id).toBe('A-1001'); expect(wb.dataVersion.value).toBe(0)
  })
  it('does not retain a reset loading lock if a concurrent later reset is detected during refresh', async () => {
    const { wb, setVersion } = setup(); await wb.restoreSession()
    setVersion(2)
    await wb.resetData('恢复初始数据')
    expect(wb.dataVersion.value).toBe(2); expect(wb.resetLoading.value).toBe(false)
    expect(wb.orders.value[0]?.refund).toBeNull()
  })
})
