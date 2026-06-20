import type { BootstrapResponse } from '../types'
import { apiURL } from '../utils/url'

export function iframeContext(): Record<string, string> {
  const params = new URLSearchParams(window.location.search)
  return {
    user_id: params.get('user_id') || '',
    token: params.get('token') || '',
    theme: params.get('theme') || '',
    lang: params.get('lang') || '',
    ui_mode: params.get('ui_mode') || 'embedded',
    src_host: params.get('src_host') || '',
    src_url: params.get('src_url') || '',
  }
}

export async function bootstrap(): Promise<BootstrapResponse> {
  const res = await fetch(apiURL('/bootstrap'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    cache: 'no-store',
    body: JSON.stringify(iframeContext()),
  })
  if (!res.ok) throw new Error(`bootstrap failed: ${res.status}`)
  const body = await res.json()
  sessionStorage.setItem('sub2api_lg_session_token', body.session_token)
  cleanTokenFromURL()
  return body
}

export async function getEntrypoints(token: string, refresh = false) {
  const res = await fetch(`${apiURL('/entrypoints')}?refresh=${refresh ? '1' : '0'}`, {
    headers: { Authorization: `Bearer ${token}` },
    cache: 'no-store',
  })
  if (!res.ok) throw new Error(`entrypoints failed: ${res.status}`)
  return res.json()
}

export async function submitReport(token: string, payload: unknown): Promise<{ report_id: string; share_url: string }> {
  const res = await fetch(apiURL('/reports'), {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    cache: 'no-store',
    body: JSON.stringify(payload),
  })
  if (!res.ok) throw new Error(`report submit failed: ${res.status}`)
  return res.json()
}

export async function getReport(reportId: string) {
  const res = await fetch(apiURL(`/reports/${encodeURIComponent(reportId)}`), {
    cache: 'no-store',
  })
  if (!res.ok) throw new Error(`report fetch failed: ${res.status}`)
  return res.json()
}

function cleanTokenFromURL() {
  const url = new URL(window.location.href)
  if (!url.searchParams.has('token')) return
  url.searchParams.delete('token')
  window.history.replaceState(null, '', `${url.pathname}${url.search}${url.hash}`)
}
