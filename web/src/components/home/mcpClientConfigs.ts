// Config snippets for every MCP client the setup dialog knows about.
//
// Radar's /mcp endpoint sits behind the auth middleware, so on a protected
// install these snippets are useless unless they carry a credential — an MCP
// client has no browser and cannot complete an OIDC or proxy login. A per-user
// API key is what closes that gap, and it has to reach the client through its
// own config syntax: most take a static `headers` map beside the URL, Codex
// spells it `http_headers` in TOML, and Claude Code takes `--header` on the
// command line.

export const API_KEY_PLACEHOLDER = 'rk_YOUR_API_KEY'

export interface MCPClientConfigs {
  claudeCode: string
  claudeDesktop: string
  cursor: string
  windsurf: string
  vsCode: string
  cline: string
  jetbrains: string
  codex: string
  gemini: string
}

/**
 * buildMCPClientConfigs renders each client's config for `mcpUrl`.
 *
 * `apiKey` null means the server needs no credential (a local, auth-disabled
 * install): the snippets stay exactly as they were, with no auth stanza at all.
 * Otherwise every snippet carries the key — pass {@link API_KEY_PLACEHOLDER}
 * when the user has not minted one yet, so the config is copy-then-substitute
 * rather than copy-then-discover-it-401s.
 */
export function buildMCPClientConfigs(mcpUrl: string, apiKey: string | null): MCPClientConfigs {
  const headers = apiKey ? { headers: { Authorization: `Bearer ${apiKey}` } } : {}
  const json = (value: unknown) => JSON.stringify(value, null, 2)

  return {
    claudeCode: apiKey
      ? `claude mcp add radar --transport http ${mcpUrl} --header "Authorization: Bearer ${apiKey}"`
      : `claude mcp add radar --transport http ${mcpUrl}`,

    claudeDesktop: json({ mcpServers: { radar: { type: 'http', url: mcpUrl, ...headers } } }),

    cursor: json({ mcpServers: { radar: { url: mcpUrl, ...headers } } }),

    windsurf: json({ mcpServers: { radar: { serverUrl: mcpUrl, ...headers } } }),

    vsCode: json({ servers: { radar: { type: 'http', url: mcpUrl, ...headers } } }),

    cline: json({ mcpServers: { radar: { url: mcpUrl, ...headers } } }),

    jetbrains: json({ mcpServers: { radar: { url: mcpUrl, ...headers } } }),

    codex: apiKey
      ? `[mcp_servers.radar]\nurl = "${mcpUrl}"\nhttp_headers = { Authorization = "Bearer ${apiKey}" }`
      : `[mcp_servers.radar]\nurl = "${mcpUrl}"`,

    gemini: json({ mcpServers: { radar: { httpUrl: mcpUrl, ...headers } } }),
  }
}
