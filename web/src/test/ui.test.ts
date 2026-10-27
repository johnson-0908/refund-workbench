import { describe, expect, it } from 'vitest'
import { uiComponents } from '../ui'
import appTemplate from '../App.vue?raw'
import loginTemplate from '../components/LoginPage.vue?raw'
import statusTemplate from '../components/StatusBadge.vue?raw'

describe('production UI registry', () => {
  it('registers every Ant Design template component without a whole-library plugin', () => {
    const templates = [appTemplate, loginTemplate, statusTemplate].join('\n')
    const used = [...templates.matchAll(/<a-([a-z-]+)(?=[\s/>])/g)]
      .map((match) => `A${match[1]!.split('-').map((part) => part[0]!.toUpperCase() + part.slice(1)).join('')}`)
    expect(Object.keys(uiComponents).sort()).toEqual([...new Set(used)].sort())
  })

  it('keeps programmatic dialogs and unused input variants out of the registered surface', () => {
    expect(uiComponents).not.toHaveProperty('AInputPassword')
    expect(uiComponents).not.toHaveProperty('AInputSearch')
    expect(uiComponents).not.toHaveProperty('ATooltip')
    expect(uiComponents.AModal).not.toHaveProperty('confirm')
    expect(uiComponents.AModal).not.toHaveProperty('useModal')
  })
})
