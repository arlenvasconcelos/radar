import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch, fetchJSON } from './client'
import { getApiBase } from './config'

export interface APIKey {
  id: string
  description: string
  username: string
  groups: string[]
  createdAt: string
}

// CreatedAPIKey carries the plaintext, which the server returns exactly once at
// creation and can never produce again. It is held in component state only —
// never cached in React Query, localStorage, or anywhere it could outlive the
// dialog that shows it.
export interface CreatedAPIKey extends APIKey {
  key: string
}

async function readError(resp: Response): Promise<never> {
  const body = await resp.json().catch(() => ({ error: 'Unknown error' }))
  throw new Error(body.error || `HTTP ${resp.status}`)
}

export function useAPIKeys(enabled: boolean) {
  return useQuery<APIKey[]>({
    queryKey: ['api-keys'],
    queryFn: async () => {
      const resp = await fetchJSON<{ keys: APIKey[] | null }>('/auth/api-keys')
      return resp.keys ?? []
    },
    enabled,
    staleTime: 30000,
  })
}

// No successMessage: the plaintext key is revealed in the panel itself, and a
// toast would compete for attention at the one moment the user has to copy it.
export function useCreateAPIKey() {
  const queryClient = useQueryClient()
  return useMutation<CreatedAPIKey, Error, string>({
    mutationFn: async (description: string) => {
      const resp = await apiFetch(`${getApiBase()}/auth/api-keys`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ description }),
      })
      if (!resp.ok) return readError(resp)
      return resp.json()
    },
    meta: { errorMessage: 'Failed to create API key' },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['api-keys'] })
    },
  })
}

export function useRevokeAPIKey() {
  const queryClient = useQueryClient()
  return useMutation<void, Error, string>({
    mutationFn: async (id: string) => {
      const resp = await apiFetch(`${getApiBase()}/auth/api-keys/${encodeURIComponent(id)}`, {
        method: 'DELETE',
      })
      if (!resp.ok) return readError(resp)
    },
    meta: {
      errorMessage: 'Failed to revoke API key',
      successMessage: 'API key revoked',
      successDetail: 'Clients using it will fail on their next request.',
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['api-keys'] })
    },
  })
}
