import { useRef, useEffect, useState, useCallback } from 'react'
import {
  X, Copy, Check, Radio, Terminal, MessageSquare, Code2, ChevronRight, Pin,
  AlertTriangle,
} from 'lucide-react'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { useAuthMe } from '../../api/client'
import { MCP_TOOL_CATALOG } from './mcpToolCatalog'
import { API_KEY_PLACEHOLDER, buildMCPClientConfigs } from './mcpClientConfigs'
import { Tooltip } from '../ui/Tooltip'

interface MCPSetupDialogProps {
  open: boolean
  onClose: () => void
  mcpUrl: string
}

// `navigator.clipboard` is undefined on insecure origins, where a
// fire-and-forget write fails silently and the user is left believing they
// copied the snippet. Failure is surfaced instead; the text stays selectable
// as the fallback.
function CopyButton({ text, className }: { text: string; className?: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle')

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(text)
      setState('copied')
    } catch {
      setState('failed')
    }
    setTimeout(() => setState('idle'), 2500)
  }

  const label =
    state === 'failed' ? 'Copy failed — select the text and copy manually' : 'Copy to clipboard'

  return (
    <Tooltip content={label} position="left" wrapperClassName={className ?? 'absolute top-2 right-2'}>
    <button
      onClick={handleCopy}
      aria-label={label}
      className="p-1.5 rounded-md bg-theme-elevated/50 hover:bg-theme-elevated text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
    >
      {state === 'copied' && <Check className="w-3.5 h-3.5 text-green-500" />}
      {state === 'failed' && <AlertTriangle className="w-3.5 h-3.5 text-red-500" />}
      {state === 'idle' && <Copy className="w-3.5 h-3.5" />}
    </button>
    </Tooltip>
  )
}

function CodeBlock({ children }: { children: string }) {
  return (
    <div className="relative group">
      <pre className="bg-theme-base rounded-md px-3 py-2.5 text-xs font-mono text-theme-text-secondary overflow-x-auto whitespace-pre-wrap break-all">
        {children}
      </pre>
      <CopyButton text={children} />
    </div>
  )
}

