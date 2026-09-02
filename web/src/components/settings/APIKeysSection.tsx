import { useState } from 'react'
import { AlertTriangle, Check, Copy, KeyRound, Loader2, Plus, Trash2 } from 'lucide-react'
import { ConfirmDialog, EmptyState, Tooltip } from '@skyhook-io/k8s-ui'
import { useAPIKeys, useCreateAPIKey, useRevokeAPIKey, type APIKey, type CreatedAPIKey } from '../../api/apiKeys'

function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <Tooltip content={label} position="left">
      <button
        type="button"
        onClick={() => {
          navigator.clipboard.writeText(text)
          setCopied(true)
          setTimeout(() => setCopied(false), 2000)
        }}
        className="flex items-center gap-1.5 px-2 py-1 rounded-md bg-theme-elevated/60 hover:bg-theme-elevated text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
      >
        {copied ? <Check className="w-3.5 h-3.5 text-green-500" /> : <Copy className="w-3.5 h-3.5" />}
        <span className="text-xs">{copied ? 'Copied' : 'Copy'}</span>
      </button>
    </Tooltip>
  )
}

// The plaintext is returned once and is unrecoverable, so this panel is
// deliberately loud and dismissed only by an explicit click — never
// automatically on the next render or mutation.
function RevealedKey({ created, onDismiss }: { created: CreatedAPIKey; onDismiss: () => void }) {
  return (
    <div className="rounded-md border border-amber-500/40 bg-amber-500/5 p-3">
      <div className="flex items-start gap-2">
        <AlertTriangle className="w-4 h-4 text-amber-500 mt-0.5 shrink-0" />
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-theme-text-primary">
            Copy this key now — it is shown only once
          </p>
          <p className="mt-0.5 text-xs text-theme-text-tertiary">
            Radar stores only a hash. If you lose it, revoke the key and create another.
          </p>

          <div className="mt-2 flex items-center gap-2">
            <code className="inline-code flex-1 text-xs break-all py-1.5">{created.key}</code>
            <CopyButton text={created.key} label="Copy key" />
          </div>

          <p className="mt-3 text-xs text-theme-text-tertiary">Use it as a header:</p>
          <div className="mt-1 flex items-center gap-2">
            <code className="inline-code flex-1 text-xs break-all py-1.5">
              Authorization: Bearer {created.key}
            </code>
            <CopyButton text={`Authorization: Bearer ${created.key}`} label="Copy header" />
          </div>

          <button
            type="button"
            onClick={onDismiss}
            className="mt-3 px-3 py-1 text-xs font-medium rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary transition-colors"
          >
            I've copied it
          </button>
        </div>
      </div>
    </div>
  )
}

function formatCreated(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '—'
  return date.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

// APIKeysContent renders the per-user key manager inline (no modal chrome) so
// the Settings dialog can host it directly. `active` gates the list query so
// opening Settings on another section doesn't fetch credentials metadata.
export function APIKeysContent({ active }: { active: boolean }) {
  const [description, setDescription] = useState('')
  const [created, setCreated] = useState<CreatedAPIKey | null>(null)
  const [pendingRevoke, setPendingRevoke] = useState<APIKey | null>(null)

  const { data: keys, isLoading, error } = useAPIKeys(active)
  const createKey = useCreateAPIKey()
  const revokeKey = useRevokeAPIKey()

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    if (createKey.isPending) return
    createKey.mutate(description.trim(), {
      onSuccess: (key) => {
        setCreated(key)
        setDescription('')
      },
    })
  }

  return (
    <div className="space-y-4">
      <form onSubmit={submit} className="flex items-end gap-2">
        <div className="flex-1 min-w-0">
          <label htmlFor="apikey-description" className="block text-xs text-theme-text-tertiary mb-1">
            Description
          </label>
          <input
            id="apikey-description"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="What will use this key? e.g. Claude Code, CI pipeline"
            className="w-full px-2 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-blue-500"
          />
        </div>
        <button
          type="submit"
          disabled={createKey.isPending}
          className="flex items-center gap-1.5 px-4 py-1.5 text-sm font-medium btn-brand rounded-md disabled:opacity-60"
        >
          {createKey.isPending ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Plus className="w-3.5 h-3.5" />}
          Create key
        </button>
      </form>

      {created && <RevealedKey created={created} onDismiss={() => setCreated(null)} />}

      {error && (
        <div className="rounded-md border border-theme-border bg-theme-elevated/40 p-3 text-sm text-theme-text-secondary">
          Could not load your API keys: {error instanceof Error ? error.message : String(error)}
        </div>
      )}

      {isLoading && (
        <div className="flex items-center gap-2 text-sm text-theme-text-tertiary">
          <Loader2 className="w-3.5 h-3.5 animate-spin" /> Loading keys…
        </div>
      )}

      {!isLoading && !error && keys?.length === 0 && (
        <EmptyState
          tone="neutral"
          variant="card"
          icon={KeyRound}
          headline="No API keys yet"
          body="Create one for a client that can't sign in through the browser — an MCP client, a CI job, or a script."
        />
      )}

      {!!keys?.length && (
        <div className="rounded-md border border-theme-border divide-y divide-theme-border-subtle overflow-hidden">
          {keys.map((key) => (
            <div key={key.id} className="flex items-center gap-3 px-3 py-2 bg-theme-elevated/30">
              <KeyRound className="w-4 h-4 text-theme-text-tertiary shrink-0" />
              <div className="min-w-0 flex-1">
                <div className="text-sm text-theme-text-primary truncate">
                  {key.description || <span className="text-theme-text-tertiary">(no description)</span>}
                </div>
                <div className="text-xs text-theme-text-tertiary">
                  <code className="inline-code">{key.id}</code>
                  <span className="mx-1.5">·</span>
                  Created {formatCreated(key.createdAt)}
                </div>
              </div>
              <Tooltip content="Revoke this key" position="left">
                <button
                  type="button"
                  onClick={() => setPendingRevoke(key)}
                  className="p-1.5 rounded-md text-theme-text-tertiary hover:text-red-400 hover:bg-theme-hover transition-colors"
                  aria-label={`Revoke ${key.description || key.id}`}
                >
                  <Trash2 className="w-3.5 h-3.5" />
                </button>
              </Tooltip>
            </div>
          ))}
        </div>
      )}

      <p className="text-xs text-theme-text-tertiary">
        A key acts as you: it sees exactly what your account sees, because Radar impersonates your
        identity and groups. Keys do not expire — revoke one as soon as it is no longer needed.
      </p>

      <ConfirmDialog
        open={!!pendingRevoke}
        onClose={() => setPendingRevoke(null)}
        onConfirm={() => {
          if (!pendingRevoke) return
          revokeKey.mutate(pendingRevoke.id, { onSettled: () => setPendingRevoke(null) })
        }}
        title="Revoke API key"
        message={`Revoke "${pendingRevoke?.description || pendingRevoke?.id}"?`}
        details="Anything using this key starts failing on its next request. This cannot be undone — you would have to create a new key."
        confirmLabel="Revoke"
        variant="danger"
        isLoading={revokeKey.isPending}
      />
    </div>
  )
}
