import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import BrandLogo from '../components/BrandLogo.vue'
import logoUrl from '../assets/yishuld-logo.png'
import html from '../../index.html?raw'
import component from '../components/BrandLogo.vue?raw'
import globalCss from '../style.css?raw'

describe('official brand asset', () => {
  it.each(['login', 'sidebar'] as const)('renders the original local image in the %s variant without a duplicate wordmark', (variant) => {
    const wrapper = mount(BrandLogo, { props: { variant } })
    const image = wrapper.get('img')
    expect(wrapper.classes()).toContain(`brand-logo--${variant}`)
    expect(image.attributes('src')).toBe(logoUrl)
    expect(image.attributes('alt')).toBe('易数链动')
    expect(image.attributes('width')).toBe('1825')
    expect(image.attributes('height')).toBe('862')
    expect(wrapper.findAll('img')).toHaveLength(1)
    expect(wrapper.find('svg').exists()).toBe(false)
    expect(wrapper.text()).toBe('')
    wrapper.unmount()
  })
  it('keeps the wordmark contained and unmodified at responsive widths', () => {
    expect(component).toMatch(/max-width:\s*100%/)
    expect(component).toMatch(/height:\s*auto/)
    expect(component).toMatch(/object-fit:\s*contain/)
    expect(component).toMatch(/background:\s*#fff/)
    expect(component).not.toMatch(/(?:filter|transform|object-position):/)
    expect(globalCss).not.toContain('.brand>span:last-child')
    expect(globalCss).not.toContain('.brand-mark')
  })
  it('references the same local PNG for the Vite-processed favicon', () => {
    expect(html).toContain('<link rel="icon" type="image/png" href="/src/assets/yishuld-logo.png" />')
  })
})
