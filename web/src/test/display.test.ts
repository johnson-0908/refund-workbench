import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import StatusBadge from '../components/StatusBadge.vue'
import { dateTime, money, statusInfo } from '../lib/format'

describe('amount and status display', () => {
  it.each([[19900, '¥199.00'], [8800, '¥88.00'], [1, '¥0.01'], [0, '¥0.00'], [123456789, '¥1,234,567.89']])('formats integer cents %s as %s', (value, expected) => {
    expect(money(value as number)).toBe(expected)
  })
  it('does not silently round invalid cents', () => { expect(money(1.5)).toBe('金额异常') })
  it('displays timestamps explicitly in Beijing time', () => { expect(dateTime('2026-10-27T01:02:03Z')).toContain('09:02:03') })
  it.each(['PROCESSING', 'UNKNOWN', 'SUCCEEDED', 'FAILED'] as const)('renders %s with a human-readable status', (status) => {
    const wrapper = mount(StatusBadge, { props: { status }, global: { stubs: { ATag: { template: '<span><slot /></span>' } } } })
    expect(wrapper.text()).toBe(statusInfo[status].label)
  })
  it('renders no-refund status without confusing it with pending', () => {
    const wrapper = mount(StatusBadge, { props: { status: null }, global: { stubs: { ATag: { template: '<span><slot /></span>' } } } })
    expect(wrapper.text()).toBe('未申请')
  })
})
