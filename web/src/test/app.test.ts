import { DOMWrapper, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installUI } from '../ui'
import App from '../App.vue'
import { authSession, deferred, order, refund, resetResult } from './fixtures'

const wrappers: ReturnType<typeof mount>[] = []
const nativeGetComputedStyle = window.getComputedStyle.bind(window)
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const unauthorized = () => json({ error: { code: 'UNAUTHORIZED', message: '登录已失效' } }, 401)
function mountApp() { const wrapper = mount(App, { global: { plugins: [installUI] } }); wrappers.push(wrapper); return wrapper }

beforeEach(() => {
  vi.spyOn(window, 'getComputedStyle').mockImplementation((element) => nativeGetComputedStyle(element))
})
afterEach(() => { wrappers.splice(0).forEach((wrapper) => wrapper.unmount()); vi.unstubAllGlobals() })

describe('real Ant Design Vue session and ERP rendering', () => {
  it('shows an independent login page without loading any business data when no session exists', async () => {
    const fetchMock = vi.fn(async (_url: string) => unauthorized())
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mountApp(); await flushPromises()
    expect(wrapper.find('[data-testid="login-page"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('登录工作台')
    expect(wrapper.find('.app-shell').exists()).toBe(false)
    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual(['/api/auth/session'])
  })
  it('does not expose business UI while session restoration is pending, then renders real empty components', async () => {
    const pending = deferred<Response>()
    const fetchMock = vi.fn(async (url: string, _init?: RequestInit) => url === '/api/auth/session' ? pending.promise : json(url === '/api/activity' ? { items: [], available: true, data_version: 0 } : { items: [] }))
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mountApp()
    expect(wrapper.find('[data-testid="session-restoring"]').exists()).toBe(true)
    expect(wrapper.find('.app-shell').exists()).toBe(false)
    expect(fetchMock).toHaveBeenCalledOnce()
    pending.resolve(json(authSession())); await flushPromises()
    expect(wrapper.find('[data-testid="erp-shell"]').exists()).toBe(true)
    expect(wrapper.find('.ant-empty').exists()).toBe(true)
    expect(wrapper.find('[data-testid="orders-empty"]').text()).toContain('当前商户暂无订单')
    expect(wrapper.text()).not.toMatch(/MySQL|Redis|Kafka|演示|本题|幂等/)
  })
  it('submits the selected read-only identity and hides refund write actions using server permissions', async () => {
    const fetchMock = vi.fn(async (url: string, _init?: RequestInit) => {
      if (url === '/api/auth/session') return unauthorized()
      if (url === '/api/auth/login') return json(authSession('a-read'))
      if (url === '/api/orders') return json({ items: [order()] })
      if (url === '/api/orders/A-1001') return json(order())
      return json({ items: [], available: true, data_version: 0 })
    })
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mountApp(); await flushPromises()
    await wrapper.find('[data-testid="identity-a-read"]').trigger('click')
    await wrapper.find('form').trigger('submit'); await flushPromises()
    const loginCall = fetchMock.mock.calls.find((call) => call[0] === '/api/auth/login')!
    expect(JSON.parse((loginCall[1] as RequestInit).body as string)).toEqual({ identity_id: 'a-read' })
    expect(wrapper.text()).toContain('财务查看')
    await wrapper.find('.order-id').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="create-refund-button"]').exists()).toBe(false)
    expect(wrapper.find('#refund-reason').exists()).toBe(false)
    expect(wrapper.text()).toContain('当前账号仅可查看退款信息')
  })
  it('hides channel reconciliation for read-only users even with an unresolved refund', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string, _init?: RequestInit) => {
      if (url === '/api/auth/session') return json(authSession('a-read'))
      if (url === '/api/orders') return json({ items: [order()] })
      if (url === '/api/orders/A-1001') return json({ ...order(), refund: refund('UNKNOWN') })
      return json({ items: [], available: true, data_version: 0 })
    }))
    const wrapper = mountApp(); await flushPromises()
    await wrapper.find('.order-id').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="reconcile-button"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('请联系财务经办人员')
  })
  it('clears the ERP immediately on logout and offers retry without pretending failed revocation succeeded', async () => {
    const leaving = deferred<Response>()
    vi.stubGlobal('fetch', vi.fn(async (url: string, _init?: RequestInit) => {
      if (url === '/api/auth/session') return json(authSession())
      if (url === '/api/auth/logout') return leaving.promise
      return json({ items: [], available: true, data_version: 0 })
    }))
    const wrapper = mountApp(); await flushPromises()
    await wrapper.find('[data-testid="logout-button"]').trigger('click')
    expect(wrapper.find('[data-testid="erp-shell"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="login-submit"]').exists()).toBe(false)
    leaving.reject(new TypeError('offline')); await flushPromises()
    expect(wrapper.find('[data-testid="auth-error"]').text()).toContain('服务端注销尚未确认')
    expect(wrapper.find('[data-testid="retry-logout"]').exists()).toBe(true)
  })
  it('returns to login when a business API says the session expired', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string, _init?: RequestInit) => {
      if (url === '/api/auth/session') return json(authSession())
      if (url === '/api/orders') return unauthorized()
      return json({ items: [], available: true, data_version: 0 })
    }))
    const wrapper = mountApp(); await flushPromises()
    expect(wrapper.find('[data-testid="login-page"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="erp-shell"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('重新登录')
  })
  it('does not render a late old-session order after a cross-tab identity change', async () => {
    const stale = deferred<Response>()
    vi.stubGlobal('fetch', vi.fn(async (url: string, _init?: RequestInit) => {
      if (url === '/api/auth/session') return json(authSession())
      if (url === '/api/orders') return stale.promise
      return json({ items: [], available: true, data_version: 0 })
    }))
    const wrapper = mountApp(); await flushPromises()
    window.dispatchEvent(new StorageEvent('storage', { key: 'yishuld:session-change:v1', newValue: '{}' }))
    await flushPromises()
    stale.resolve(json({ items: [order('A-private')] })); await flushPromises()
    expect(wrapper.find('[data-testid="login-page"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('A-private')
  })
  it('renders API failures in the real ERP without crashing AntD empty state', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string, _init?: RequestInit) => {
      if (url === '/api/auth/session') return json(authSession())
      if (url === '/api/orders') return json({ error: { code: 'UNAVAILABLE', message: '订单暂不可用' } }, 503)
      return json({ items: [], available: true, data_version: 0 })
    }))
    const wrapper = mountApp(); await flushPromises()
    expect(wrapper.find('[data-testid="list-error"]').text()).toContain('订单暂不可用')
    expect(wrapper.find('[data-testid="orders-empty"]').text()).toContain('暂时无法读取订单，请重试')
  })
  it('requires typed confirmation in the real reset modal and refreshes the ERP without logging out', async () => {
    const value = authSession(); value.user.permissions.push('data:reset')
    let version = 0
    const fetchMock = vi.fn(async (url: string, _init?: RequestInit) => {
      if (url === '/api/auth/session') return json(value)
      if (url === '/api/maintenance/reset') { version = 1; return json({ ...resetResult(), order_count: 1 }) }
      if (url === '/api/orders') return json({ items: [{ ...order(), data_version: version, refund: version === 0 ? refund() : null }] })
      return json({ items: [], available: true, data_version: version })
    })
    vi.stubGlobal('fetch', fetchMock)
    const wrapper = mountApp(); await flushPromises()
    await wrapper.find('[data-testid="reset-data-button"]').trigger('click'); await flushPromises()
    const modalElement = document.querySelector('.ant-modal')
    expect(modalElement).not.toBeNull()
    const modal = new DOMWrapper(modalElement!)
    expect(modal.text()).toContain('其他企业不受影响')
    expect(modal.text()).toContain('恢复前将自动归档')
    const confirm = modal.find('.ant-btn-dangerous')
    expect(confirm.attributes('disabled')).toBeDefined()
    await modal.find('#reset-confirmation').setValue('恢复')
    expect(confirm.attributes('disabled')).toBeDefined()
    expect(fetchMock.mock.calls.some((call) => call[0] === '/api/maintenance/reset')).toBe(false)
    await modal.find('#reset-confirmation').setValue('恢复初始数据')
    expect(confirm.attributes('disabled')).toBeUndefined()
    await confirm.trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="erp-shell"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="reset-success"]').text()).toContain('已恢复 1 笔初始订单')
    expect(wrapper.find('[data-testid="reset-success"]').text()).toContain('已自动归档')
    const resetCall = fetchMock.mock.calls.find((call) => call[0] === '/api/maintenance/reset')!
    expect(JSON.parse(resetCall[1]!.body as string)).toEqual({ confirmation: '恢复初始数据' })
    expect(wrapper.find('.orders-table').text()).toContain('未申请')
  })
  it('does not show data reset to viewers or operators without the server permission', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => url === '/api/auth/session' ? json(authSession('a-read')) : json({ items: [], available: true, data_version: 0 })))
    const wrapper = mountApp(); await flushPromises()
    expect(wrapper.find('[data-testid="reset-data-button"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('恢复初始数据')
  })
})
