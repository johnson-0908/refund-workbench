import type { Activity, AuthSession, IdentityId, Order, Refund, ResetResult } from './types'

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message)
    this.name = 'ApiError'
  }
}

export interface RequestContext { csrfToken?: string; signal: AbortSignal }
export interface WorkbenchApi {
  login(identityId: IdentityId, ctx: RequestContext): Promise<AuthSession>
  session(ctx: RequestContext): Promise<AuthSession>
  logout(ctx: RequestContext): Promise<void>
  reset(confirmation: string, ctx: RequestContext): Promise<ResetResult>
  orders(ctx: RequestContext): Promise<Order[]>
  order(id: string, ctx: RequestContext): Promise<Order>
  activity(ctx: RequestContext): Promise<{ items: Activity[]; available: boolean; data_version: number }>
  submit(id: string, reason: string, key: string, ctx: RequestContext): Promise<Refund>
  reconcile(id: string, ctx: RequestContext): Promise<Refund>
}

async function request<T>(path: string, ctx: RequestContext, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`/api${path}`, {
    ...init, signal: ctx.signal, credentials: 'same-origin',
    headers: { ...(ctx.csrfToken ? { 'X-CSRF-Token': ctx.csrfToken } : {}), ...init.headers },
  })
  if (response.status === 204) return undefined as T
  let body: unknown
  try { body = await response.json() } catch {
    throw new ApiError(response.status, 'INVALID_RESPONSE', '服务响应无法解析，请刷新查询原订单状态。')
  }
  if (!response.ok) {
    const error = (body as { error?: { code?: string; message?: string } })?.error
    throw new ApiError(response.status, error?.code ?? 'REQUEST_FAILED', error?.message ?? '请求失败，请稍后重试。')
  }
  return body as T
}

export const api: WorkbenchApi = {
  login: (identityId, ctx) => request('/auth/login', ctx, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ identity_id: identityId }),
  }),
  session: (ctx) => request('/auth/session', ctx),
  logout: (ctx) => request('/auth/logout', ctx, { method: 'POST' }),
  reset: (confirmation, ctx) => request('/maintenance/reset', ctx, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ confirmation }),
  }),
  orders: async (ctx) => (await request<{ items: Order[] }>('/orders', ctx)).items,
  order: (id, ctx) => request(`/orders/${encodeURIComponent(id)}`, ctx),
  activity: (ctx) => request('/activity', ctx),
  submit: (id, reason, key, ctx) => request(`/orders/${encodeURIComponent(id)}/refunds`, ctx, {
    method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
    body: JSON.stringify({ reason }),
  }),
  reconcile: (id, ctx) => request(`/refunds/${encodeURIComponent(id)}/reconcile`, ctx, { method: 'POST' }),
}
