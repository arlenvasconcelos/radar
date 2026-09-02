import { describe, expect, it } from 'vitest'
import { API_KEY_PLACEHOLDER, buildMCPClientConfigs, type MCPClientConfigs } from './mcpClientConfigs'

const URL = 'https://radar.example.com/mcp'
const KEY = 'rk_0123456789abcdef'

const ALL_CLIENTS: (keyof MCPClientConfigs)[] = [
  'claudeCode', 'claudeDesktop', 'cursor', 'windsurf',
  'vsCode', 'cline', 'jetbrains', 'codex', 'gemini',
]

describe('buildMCPClientConfigs', () => {
  it('emits no auth stanza when the server needs no credential', () => {
    const configs = buildMCPClientConfigs(URL, null)
    for (const client of ALL_CLIENTS) {
      expect(configs[client], client).toContain(URL)
      expect(configs[client].toLowerCase(), client).not.toContain('authorization')
    }
  })

  // Every client gets the credential, in its own syntax: a snippet that reaches
  // a protected /mcp without one is a 401 the user has no way to diagnose.
  it('carries the key to every client', () => {
    const configs = buildMCPClientConfigs(URL, KEY)
    for (const client of ALL_CLIENTS) {
      expect(configs[client], client).toContain(KEY)
      expect(configs[client], client).toContain('Authorization')
    }
  })

  it('uses each client’s own header syntax', () => {
    const c = buildMCPClientConfigs(URL, KEY)

    expect(c.claudeCode).toBe(
      `claude mcp add radar --transport http ${URL} --header "Authorization: Bearer ${KEY}"`,
    )
    expect(c.codex).toBe(
      `[mcp_servers.radar]\nurl = "${URL}"\nhttp_headers = { Authorization = "Bearer ${KEY}" }`,
    )

    // The JSON clients differ only in where the URL hangs; all take `headers`.
    expect(JSON.parse(c.claudeDesktop).mcpServers.radar)
      .toEqual({ type: 'http', url: URL, headers: { Authorization: `Bearer ${KEY}` } })
    expect(JSON.parse(c.cursor).mcpServers.radar)
      .toEqual({ url: URL, headers: { Authorization: `Bearer ${KEY}` } })
    expect(JSON.parse(c.windsurf).mcpServers.radar)
      .toEqual({ serverUrl: URL, headers: { Authorization: `Bearer ${KEY}` } })
    expect(JSON.parse(c.vsCode).servers.radar)
      .toEqual({ type: 'http', url: URL, headers: { Authorization: `Bearer ${KEY}` } })
    expect(JSON.parse(c.gemini).mcpServers.radar)
      .toEqual({ httpUrl: URL, headers: { Authorization: `Bearer ${KEY}` } })
    expect(JSON.parse(c.cline).mcpServers.radar)
      .toEqual({ url: URL, headers: { Authorization: `Bearer ${KEY}` } })
    expect(JSON.parse(c.jetbrains).mcpServers.radar)
      .toEqual({ url: URL, headers: { Authorization: `Bearer ${KEY}` } })
  })

  it('stays valid with the placeholder, so an un-keyed copy is substitutable', () => {
    const c = buildMCPClientConfigs(URL, API_KEY_PLACEHOLDER)
    expect(() => JSON.parse(c.cursor)).not.toThrow()
    expect(JSON.parse(c.cursor).mcpServers.radar.headers.Authorization)
      .toBe(`Bearer ${API_KEY_PLACEHOLDER}`)
    expect(c.claudeCode).toContain(API_KEY_PLACEHOLDER)
  })
})
