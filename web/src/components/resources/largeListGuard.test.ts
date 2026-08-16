import { describe, expect, it } from 'vitest'
import { decideLargeListGuard, LARGE_RESOURCE_LIST_LIMIT } from './largeListGuard'

const base = {
  countKey: 'Pod',
  countsLoaded: true,
  countsErrored: false,
  countKnown: true,
  countUnavailable: false,
  count: 0,
}

describe('decideLargeListGuard', () => {
  it('blocks a guarded kind above the limit', () => {
    const d = decideLargeListGuard({ ...base, count: LARGE_RESOURCE_LIST_LIMIT + 1 })
    expect(d).toEqual({ waitingForCount: false, blocked: true })
  })

  it('loads a guarded kind at or under the limit', () => {
    expect(decideLargeListGuard({ ...base, count: LARGE_RESOURCE_LIST_LIMIT }).blocked).toBe(false)
    expect(decideLargeListGuard({ ...base, count: 24000 }).blocked).toBe(false)
  })

  it('loads the reported cluster (29,455 pods), which the raised limit now covers', () => {
    expect(decideLargeListGuard({ ...base, count: 29455 })).toEqual({
      waitingForCount: false,
      blocked: false,
    })
  })

  it('never blocks unguarded kinds, whatever their count', () => {
    const d = decideLargeListGuard({ ...base, countKey: 'Service', count: 500000 })
    expect(d).toEqual({ waitingForCount: false, blocked: false })
  })

  it('holds the list query while counts are in flight for a guarded kind', () => {
    const d = decideLargeListGuard({ ...base, countsLoaded: false, countKnown: false, count: undefined })
    expect(d.waitingForCount).toBe(true)
    expect(d.blocked).toBe(false)
  })

  it('fails open when the counts request errored', () => {
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
