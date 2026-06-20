import type { ASNInfo, BrowserSummary, ClientTraceInfo, EntryPoint, TraceIPInfo } from '../types'

interface DNSAnswer {
  data?: string
  type?: number
}

interface DNSResponse {
  Answer?: DNSAnswer[]
}

const dohEndpoints = [
  'https://cloudflare-dns.com/dns-query',
  'https://dns.google/resolve',
]

export async function traceEndpointFromBrowser(endpoint: EntryPoint, browser: BrowserSummary): Promise<ClientTraceInfo> {
  const host = endpointHost(endpoint)
  const trace: ClientTraceInfo = {
    source: 'browser',
    host,
    checked_at: new Date().toISOString(),
    avg_ping_ms: browser.avg_endpoint_ping_ms,
    ping_success_rate: browser.endpoint_ping_success_rate ?? null,
  }
  if (!host) {
    trace.error = 'empty_host'
    return trace
  }
  try {
    const ips = await resolveHost(host)
    trace.ips = await Promise.all(ips.slice(0, 6).map(async (ip) => ({
      ip,
      asn: await lookupASN(ip),
    })))
    if (trace.ips.length === 0) trace.error = 'dns_no_answer'
  } catch (e) {
    trace.error = String((e as Error)?.message || e)
  }
  return trace
}

export async function traceIPFromBrowser(ip: string): Promise<TraceIPInfo> {
  return {
    ip,
    asn: await lookupASN(ip),
  }
}

async function resolveHost(host: string): Promise<string[]> {
  const answers = await Promise.allSettled([
    resolveDNS(host, 'A').then((items) => items.map((item) => item.data?.trim() || '').filter(isIPv4)),
    resolveDNS(host, 'AAAA').then((items) => items.map((item) => item.data?.trim() || '').filter(isIPv6)),
  ])
  const out: string[] = []
  for (const answer of answers) {
    if (answer.status !== 'fulfilled') continue
    for (const value of answer.value) if (value && !out.includes(value)) out.push(value)
  }
  return out
}

async function lookupASN(ip: string): Promise<ASNInfo | null> {
  const query = cymruOriginQuery(ip)
  if (!query) return null
  const answers = await resolveDNS(query, 'TXT')
  const data = cleanTXT(answers.find((item) => item.type === 16)?.data || '')
  const parts = data.split('|').map((item) => item.trim())
  if (!parts[0] || parts[0].toLowerCase() === 'na') return null
  const asn = parts[0].split(/\s+/)[0]
  const info: ASNInfo = { asn }
  if (parts[1]) info.prefix = parts[1]
  if (parts[2]) info.cc = parts[2]
  if (parts[3]) info.registry = parts[3]
  if (parts[4]) info.allocated = parts[4]
  info.name = await lookupASName(asn)
  return info
}

async function lookupASName(asn: string): Promise<string> {
  if (!asn) return ''
  try {
    const answers = await resolveDNS(`AS${asn}.asn.cymru.com`, 'TXT')
    const parts = cleanTXT(answers.find((item) => item.type === 16)?.data || '').split('|').map((item) => item.trim())
    return parts[4] || ''
  } catch {
    return ''
  }
}

async function resolveDNS(name: string, type: 'A' | 'AAAA' | 'TXT'): Promise<DNSAnswer[]> {
  let lastError: unknown
  for (const endpoint of dohEndpoints) {
    try {
      const url = new URL(endpoint)
      url.searchParams.set('name', name)
      url.searchParams.set('type', type)
      const res = await fetch(url.toString(), {
        cache: 'no-store',
        headers: { Accept: 'application/dns-json' },
      })
      if (!res.ok) throw new Error(`doh ${res.status}`)
      const body = await res.json() as DNSResponse
      return body.Answer || []
    } catch (e) {
      lastError = e
    }
  }
  throw lastError instanceof Error ? lastError : new Error('doh_failed')
}

function endpointHost(endpoint: EntryPoint): string {
  try {
    return new URL(endpoint.base_url).hostname
  } catch {
    return endpoint.host.split(':')[0] || endpoint.host
  }
}

function cymruOriginQuery(ip: string): string {
  if (isIPv4(ip)) {
    return `${ip.split('.').reverse().join('.')}.origin.asn.cymru.com`
  }
  const expanded = expandIPv6(ip)
  if (!expanded) return ''
  return `${expanded.split('').reverse().join('.')}.origin6.asn.cymru.com`
}

function isIPv4(value: string): boolean {
  const parts = value.split('.')
  return parts.length === 4 && parts.every((part) => {
    if (!/^\d{1,3}$/.test(part)) return false
    const parsed = Number(part)
    return parsed >= 0 && parsed <= 255
  })
}

function isIPv6(value: string): boolean {
  return Boolean(expandIPv6(value))
}

function expandIPv6(ip: string): string {
  const value = ip.trim().toLowerCase()
  if (!value.includes(':')) return ''
  const parts = value.split('::')
  if (parts.length > 2) return ''
  const left = parts[0] ? parts[0].split(':') : []
  const right = parts.length === 2 && parts[1] ? parts[1].split(':') : []
  const fill = 8 - left.length - right.length
  if (fill < 0) return ''
  const groups = [...left, ...Array(fill).fill('0'), ...right]
  if (groups.length !== 8 || groups.some((group) => !/^[0-9a-f]{0,4}$/.test(group))) return ''
  return groups.map((group) => group.padStart(4, '0')).join('')
}

function cleanTXT(value: string): string {
  return value.replace(/^"/, '').replace(/"$/, '').replace(/"\s+"/g, '')
}

export function uniqueASNLabels(ips: TraceIPInfo[] | undefined): string {
  if (!ips?.length) return '-'
  const labels = ips
    .map((item) => asnLabel(item.asn))
    .filter((item) => item !== '-')
  return Array.from(new Set(labels)).join(' / ') || '-'
}

export function asnLabel(asn?: ASNInfo | null): string {
  return asn?.asn ? `AS${asn.asn}` : '-'
}
