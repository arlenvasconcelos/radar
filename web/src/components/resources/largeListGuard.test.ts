import { describe, expect, it } from 'vitest'
import { decideLargeListGuard, LARGE_RESOURCE_LIST_LIMIT } from './largeListGuard'

// Stage-3 contract (issue #1303): guarded kinds above the limit are no longer
// refused — they switch to WINDOWED mode: the list query runs with
// include=summary + sort/limit window params, renders the first
// LARGE_RESOURCE_LIST_LIMIT rows, and the truncation banner + server-side
// q search cover the rest. The Stage-0 pin of the old refusal behavior was
// rewritten into this table on purpose — same decision function, new meaning.

const base = {
  countKey: 'Pod',
  countsLoaded: true,
  countsErrored: false,
  countKnown: true,
  countUnavailable: false,
  count: 0,
}

describe('decideLargeListGuard', () => {
  it('windows a guarded kind above the limit instead of refusing', () => {
    const d = decideLargeListGuard({ ...base, count: LARGE_RESOURCE_LIST_LIMIT + 1 })
    expect(d).toEqual({ guarded: true, waitingForCount: false, windowed: true })
  })

  it('loads a guarded kind at or under the limit unwindowed (the reported cluster: 29,455 pods)', () => {
    // The 35k window was measured against the loadtest harness; the #1303
    // cluster fits entirely — no banner, no truncation.
    expect(decideLargeListGuard({ ...base, count: 29455 }).windowed).toBe(false)
    expect(decideLargeListGuard({ ...base, count: LARGE_RESOURCE_LIST_LIMIT }).windowed).toBe(false)
  })

  it('never windows unguarded kinds, whatever their count', () => {
    const d = decideLargeListGuard({ ...base, countKey: 'Service', count: 500000 })
    expect(d).toEqual({ guarded: false, waitingForCount: false, windowed: false })
  })

  it('holds the list query while counts are in flight for a guarded kind', () => {
    const d = decideLargeListGuard({ ...base, countsLoaded: false, countKnown: false, count: undefined })
    expect(d.waitingForCount).toBe(true)
    expect(d.windowed).toBe(false)
  })

  it('fails open when the counts request errored (list loads unwindowed)', () => {
    const d = decideLargeListGuard({
      ...base,
      countsLoaded: false,
      countsErrored: true,
      countKnown: false,
      count: undefined,
    })
    expect(d.waitingForCount).toBe(false)
    expect(d.windowed).toBe(false)
  })

  it('degrades to windowed mode when the count is unavailable for a guarded kind', () => {
    // The server enforces the window limit, so an unknown count is safe to
    // load windowed — the old behavior refused outright.
    const d = decideLargeListGuard({ ...base, countKnown: false, countUnavailable: true, count: undefined })
    expect(d.windowed).toBe(true)
  })
})
