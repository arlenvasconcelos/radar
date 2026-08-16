// List-scale policy for the resources view.
//
// Guard: kinds whose lists get big enough to hurt the browser refuse to load
// above LARGE_RESOURCE_LIST_LIMIT, pointing the user at the namespace filter.
// The counts endpoint is namespace-scoped, so narrowing the namespace drops
// the count below the limit and the list loads — that's the way out.
//
// Summary: kinds whose list fetch requests ?include=summary — the server
// projects each object down to the fields the table actually renders
// (internal/server/resource_summary_typed.go). The detail drawer fetches the
// full object through its own query, so the projection never reaches
// renderers.

// 35,000 measured against the loadtest harness (50-60k synthetic pods,
// summary projection): 605ms fetch / 457ms main-thread long tasks / 90MB JS
// heap, scaling linearly with no cliff up to 50k. Realistic-width pods land
// ~2x those figures. The projection is what makes this limit affordable —
// raising it further without re-measuring is not safe.
export const LARGE_RESOURCE_LIST_LIMIT = 35000

// The bulk CPU/Memory columns are gated separately: /api/metrics/top/pods
// returns one entry per pod and got none of the summary-projection slimming,
// so its threshold does not move with the list limit.
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
  /** Guarded kind, counts still in flight — hold the list query. */
  waitingForCount: boolean
  /** Guarded kind over the limit (or count unavailable) — refuse the list. */
  blocked: boolean
}

export function decideLargeListGuard(input: LargeListGuardInput): LargeListGuardDecision {
  const guarded = input.countKey !== '' && LARGE_RESOURCE_LIST_GUARD_KEYS.has(input.countKey)
  const waitingForCount = guarded && !input.countsLoaded && !input.countsErrored
  const blocked =
    guarded &&
    input.countsLoaded &&
    (input.countUnavailable || (input.countKnown && (input.count ?? 0) > LARGE_RESOURCE_LIST_LIMIT))
  return { waitingForCount, blocked }
}
