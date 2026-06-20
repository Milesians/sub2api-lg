export interface StreamResult {
  ok: boolean
  first_event_ms: number | null
  total_ms: number
  event_count: number
  max_event_gap_ms: number | null
  avg_event_gap_ms: number | null
  done_seen: boolean
  stream_interrupted: boolean
  stream_buffered: boolean
  error_kind?: 'timeout' | 'network_error'
  error_message?: string
}

export async function testDiagStream(url: string, timeoutMs: number): Promise<StreamResult> {
  const controller = new AbortController()
  const timer = window.setTimeout(() => controller.abort(), timeoutMs)
  const started = performance.now()
  const events: Array<{ name: string; at: number }> = []
  let buffer = ''
  let doneSeen = false

  try {
    const res = await fetch(url, {
      method: 'GET',
      cache: 'no-store',
      credentials: 'omit',
      signal: controller.signal,
    })
    if (!res.body) throw new Error('response body is empty')
    const reader = res.body.getReader()
    const decoder = new TextDecoder()

    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      const now = performance.now()
      buffer += decoder.decode(value, { stream: true })
      const chunks = buffer.split('\n\n')
      buffer = chunks.pop() || ''
      for (const chunk of chunks) {
        const eventName = parseSSEEventName(chunk) || 'message'
        events.push({ name: eventName, at: now })
        if (eventName === 'done') doneSeen = true
      }
    }

    const ended = performance.now()
    const gaps = events.slice(1).map((event, index) => event.at - events[index].at)
    return {
      ok: doneSeen,
      first_event_ms: events[0] ? Math.round(events[0].at - started) : null,
      total_ms: Math.round(ended - started),
      event_count: events.length,
      max_event_gap_ms: gaps.length ? Math.round(Math.max(...gaps)) : null,
      avg_event_gap_ms: gaps.length ? Math.round(gaps.reduce((sum, item) => sum + item, 0) / gaps.length) : null,
      done_seen: doneSeen,
      stream_interrupted: !doneSeen,
      stream_buffered: detectBuffered(events, started, ended),
    }
  } catch (e) {
    const error = e as Error
    return {
      ok: false,
      first_event_ms: null,
      total_ms: Math.round(performance.now() - started),
      event_count: events.length,
      max_event_gap_ms: null,
      avg_event_gap_ms: null,
      done_seen: false,
      stream_interrupted: true,
      stream_buffered: false,
      error_kind: error?.name === 'AbortError' ? 'timeout' : 'network_error',
      error_message: String(error?.message || error),
    }
  } finally {
    window.clearTimeout(timer)
  }
}

function parseSSEEventName(chunk: string): string | null {
  const line = chunk.split('\n').find((item) => item.startsWith('event:'))
  return line ? line.slice('event:'.length).trim() : null
}

function detectBuffered(events: Array<{ at: number }>, started: number, ended: number): boolean {
  if (events.length < 5) return false
  const firstEventMs = events[0].at - started
  const totalMs = ended - started
  const spanMs = events[events.length - 1].at - events[0].at
  return firstEventMs > totalMs * 0.75 && spanMs < totalMs * 0.2
}
