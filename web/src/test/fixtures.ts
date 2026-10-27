import type { AuthSession, IdentityId, Order, Refund, ResetResult } from '../lib/types'

export const authSession = (identity: IdentityId = 'a-write'): AuthSession => ({
  user: {
    identity_id: identity, merchant_id: identity.startsWith('b') ? 'B' : 'A',
    merchant_name: identity.startsWith('b') ? '华南商贸' : '华东商贸', display_name: identity === 'a-read' ? '财务查看' : '财务经办',
    role: identity === 'a-read' ? 'viewer' : 'operator',
    permissions: identity === 'a-read' ? ['orders:read', 'refunds:read'] : ['orders:read', 'refunds:read', 'refunds:create', 'refunds:reconcile'],
  }, csrf_token: `csrf-${identity}`, expires_at: new Date(Date.now() + 3600_000).toISOString(),
})

export const order = (id = 'A-1001'): Order => ({ data_version: 0, order_id: id, merchant_id: id.startsWith('B') ? 'B' : 'A', amount_cents: 19900, status: 'PAID', refund: null })
export const resetResult = (): ResetResult => ({ reset_id: 'reset_001', merchant_id: 'A', order_count: 4, data_version: 1, reset_at: new Date().toISOString(), message: '已恢复' })
export const refund = (status: Refund['status'] = 'SUCCEEDED'): Refund => ({
  refund_id: 'rf_001', order_id: 'A-1001', amount_cents: 19900, provider_request_id: 'provider_001',
  reason: '用户取消服务', status, message: '渠道结果', created_at: '2026-10-27T01:00:00Z', updated_at: '2026-10-27T01:01:00Z',
})
export function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
