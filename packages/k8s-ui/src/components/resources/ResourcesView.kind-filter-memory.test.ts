import { describe, expect, it } from 'vitest'
import { kindFiltersFromSearch } from './ResourcesView'
import { serializeColumnFilters } from './resource-utils'

describe('kindFiltersFromSearch', () => {
  it('restores column filters, their exclude operator and problem filters', () => {
    const filters = serializeColumnFilters({ namespace: ['a', 'b'], status: ['Running'] }, { status: true })
    const search = `?${new URLSearchParams({ filters, problems: 'failed,pending', search: 'web' })}`

    expect(kindFiltersFromSearch(search)).toEqual({
      columnFilters: { namespace: ['a', 'b'], status: ['Running'] },
      columnFilterExcludes: { status: true },
      problemFilters: ['failed', 'pending'],
    })
  })

  it('starts empty for a kind with nothing remembered', () => {
    expect(kindFiltersFromSearch(undefined)).toEqual({ columnFilters: {}, columnFilterExcludes: {}, problemFilters: [] })
  })
})
