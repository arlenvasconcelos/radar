import { describe, expect, it } from 'vitest'
import { ACCESS_CHECK_RETRIES, shouldRetryAccessCheck } from './NoClusterAccessBanner'

// /auth/me can answer before the first namespace discovery finishes; the
// banner must not stay hidden for the whole session because of that race.
describe('shouldRetryAccessCheck', () => {
  it('retries while access is not known yet, a bounded number of times', () => {
    expect(shouldRetryAccessCheck({ authEnabled: true }, 0)).toBe(true)
    expect(shouldRetryAccessCheck({ authEnabled: true }, ACCESS_CHECK_RETRIES - 1)).toBe(true)
    expect(shouldRetryAccessCheck({ authEnabled: true }, ACCESS_CHECK_RETRIES)).toBe(false)
  })

  it('stops once access is known either way', () => {
    expect(shouldRetryAccessCheck({ authEnabled: true, noNamespaceAccess: false }, 0)).toBe(false)
    expect(shouldRetryAccessCheck({ authEnabled: true, noNamespaceAccess: true }, 0)).toBe(false)
  })

  it('never retries without auth', () => {
    expect(shouldRetryAccessCheck({ authEnabled: false }, 0)).toBe(false)
    expect(shouldRetryAccessCheck(undefined, 0)).toBe(false)
  })
})
