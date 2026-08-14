import { describe, expect, it } from 'vitest'
import { decideLargeListGuard, LARGE_RESOURCE_LIST_LIMIT } from './largeListGuard'

// Pins the CURRENT 25k-guard behavior (issue #1303: "cannot display more than
// 25000 pods" — a hard refusal, added in #960). When the guard is converted
// to server-side windowing + truncation, `blocked` stops meaning "refuse the
// fetch" and this table flips with it — rewrite these assertions, don't
// delete them.

const base = {
  countKey: 'Pod',
  countsLoaded: true,
  countsErrored: false,
  countKnown: true,
  countUnavailable: false,
  count: 0,
}

describe('decideLargeListGuard', () => {
  it('refuses a guarded kind above the limit (the reported cluster: 29,455 pods)', () => {
    const d = decideLargeListGuard({ ...base, count: 29455 })
    expect(d).toEqual({ guarded: true, waitingForCount: false, blocked: true })
  })

  it('allows a guarded kind at or under the limit', () => {
    expect(decideLargeListGuard({ ...base, count: LARGE_RESOURCE_LIST_LIMIT }).blocked).toBe(false)
    expect(decideLargeListGuard({ ...base, count: 24000 }).blocked).toBe(false)
  })

  it('never blocks unguarded kinds, whatever their count (Services/Jobs today)', () => {
    const d = decideLargeListGuard({ ...base, countKey: 'Service', count: 500000 })
    expect(d).toEqual({ guarded: false, waitingForCount: false, blocked: false })
  })

  it('holds the list query while counts are in flight for a guarded kind', () => {
    const d = decideLargeListGuard({ ...base, countsLoaded: false, countKnown: false, count: undefined })
    expect(d.waitingForCount).toBe(true)
    expect(d.blocked).toBe(false)
  })

  it('fails open when the counts request errored (list loads unguarded)', () => {
    const d = decideLargeListGuard({
      ...base,
      countsLoaded: false,
      countsErrored: true,
      countKnown: false,
      count: undefined,
    })
    expect(d.waitingForCount).toBe(false)
    expect(d.blocked).toBe(false)
  })

  it('fails closed when the count is unavailable for a guarded kind', () => {
    const d = decideLargeListGuard({ ...base, countKnown: false, countUnavailable: true, count: undefined })
    expect(d.blocked).toBe(true)
  })
})
