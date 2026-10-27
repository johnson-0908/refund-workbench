import { afterEach, describe, expect, it, vi } from 'vitest'
import { createUUID } from '../lib/uuid'

afterEach(() => vi.unstubAllGlobals())

describe('cryptographic UUID generation in secure and HTTP contexts', () => {
  it('uses the native UUID implementation when available', () => {
    const native = vi.fn(() => '00112233-4455-4677-8899-aabbccddeeff')
    const getRandomValues = vi.fn()
    vi.stubGlobal('crypto', { randomUUID: native, getRandomValues })
    expect(createUUID()).toBe('00112233-4455-4677-8899-aabbccddeeff')
    expect(native).toHaveBeenCalledOnce()
    expect(getRandomValues).not.toHaveBeenCalled()
  })
  it('constructs RFC 4122 version and variant bits from 16 cryptographically random bytes', () => {
    const getRandomValues = vi.fn((bytes: Uint8Array) => {
      expect(bytes).toBeInstanceOf(Uint8Array)
      expect(bytes).toHaveLength(16)
      for (let index = 0; index < bytes.length; index++) bytes[index] = index
      return bytes
    })
    vi.stubGlobal('crypto', { getRandomValues })
    expect(createUUID()).toBe('00010203-0405-4607-8809-0a0b0c0d0e0f')
    expect(getRandomValues).toHaveBeenCalledOnce()
  })
  it('fails closed without secure randomness rather than creating weak idempotency keys', () => {
    vi.stubGlobal('crypto', {})
    const weakRandom = vi.spyOn(Math, 'random')
    expect(() => createUUID()).toThrow('Secure randomness is unavailable')
    expect(weakRandom).not.toHaveBeenCalled()
  })
})
