export interface TimedFetchResult {
  ok: boolean
  status?: number
  duration_ms: number
  header_ms?: number
  response_bytes?: number
  error_kind?: 'timeout' | 'network_error' | 'http_status'
  error_message?: string
  timing_detail_available?: boolean
  ttfb_ms?: number | null
  endpoint_ms?: number | null
  dns_ms?: number | null
  connect_ms?: number | null
  tls_ms?: number | null
  request_id?: string
}

export interface TimedFetchOptions {
  method?: 'GET' | 'POST'
  body?: BodyInit
  contentType?: string
  mode?: RequestMode
  okOnHTTPResponse?: boolean
  readBody?: boolean
}

export async function timedFetch(url: string, timeoutMs: number, options: TimedFetchOptions = {}): Promise<TimedFetchResult> {
  const controller = new AbortController()
  const timer = window.setTimeout(() => controller.abort(), timeoutMs)
  const started = performance.now()

  try {
    const res = await fetch(url, {
      method: options.method || 'GET',
      mode: options.mode,
      cache: 'no-store',
      credentials: 'omit',
      headers: options.contentType ? { 'Content-Type': options.contentType } : undefined,
      body: options.body,
      signal: controller.signal,
    })
    const firstHeadersAt = performance.now()
    const body = options.readBody === false ? undefined : await res.arrayBuffer()
    const ended = performance.now()
    const timing = getResourceTiming(url)
    const ok = options.okOnHTTPResponse || res.ok
    return {
      ok,
      status: res.status,
      duration_ms: Math.round(ended - started),
      header_ms: Math.round(firstHeadersAt - started),
      response_bytes: body?.byteLength,
      error_kind: ok ? undefined : 'http_status',
      timing_detail_available: Boolean(timing?.detail_available),
      ttfb_ms: timing?.detail_available ? Math.round(timing.ttfb_ms) : Math.round(firstHeadersAt - started),
      endpoint_ms: timing?.endpoint_ms ?? null,
      dns_ms: timing?.dns_ms ?? null,
      connect_ms: timing?.connect_ms ?? null,
      tls_ms: timing?.tls_ms ?? null,
      request_id: res.headers.get('X-Request-Id') || undefined,
    }
  } catch (e) {
    const error = e as Error
    const message = String(error?.message || error)
    return {
      ok: false,
      duration_ms: Math.round(performance.now() - started),
      error_kind: error?.name === 'AbortError' ? 'timeout' : corsLikely(message) ? 'network_error' : 'network_error',
      error_message: message,
    }
  } finally {
    window.clearTimeout(timer)
  }
}

function getResourceTiming(url: string) {
  const entries = performance.getEntriesByName(url, 'resource') as PerformanceResourceTiming[]
  const entry = entries[entries.length - 1]
  if (!entry) return null
  const dnsMS = positiveDuration(entry.domainLookupStart, entry.domainLookupEnd)
  const connectMS = positiveDuration(entry.connectStart, entry.connectEnd)
  const tlsMS = entry.secureConnectionStart > 0 ? positiveDuration(entry.secureConnectionStart, entry.connectEnd) : null
  return {
    ttfb_ms: entry.responseStart - entry.requestStart,
    endpoint_ms: connectMS,
    dns_ms: dnsMS,
    connect_ms: connectMS,
    tls_ms: tlsMS,
    detail_available: entry.responseStart > 0,
  }
}

function positiveDuration(start: number, end: number): number | null {
  const value = end - start
  return value > 0 ? Math.round(value) : null
}

function corsLikely(message: string): boolean {
  return message.toLowerCase().includes('cors') || message.toLowerCase().includes('failed to fetch')
}
