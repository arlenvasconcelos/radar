import { describe, expect, it } from 'vitest'
import { createViewMemory } from './viewMemory'

describe('createViewMemory', () => {
  it('remembers the last resource kind list for the Resources section', () => {
    const memory = createViewMemory()
    memory.record('resources', '/resources/pods', '?filters=namespace:a&namespaces=x')
    memory.record('resources', '/resources/services', '?search=web')

    expect(memory.sectionPath('resources')).toBe('/resources/services?search=web')
  })

  it('never remembers the global namespace pick, the investigation focus or an open drawer', () => {
    const memory = createViewMemory()
    memory.record('resources', '/resources/pods', '?namespaces=a,b&ai-run=r1&resource=a/web&tab=yaml&full=1&problems=failed')

    expect(memory.sectionPath('resources')).toBe('/resources/pods?problems=failed')
  })

  it('returns to the list, not to a detail view opened on top of it', () => {
    const memory = createViewMemory()
    memory.record('helm', '/helm', '?release=default/web&releaseStorage=secret')
    memory.record('applications', '/applications', '?health=degraded&app=default/web&view=cost&workload=apps/Deployment/a/web&tab=logs&run=r1')
    memory.record('cnpg', '/cnpg', '?drawer=cluster:a/db&cat=backups')
    memory.record('timeline', '/timeline', '?event=e1&activity=warnings')

    expect(memory.sectionPath('helm')).toBe('/helm')
    expect(memory.sectionPath('applications')).toBe('/applications?health=degraded')
    expect(memory.sectionPath('cnpg')).toBe('/cnpg?cat=backups')
    expect(memory.sectionPath('timeline')).toBe('/timeline?activity=warnings')
  })

  it("keeps only the params that shape that section's list", () => {
    const memory = createViewMemory()
    memory.record('timeline', '/timeline', '?app=default/Deployment/web&scopeNamespaces=default&time=all&whats-new=1')
    memory.record('topology', '/topology', '?mode=traffic&group=namespace&unknown=x')
    memory.record('traffic', '/traffic', '?q=web')

    expect(memory.sectionPath('timeline')).toBe('/timeline?app=default%2FDeployment%2Fweb&scopeNamespaces=default&time=all')
    expect(memory.sectionPath('topology')).toBe('/topology?mode=traffic&group=namespace')
    expect(memory.sectionPath('traffic')).toBe('/traffic')
  })

  it('remembers section list routes but not their detail pages', () => {
    const memory = createViewMemory()
    memory.record('gitops', '/gitops', '?health=degraded')
    memory.record('gitops', '/gitops/argo/argocd/app', '?tab=tree')
    memory.record('home', '/', '')

    expect(memory.sectionPath('gitops')).toBe('/gitops?health=degraded')
    expect(memory.sectionPath('home')).toBe('/')
  })

  it('records a cleared list as no filters, so Clear filters also clears the memory', () => {
    const memory = createViewMemory()
    memory.record('resources', '/resources/pods', '?filters=namespace:a')
    memory.record('resources', '/resources/pods', '?namespaces=x')

    expect(memory.sectionPath('resources')).toBe('/resources/pods')
  })

  it('forgets everything on clear', () => {
    const memory = createViewMemory()
    memory.record('resources', '/resources/pods', '?filters=namespace:a')
    memory.clear()

    expect(memory.sectionPath('resources')).toBeUndefined()
  })
})
