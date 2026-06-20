import type { BootstrapResponse } from '../types'
import { apiURL } from '../utils/url'

const sub2apiCredentialKey = 'sub2api_lg_sub2api_credential'

export function iframeContext(): Record<string, string> {
  const params = new URLSearchParams(window.location.search)
  return {
    user_id: params.get('user_id') || '',
    ticket: params.get('ticket') || '',
    token: params.get('token') || '',
    legacy_token: params.get('legacy_token') || params.get('token') || '',
    theme: params.get('theme') || '',
    lang: params.get('lang') || '',
    ui_mode: params.get('ui_mode') || (window.location.pathname.includes('/admin') ? 'admin' : 'customer'),
    src_host: params.get('src_host') || '',
    src_url: params.get('src_url') || '',
  }
}

export async function bootstrap(): Promise<BootstrapResponse> {
  const context = iframeContext()
  rememberSub2APICredential(context)
  const res = await fetch(apiURL('/customer/bootstrap'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    cache: 'no-store',
    body: JSON.stringify(context),
  })
  if (!res.ok) throw new Error(`bootstrap failed: ${res.status}`)
  const body = await res.json()
  sessionStorage.setItem('sub2api_lg_session_token', body.session_token)
  cleanTokenFromURL()
  return body
}

export async function adminBootstrap(): Promise<BootstrapResponse> {
  const context = iframeContext()
  rememberSub2APICredential(context)
  const res = await fetch(apiURL('/admin/bootstrap'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    cache: 'no-store',
    body: JSON.stringify(context),
  })
  if (!res.ok) throw new Error(`admin bootstrap failed: ${res.status}`)
  const body = await res.json()
  sessionStorage.setItem('sub2api_lg_admin_session_token', body.session_token)
  cleanTokenFromURL()
  return body
}

export async function getEntrypoints(token: string) {
  const res = await fetch(apiURL('/customer/entrypoints'), {
    headers: authHeaders(token),
    cache: 'no-store',
  })
  if (!res.ok) throw new Error(`entrypoints failed: ${res.status}`)
  return res.json()
}

export async function submitReport(token: string, payload: unknown): Promise<{ report_id: string; share_url: string; customer_summary?: any }> {
  const res = await fetch(apiURL('/customer/reports'), {
    method: 'POST',
    headers: { ...authHeaders(token), 'Content-Type': 'application/json' },
    cache: 'no-store',
    body: JSON.stringify(payload),
  })
  if (!res.ok) throw new Error(`report submit failed: ${res.status}`)
  return res.json()
}

export async function getReport(reportId: string) {
  const current = new URL(window.location.href)
  const shareToken = current.searchParams.get('share_token')
  const path = `/customer/reports/${encodeURIComponent(reportId)}${shareToken ? `?share_token=${encodeURIComponent(shareToken)}` : ''}`
  const res = await fetch(apiURL(path), {
    cache: 'no-store',
  })
  if (!res.ok) throw new Error(`report fetch failed: ${res.status}`)
  return res.json()
}

export async function listAdminReports(token: string, params: Record<string, string> = {}) {
  const query = new URLSearchParams(params)
  const suffix = query.toString() ? `?${query.toString()}` : ''
  const res = await fetch(apiURL(`/admin/reports${suffix}`), {
    headers: authHeaders(token),
    cache: 'no-store',
  })
  if (!res.ok) throw new Error(`admin reports failed: ${res.status}`)
  return res.json()
}

export async function getAdminReport(token: string, reportId: string) {
  const res = await fetch(apiURL(`/admin/reports/${encodeURIComponent(reportId)}`), {
    headers: authHeaders(token),
    cache: 'no-store',
  })
  if (!res.ok) throw new Error(`admin report failed: ${res.status}`)
  return res.json()
}

export async function getEntrypointInventory(token: string) {
  const res = await fetch(apiURL('/admin/entrypoints/inventory'), {
    headers: authHeaders(token),
    cache: 'no-store',
  })
  if (!res.ok) throw new Error(`entrypoint inventory failed: ${res.status}`)
  return res.json()
}

export async function resolveCustomNetinfo(token: string, customEndpoints: Array<{ endpoint_public_id: string; display_name: string; probe_base_url: string }>) {
  const res = await fetch(apiURL('/customer/netinfo/resolve'), {
    method: 'POST',
    headers: { ...authHeaders(token), 'Content-Type': 'application/json' },
    cache: 'no-store',
    body: JSON.stringify({ custom_endpoints: customEndpoints }),
  })
  if (!res.ok) throw new Error(`netinfo resolve failed: ${res.status}`)
  return res.json()
}

function authHeaders(token: string): Record<string, string> {
  const headers: Record<string, string> = { Authorization: `Bearer ${token}` }
  const credential = sub2apiCredential()
  if (credential) headers['X-Sub2API-Credential'] = credential
  return headers
}

function rememberSub2APICredential(context: Record<string, string>) {
  const credential = context.token || context.legacy_token || context.ticket
  if (credential) sessionStorage.setItem(sub2apiCredentialKey, credential)
}

function sub2apiCredential(): string {
  const params = new URLSearchParams(window.location.search)
  return params.get('token') || params.get('legacy_token') || params.get('ticket') || sessionStorage.getItem(sub2apiCredentialKey) || ''
}

function cleanTokenFromURL() {
  const url = new URL(window.location.href)
  const keys = ['token', 'ticket', 'legacy_token']
  if (!keys.some((key) => url.searchParams.has(key))) return
  for (const key of keys) url.searchParams.delete(key)
  window.history.replaceState(null, '', `${url.pathname}${url.search}${url.hash}`)
}