export function MCPSetupDialog({ open, onClose, mcpUrl }: MCPSetupDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const [isDesktop, setIsDesktop] = useState(false)
  const [portPinned, setPortPinned] = useState(false)
  const [pinning, setPinning] = useState(false)
  const [pinSuccess, setPinSuccess] = useState(false)
  const [pinError, setPinError] = useState('')

  useEffect(() => {
    if (!open) return
    setPinSuccess(false)
    setPinError('')
    fetch(apiUrl('/config'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      .then((res) => res.ok ? res.json() : null)
      .then((data) => {
        if (data) {
          setIsDesktop(data.isDesktop ?? false)
          setPortPinned(data.file?.port != null && data.file.port > 0)
        }
      })
      .catch((err) => console.warn('[mcp-setup] Failed to load config:', err))
  }, [open])

  const handlePinPort = useCallback(async () => {
    const currentPort = Number(window.location.port) || 80
    setPinning(true)
    setPinError('')
    try {
      const configRes = await fetch(apiUrl('/config'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      if (!configRes.ok) {
        setPinError('Failed to load current config')
        return
      }
      const configData = await configRes.json()
      const updated = { ...configData.file, port: currentPort }
      const res = await fetch(apiUrl('/config'), {
        method: 'PUT',
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify(updated),
      })
      if (res.ok) {
        setPortPinned(true)
        setPinSuccess(true)
      } else {
        setPinError('Failed to save port configuration')
      }
    } catch {
      setPinError('Failed to pin port — check server connection')
    } finally {
      setPinning(false)
    }
  }, [])

  const { data: authMe } = useAuthMe()

  useEffect(() => {
    if (!open) return
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [open, onClose])

  useEffect(() => {
    if (open && dialogRef.current) {
      dialogRef.current.focus()
    }
  }, [open])

  if (!open) return null

  // /mcp sits behind the auth middleware, so on a protected Radar every snippet
  // below needs a credential. A browser-less MCP client can't complete the
  // login, which is exactly what an API key is for.
  const needsAuth = authMe?.authEnabled === true
  const keysAvailable = needsAuth && authMe?.apiKeysEnabled === true
  // Snippets carry the placeholder, never a live key. Minting belongs in
  // Settings: a credential that never expires should not be a side effect of
  // opening setup instructions, and a snippet holding a real key invites being
  // pasted into a ticket or chat.
  const snippetKey = keysAvailable ? API_KEY_PLACEHOLDER : null

  const openKeySettings = () => {
    onClose()
    window.dispatchEvent(
      new CustomEvent('radar:open-settings', { detail: { section: 'apikeys' } }),
    )
  }

  const currentPort = Number(window.location.port) || 80

  const configs = buildMCPClientConfigs(mcpUrl, snippetKey)

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div
        ref={dialogRef}
        tabIndex={-1}
        className="relative dialog max-w-3xl w-full mx-4 outline-none max-h-[85vh] flex flex-col"
      >
        {/* Header */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-theme-border/50 shrink-0">
          <div className="flex items-center gap-3">
            <Radio className="w-5 h-5 text-purple-400" />
            <h3 className="text-lg font-semibold text-theme-text-primary">MCP Server</h3>
          </div>
          <button onClick={onClose} className="p-1.5 hover:bg-theme-elevated rounded-md transition-colors">
            <X className="w-5 h-5 text-theme-text-tertiary" />
          </button>
        </div>

        {/* Scrollable content */}
        <div className="overflow-y-auto flex-1 min-h-0 px-6 py-5 space-y-6">
          {/* Explanation */}
          <div className="space-y-3">
            <h4 className="text-sm font-semibold text-theme-text-primary">AI meets your cluster</h4>
            <p className="text-sm text-theme-text-secondary leading-relaxed">
              Radar exposes a{' '}
              <a href="https://modelcontextprotocol.io" target="_blank" rel="noopener noreferrer" className="text-purple-400 hover:text-purple-300 underline underline-offset-2">
                Model Context Protocol
              </a>{' '}
              (MCP) server that lets AI agents query your cluster through Radar.
              Unlike raw kubectl access, Radar gives your AI pre-processed, enriched data —
              topology graphs, health assessments, deduplicated events, filtered logs — so it
              can understand your cluster state quickly without burning through context on
              verbose YAML output.
            </p>
            <p className="text-sm text-theme-text-secondary leading-relaxed">
              Read tools are strictly read-only. Write tools (restart, scale, sync, apply,
              node drain) are annotated as destructive so your AI client can flag them and
              prompt before running.
            </p>
          </div>

          {/* Endpoint */}
          <div className="space-y-2">
            <h4 className="text-sm font-semibold text-theme-text-primary">Endpoint</h4>
            <div className="relative">
              <div className="flex items-center gap-3 bg-theme-base rounded-md px-3 py-2.5">
                <span className="badge text-purple-400 bg-purple-500/10">HTTP</span>
                <code className="inline-code text-sm">{mcpUrl}</code>
              </div>
              <CopyButton text={mcpUrl} />
            </div>

            {isDesktop && !portPinned && (
              <div className="flex items-center gap-2 px-3 py-2 text-xs bg-amber-500/10 border border-amber-500/20 rounded-md">
                <span className="text-amber-700 dark:text-amber-300 flex-1">
                  Port changes on every restart. Pin it to keep a stable MCP endpoint.
                </span>
                <button
                  onClick={handlePinPort}
                  disabled={pinning}
                  className="shrink-0 flex items-center gap-1 px-2.5 py-1 text-xs font-medium text-amber-800 dark:text-amber-200 hover:text-amber-900 dark:hover:text-white bg-amber-500/20 hover:bg-amber-500/30 rounded transition-colors disabled:opacity-50"
                >
                  <Pin className="w-3 h-3" />
                  Pin port {currentPort}
                </button>
              </div>
            )}

            {isDesktop && pinSuccess && (
              <p className="text-xs text-green-700 dark:text-green-400/80 px-0.5">
                Port {currentPort} pinned. MCP endpoint will remain stable across restarts.
              </p>
            )}

            {isDesktop && pinError && (
              <p className="text-xs text-red-600 dark:text-red-400 px-0.5">
                {pinError}
              </p>
            )}

            {isDesktop && (
              <p className="text-xs text-theme-text-tertiary px-0.5">
                You can change the port in{' '}
                <button
                  onClick={() => { onClose(); window.dispatchEvent(new Event('radar:open-settings')) }}
                  className="text-purple-500 dark:text-purple-400 hover:underline underline-offset-2"
                >
                  Settings
                </button>.
              </p>
            )}
          </div>

          {/* Authentication — /mcp sits behind the auth middleware, so on a
              protected Radar the snippets below are useless without a credential. */}
          {needsAuth && (
            <div className="space-y-2">
              <h4 className="text-sm font-semibold text-theme-text-primary">Authentication</h4>

              {keysAvailable ? (
                <>
                  <p className="text-sm text-theme-text-secondary leading-relaxed">
                    This Radar requires a sign-in, and an MCP client has no browser to complete one.
                    Give it an API key instead — the key acts as you, so your agent sees exactly the
                    namespaces and resources your own account sees.
                  </p>

                  <p className="text-sm text-theme-text-secondary leading-relaxed">
                    The snippets below carry a{' '}
                    <code className="inline-code">{API_KEY_PLACEHOLDER}</code> placeholder.{' '}
                    <button
                      onClick={openKeySettings}
                      className="text-purple-500 dark:text-purple-400 hover:underline underline-offset-2"
                    >
                      Create a key
                    </button>{' '}
                    and substitute it after copying. Keys do not expire, so revoke one there when its
                    client is retired.
                  </p>
                </>
              ) : (
                <p className="text-sm text-theme-text-secondary leading-relaxed">
                  This Radar requires a sign-in, so the endpoint above rejects clients that send no
                  credential. Per-user API keys — the usual way to connect an MCP client that has no
                  browser — are not enabled here; ask whoever runs this server about{' '}
                  <code className="inline-code">--auth-api-keys-file</code>, or use the token your
                  identity provider issues.
                </p>
              )}
            </div>
          )}

          {/* Setup instructions */}
          <div className="space-y-2">
            <h4 className="text-sm font-semibold text-theme-text-primary">Connect your AI tool</h4>

            {[
              { icon: Terminal, name: 'Claude Code', path: '', config: configs.claudeCode },
              { icon: MessageSquare, name: 'Claude Desktop', path: '~/Library/Application Support/Claude/claude_desktop_config.json', config: configs.claudeDesktop },
              { icon: Code2, name: 'Cursor', path: '~/.cursor/mcp.json', config: configs.cursor },
              { icon: Code2, name: 'Windsurf', path: '~/.codeium/windsurf/mcp_config.json', config: configs.windsurf },
              { icon: Code2, name: 'VS Code Copilot', path: '.vscode/mcp.json', config: configs.vsCode },
              { icon: Code2, name: 'Cline', path: 'Cline MCP settings (via UI)', config: configs.cline },
              { icon: Code2, name: 'JetBrains AI', path: 'Settings → Tools → AI Assistant → MCP', config: configs.jetbrains },
              { icon: Terminal, name: 'OpenAI Codex', path: '~/.codex/config.toml', config: configs.codex },
              { icon: Terminal, name: 'Gemini CLI', path: '~/.gemini/settings.json', config: configs.gemini },
            ].map((agent) => (
              <details key={agent.name} className="group rounded-md border border-theme-border/50 bg-theme-base/30">
                <summary className="flex items-center gap-2 px-3 py-2 select-none list-none hover:bg-theme-hover/50 rounded-md transition-colors [&::-webkit-details-marker]:hidden">
                  <ChevronRight className="w-3.5 h-3.5 text-theme-text-tertiary transition-transform group-open:rotate-90" />
                  <agent.icon className="w-4 h-4 text-theme-text-tertiary" />
                  <span className="text-sm font-medium text-theme-text-primary">{agent.name}</span>
                  {agent.path && <span className="text-[10px] text-theme-text-tertiary ml-auto">{agent.path}</span>}
                </summary>
                <div className="px-3 pb-3 pt-1">
                  <CodeBlock>{agent.config}</CodeBlock>
                </div>
              </details>
            ))}
          </div>

          {/* Available tools */}
          <div className="space-y-2">
            <div className="flex items-baseline justify-between">
              <h4 className="text-sm font-semibold text-theme-text-primary">Tools</h4>
              <span className="text-[11px] text-theme-text-tertiary">{MCP_TOOL_CATALOG.length} tools</span>
            </div>
            <div className="grid grid-cols-1 gap-1.5">
              {MCP_TOOL_CATALOG.map((tool) => (
                <div key={tool.name} className="card-inner space-y-1.5">
                  <div className="flex items-center gap-2">
                    <code className="inline-code text-[11px]">{tool.name}</code>
                    {tool.write && (
                      <Tooltip content="Write tool — annotated as destructive">
                      <span className="badge-sm bg-amber-500/10 text-amber-600 dark:text-amber-400">
                        write
                      </span>
                      </Tooltip>
                    )}
                  </div>
                  <p className="text-[11px] text-theme-text-tertiary leading-relaxed">{tool.desc}</p>
                  {tool.params.length > 0 && (
                    <div className="flex flex-wrap gap-1.5 pt-0.5">
                      {tool.params.map((p) => (
                        <Tooltip key={p.arg} content={p.desc}>
                        <span className="inline-code text-[11px]">
                          <span>{p.arg}</span>
                          {p.required && <span className="text-red-400">*</span>}
                        </span>
                        </Tooltip>
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </div>
          </div>
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between px-6 py-3 border-t border-theme-border/50 shrink-0">
          <a
            href="https://github.com/skyhook-io/radar/blob/main/docs/mcp.md"
            target="_blank"
            rel="noopener noreferrer"
            className="text-xs text-theme-text-tertiary hover:text-purple-400 transition-colors"
          >
            Documentation
          </a>
          <button
            onClick={onClose}
            className="px-4 py-2 text-sm font-medium rounded-lg hover:bg-theme-elevated transition-colors text-theme-text-secondary"
          >
            Close
          </button>
        </div>
      </div>
    </div>
  )
}
