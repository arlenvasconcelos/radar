import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { MCPSetupDialog } from './MCPSetupDialog'
import { API_KEY_PLACEHOLDER } from './mcpClientConfigs'

let mockAuthMe: { authEnabled: boolean; apiKeysEnabled?: boolean } = { authEnabled: false }

vi.mock('../../api/client', () => ({
  useAuthMe: () => ({ data: mockAuthMe }),
}))

vi.mock('../../api/apiKeys', () => ({
  useCreateAPIKey: () => ({
    mutate: vi.fn(),
    reset: vi.fn(),
    isPending: false,
    isError: false,
    error: null,
  }),
}))

// No DOM environment is installed, and the dialog reads window.location.port
// for the desktop port-pin hint. renderToString never runs effects, so a
// location stub is all the component touches.
vi.stubGlobal('window', { location: { port: '9280' } })

beforeEach(() => {
  mockAuthMe = { authEnabled: false }
})

const render = () =>
  renderToString(
    <MCPSetupDialog open onClose={() => {}} mcpUrl="https://radar.example.com/mcp" />,
  )

describe('MCPSetupDialog authentication', () => {
  // A laptop install has no auth; injecting a credential stanza would hand
  // every local user a config that is wrong for their server.
  it('says nothing about credentials when auth is off', () => {
    const html = render()
    expect(html).not.toContain('Authentication')
    expect(html).not.toContain(API_KEY_PLACEHOLDER)
    expect(html.toLowerCase()).not.toContain('authorization')
  })

  // /mcp is behind the auth middleware, so on a protected Radar the snippets
  // 401 unless they carry a key. The dialog shapes the config for one and
  // points at Settings, but never mints: a credential that does not expire
  // should not be a side effect of opening setup instructions, and a snippet
  // holding a live key invites being pasted into a ticket or chat.
  it('shows the placeholder and links to key creation when keys are available', () => {
    mockAuthMe = { authEnabled: true, apiKeysEnabled: true }
    const html = render()

    expect(html).toContain('Authentication')
    expect(html).toContain(API_KEY_PLACEHOLDER)
    expect(html).toContain('Authorization: Bearer')
    expect(html).toContain('Create a key')
    // No inline minting: nothing to paste an existing key into either.
    expect(html).not.toContain('Existing API key')
  })

  // Auth on but no key store (or Radar Cloud, where the Hub issues tokens):
  // there is no key to inject, so say why rather than emit a config that
  // cannot be made to work.
  it('explains the gap when keys are not enabled', () => {
    mockAuthMe = { authEnabled: true, apiKeysEnabled: false }
    const html = render()

    expect(html).toContain('Authentication')
    expect(html).toContain('--auth-api-keys-file')
    expect(html).not.toContain('Create a key')
    expect(html).not.toContain(API_KEY_PLACEHOLDER)
  })
})
