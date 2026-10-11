import { kindFiltersFromSearch, kindFiltersToSearch } from '../components/resources/resource-utils'
import { RESOURCE_LIST_PATH } from './navigation'

const FILTERS_PREFIX = 'radar-filters:'
const LAST_KIND_PREFIX = 'radar-last-kind:'

/**
 * Per-cluster Resources filters (one query string per kind) and the last
 * Resources kind, kept in localStorage so they survive reloads and new tabs.
 * Storage can be unavailable (private mode, blocked site data): every access
 * degrades to "nothing remembered".
 *
 * Filters are read from this instance's own memory first, so a tab keeps
 * showing what happened in that tab while another tab saves different
 * filters; localStorage only seeds a kind the first time the tab reads it.
 */
export function createKindFilterStore(cluster: string) {
  const filtersKey = (plural: string, group: string) => `${FILTERS_PREFIX}${cluster}:${plural}/${group}`
  const lastKindKey = LAST_KIND_PREFIX + cluster
  const tab = new Map<string, string>()

  return {
    get(plural: string, group: string): string | undefined {
      const key = filtersKey(plural, group)
      if (tab.has(key)) return tab.get(key) || undefined
      let saved: string | undefined
      try {
        saved = localStorage.getItem(key) ?? undefined
      } catch { /* unavailable: nothing saved */ }
      // What the tab was given is now the tab's own, whatever other tabs save later.
      tab.set(key, saved ?? '')
      return saved
    },
    /** An empty search removes the entry. */
    set(plural: string, group: string, search: string) {
      tab.set(filtersKey(plural, group), search)
      try {
        if (search) localStorage.setItem(filtersKey(plural, group), search)
        else localStorage.removeItem(filtersKey(plural, group))
      } catch { /* ignore */ }
    },
    /** Removes one column's filter from every kind of this cluster, in this tab and in storage. */
    clearColumn(column: string) {
      const withoutColumn = (search: string) => {
        const filters = kindFiltersFromSearch(search)
        delete filters.columnFilters[column]
        delete filters.columnFilterExcludes[column]
        return kindFiltersToSearch(filters)
      }
      for (const [key, search] of tab) tab.set(key, withoutColumn(search))
      const prefix = `${FILTERS_PREFIX}${cluster}:`
      try {
        const keys: string[] = []
        for (let i = 0; i < localStorage.length; i++) {
          const key = localStorage.key(i)
          // The rest is `<plural>/<group>`, never a colon: guards against a context named `<cluster>:…`.
          if (key?.startsWith(prefix) && !key.slice(prefix.length).includes(':')) keys.push(key)
        }
        for (const key of keys) {
          const next = withoutColumn(localStorage.getItem(key) ?? '')
          if (next) localStorage.setItem(key, next)
          else localStorage.removeItem(key)
        }
      } catch { /* ignore */ }
    },
    /** The last Resources list path (`/resources/<plural>[?apiGroup=…]`). */
    lastKind(): string | undefined {
      try {
        return localStorage.getItem(lastKindKey) ?? undefined
      } catch {
        return undefined
      }
    },
    /** Records a location; only a Resources list path counts. */
    recordLastKind(pathname: string, search: string) {
      const path = pathname.replace(/\/+$/, '')
      if (!RESOURCE_LIST_PATH.test(path)) return
      const group = new URLSearchParams(search).get('apiGroup')
      const value = group ? `${path}?${new URLSearchParams({ apiGroup: group })}` : path
      try {
        localStorage.setItem(lastKindKey, value)
      } catch { /* ignore */ }
    },
  }
}

export type KindFilterStore = ReturnType<typeof createKindFilterStore>
