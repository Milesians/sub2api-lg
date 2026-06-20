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
  origin_peer_ip?: string
}

export interface TimedFetchOptions {
  method?: 'GET' | 'POST'
  body?: BodyInit
  contentType?: string
}

export async function timedFetch(url: string, timeoutMs: number, options: TimedFetchOptions = {}): Promise<TimedFetchResult> {
  const controller = new AbortController()
  const timer = window.setTimeout(() => controller.abort(), timeoutMs)
  const started = performance.now()
  performance.clearResourceTimings()

  try {
    const res = await fetch(url, {
      method: options.method || 'GET',
      cache: 'no-store',
      credentials: 'omit',
      headers: options.contentType ? { 'Content-Type': options.contentType } : undefined,
      body: options.body,
      signal: controller.signal,
    })
    const firstHeadersAt = performance.now()
    const body = await res.arrayBuffer()
    const ended = performance.now()
    const timing = getResourceTiming(url)
    return {
      ok: res.ok,
      status: res.status,
      duration_ms: Math.round(ended - started),
      header_ms: Math.round(firstHeadersAt - started),
      response_bytes: body.byteLength,
      error_kind: res.ok ? undefined : 'http_status',
      timing_detail_available: Boolean(timing?.detail_available),
      ttfb_ms: timing?.detail_available ? Math.round(timing.ttfb_ms) : Math.round(firstHeadersAt - started),
      origin_peer_ip: res.headers.get('X-Origin-Peer-IP') || undefined,
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
  return {
    ttfb_ms: entry.responseStart - entry.requestStart,
    detail_available: entry.responseStart > 0,
  }
}

function corsLikely(message: string): boolean {
  return message.toLowerCase().includes('cors') || message.toLowerCase().includes('failed to fetch')
}
