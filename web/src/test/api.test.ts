import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../lib/api'
import { authSession, refund, resetResult } from './fixtures'

afterEach(() => { vi.unstubAllGlobals() })
describe('cookie session HTTP contract', () => {
  it('sends same-origin cookies, CSRF, signal and idempotency key, never Bearer or client amount/merchant', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(refund()), { status: 201 }))
    vi.stubGlobal('fetch', fetchMock)
    const signal = new AbortController().signal
    await api.submit('A-1001', '用户取消服务', 'same-key', { csrfToken: 'session-csrf', signal })
    const [url, init] = fetchMock.mock.calls[0]!
    expect(url).toBe('/api/orders/A-1001/refunds')
    expect(init.headers).toEqual({ 'X-CSRF-Token': 'session-csrf', 'Content-Type': 'application/json', 'Idempotency-Key': 'same-key' })
    expect(init.credentials).toBe('same-origin')
    expect(JSON.parse(init.body)).toEqual({ reason: '用户取消服务' })
    expect(init.signal).toBe(signal)
  })
  it('binds business GETs to the current CSRF/session and does not send auth header on restoration', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ items: [] })))
    vi.stubGlobal('fetch', fetchMock)
    await api.orders({ csrfToken: 'session-csrf', signal: new AbortController().signal })
    expect(fetchMock.mock.calls[0]![1].headers).toEqual({ 'X-CSRF-Token': 'session-csrf' })
    fetchMock.mockResolvedValue(new Response(JSON.stringify(authSession())))
    await api.session({ signal: new AbortController().signal })
    expect(fetchMock.mock.calls[1]![1].headers).toEqual({})
  })
  it('only sends an identity selection during login and attaches CSRF to logout', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(authSession())))
    vi.stubGlobal('fetch', fetchMock)
    await api.login('a-read', { signal: new AbortController().signal })
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/auth/login')
    expect(JSON.parse(fetchMock.mock.calls[0]![1].body)).toEqual({ identity_id: 'a-read' })
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ status: 'logged_out' })))
    await api.logout({ csrfToken: 'csrf', signal: new AbortController().signal })
    expect(fetchMock.mock.calls[1]![1].headers).toEqual({ 'X-CSRF-Token': 'csrf' })
  })
  it('preserves server error code and status', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: 'IDEMPOTENCY_CONFLICT', message: '同键异参' } }), { status: 409 })))
    await expect(api.orders({ csrfToken: 'csrf', signal: new AbortController().signal })).rejects.toMatchObject({ status: 409, code: 'IDEMPOTENCY_CONFLICT', message: '同键异参' })
  })
  it('sends only the explicit confirmation to the merchant-scoped reset endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(resetResult())))
    vi.stubGlobal('fetch', fetchMock)
    await api.reset('恢复初始数据', { csrfToken: 'csrf', signal: new AbortController().signal })
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/maintenance/reset')
    expect(fetchMock.mock.calls[0]![1].credentials).toBe('same-origin')
    expect(fetchMock.mock.calls[0]![1].headers['X-CSRF-Token']).toBe('csrf')
    expect(JSON.parse(fetchMock.mock.calls[0]![1].body)).toEqual({ confirmation: '恢复初始数据' })
  })
  it('treats unparseable response as an error, not a successful refund', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('not json', { status: 200 })))
    await expect(api.submit('A-1001', 'reason', 'key', { csrfToken: 'csrf', signal: new AbortController().signal })).rejects.toBeInstanceOf(ApiError)
  })
})
