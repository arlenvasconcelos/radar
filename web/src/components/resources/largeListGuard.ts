// List-scale policy for the resources view (issue #1303).
//
// Guard: kinds whose lists get big enough to hurt the browser switch to
// WINDOWED mode above LARGE_RESOURCE_LIST_LIMIT — the query fetches the
// summary projection with server-side sort+limit ({total, items} envelope),
// the table renders the first page, and a truncation banner + server-side
// q search cover the rest. Nothing is refused. largeListGuard.test.ts pins
// this decision table.
//
// Summary: kinds whose list fetch requests ?include=summary — the server
// projects each object down to the fields the table actually renders
// (internal/server/resource_summary_typed.go). The detail drawer fetches the
// full object through its own query, so the projection never reaches
// renderers.

// 35,000 measured against the loadtest harness (50-60k synthetic pods,
// summary projection): fetch 605ms / 457ms main-thread long tasks / 90MB JS
// heap at this window — comfortably inside budget, with linear scaling and
// no cliff up to 50k. Realistic-width pods land ~2x those figures.
export const LARGE_RESOURCE_LIST_LIMIT = 35000

// The bulk CPU/Memory columns are gated separately: /api/metrics/top/pods
// returns one unwindowed entry per pod and got none of the summary-projection
// slimming, so its threshold does not move with the list window.
export const BULK_POD_METRICS_LIMIT = 25000

export const LARGE_RESOURCE_LIST_GUARD_KEYS = new Set([
  'Pod',
  'Event',
  'apps/ReplicaSet',
  'discovery.k8s.io/EndpointSlice',
])

// Keyed by URL kind segment (lowercase plural), matching the list fetch path.
// Services and Jobs are unguarded but share the same payload problem at scale,
// so they take the summary projection too.
export const SUMMARY_LIST_KINDS = new Set([
  'pods',
  'events',
  'replicasets',
  'endpointslices',
  'services',
  'jobs',
])

export interface LargeListGuardInput {
  /** Resource-counts key for the selected kind ('' when none selected). */
  countKey: string
  /** Counts response arrived. */
  countsLoaded: boolean
  /** Counts request errored (guard fails open — list loads without a count). */
  countsErrored: boolean
  /** Counts response carries a number for this key. */
  countKnown: boolean
  /** Counts response marked this key unavailable (guard fails closed). */
  countUnavailable: boolean
  count: number | undefined
}

export interface LargeListGuardDecision {
  /** Selected kind is subject to the guard at all. */
  guarded: boolean
  /** Guarded kind, counts still in flight — hold the list query. */
  waitingForCount: boolean
  /**
   * Guarded kind over the limit (or count unavailable) — fetch windowed:
   * summary projection, server-side sort + LARGE_RESOURCE_LIST_LIMIT cap,
   * {total, items} envelope, truncation banner when total exceeds the page.
   */
  windowed: boolean
}

export function decideLargeListGuard(input: LargeListGuardInput): LargeListGuardDecision {
  const guarded = input.countKey !== '' && LARGE_RESOURCE_LIST_GUARD_KEYS.has(input.countKey)
  const waitingForCount = guarded && !input.countsLoaded && !input.countsErrored
  const windowed =
    guarded &&
    input.countsLoaded &&
    (input.countUnavailable || (input.countKnown && (input.count ?? 0) > LARGE_RESOURCE_LIST_LIMIT))
  return { guarded, waitingForCount, windowed }
}
