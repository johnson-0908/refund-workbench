/** Cryptographic UUIDv4, including HTTP/IP deployments where randomUUID is absent. */
export function createUUID(): string {
  const crypto = globalThis.crypto
  if (typeof crypto?.randomUUID === 'function') return crypto.randomUUID()
  // getRandomValues is also available in insecure contexts; never fall back to Math.random.
  // https://developer.mozilla.org/en-US/docs/Web/API/Crypto/getRandomValues
  if (typeof crypto?.getRandomValues !== 'function') throw new Error('Secure randomness is unavailable')
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  bytes[6] = (bytes[6]! & 0x0f) | 0x40
  bytes[8] = (bytes[8]! & 0x3f) | 0x80
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}
