import type { BrowserSummary, EndpointResult, EntryPoint, ProbeConfig } from '../types'
import { joinURL, withNonce } from '../utils/url'
import { percentile, ratio } from './stats'
import { timedFetch, type TimedFetchResult } from './timed-fetch'
import { testDiagStream } from './stream-test'

export type TestKind = 'ping' | 'download' | 'upload' | 'stream'

const minPingSamples = 20

export interface DiagnoseProgressEvent {
  endpoint_id: string
  kind: TestKind
  label: string
  size?: string
  ok: boolean
  duration_ms?: number | null
  ttfb_ms?: number | null
  ttft_ms?: number | null
  mbps?: number | null
  origin_peer_ip?: string
  error_message?: string
  sample_index: number
  sample_total: number
}

interface SizedFetchResult {
  size: string
  bytes: number
  result: TimedFetchResult
}

export async function diagnoseEndpoint(
  endpoint: EntryPoint,
  probe: ProbeConfig,
  onProgress?: (event: DiagnoseProgressEvent) => void,
): Promise<EndpointResult> {
  const pingResults: TimedFetchResult[] = []
  const downloadResults: SizedFetchResult[] = []
  const uploadResults: SizedFetchResult[] = []
  const sizes = normalizedSizes(probe.blob_sizes)
  const pingSamples = Math.max(probe.browser_repeat, minPingSamples)
  const totalSteps = pingSamples + sizes.length * 2 + 1
  let step = 0

  for (let i = 0; i < pingSamples; i += 1) {
    const result = await timedFetch(withNonce(joinURL(endpoint.lg_base_url, probe.paths.ping)), probe.browser_timeout_ms)
    pingResults.push(result)
    step += 1
    onProgress?.({
      endpoint_id: endpoint.id,
      kind: 'ping',
      label: `Ping ${i + 1}/${pingSamples}`,
      ok: result.ok,
      duration_ms: result.duration_ms,
      ttfb_ms: result.ttfb_ms ?? null,
      origin_peer_ip: result.origin_peer_ip,
      error_message: result.error_message,
      sample_index: step,
      sample_total: totalSteps,
    })
  }

  for (const size of sizes) {
    const url = new URL(joinURL(endpoint.lg_base_url, probe.paths.blob))
    url.searchParams.set('size', size)
    url.searchParams.set('nonce', crypto.randomUUID())
    const result = await timedFetch(url.toString(), probe.browser_timeout_ms)
    const bytes = result.response_bytes || sizeToBytes(size)
    downloadResults.push({ size, bytes, result })
    step += 1
    onProgress?.({
      endpoint_id: endpoint.id,
      kind: 'download',
      label: `下载 ${size}`,
      size,
      ok: result.ok,
      duration_ms: result.duration_ms,
      ttfb_ms: result.ttfb_ms ?? null,
      mbps: mbps(bytes, result.duration_ms, result.ok),
      error_message: result.error_message,
      sample_index: step,
      sample_total: totalSteps,
    })
  }

  for (const size of sizes) {
    const bytes = sizeToBytes(size)
    const url = new URL(joinURL(endpoint.lg_base_url, probe.paths.upload || '/diag/upload'))
    url.searchParams.set('size', size)
    url.searchParams.set('nonce', crypto.randomUUID())
    const result = await timedFetch(url.toString(), probe.browser_timeout_ms, {
      method: 'POST',
      body: payload(bytes),
      contentType: 'application/octet-stream',
    })
    uploadResults.push({ size, bytes, result })
    step += 1
    onProgress?.({
      endpoint_id: endpoint.id,
      kind: 'upload',
      label: `上传 ${size}`,
      size,
      ok: result.ok,
      duration_ms: result.duration_ms,
      ttfb_ms: result.ttfb_ms ?? null,
      mbps: mbps(bytes, result.duration_ms, result.ok),
      error_message: result.error_message,
      sample_index: step,
      sample_total: totalSteps,
    })
  }

  const streamURL = new URL(joinURL(endpoint.lg_base_url, probe.paths.stream))
  streamURL.searchParams.set('events', String(probe.stream.events))
  streamURL.searchParams.set('interval_ms', String(probe.stream.interval_ms))
  streamURL.searchParams.set('bytes', String(probe.stream.bytes))
  streamURL.searchParams.set('nonce', crypto.randomUUID())
  const stream = await testDiagStream(streamURL.toString(), probe.browser_timeout_ms + probe.stream.events * probe.stream.interval_ms + 1000)
  step += 1
  onProgress?.({
    endpoint_id: endpoint.id,
    kind: 'stream',
    label: '流式 TTFT',
    ok: stream.ok,
    duration_ms: stream.total_ms,
    ttft_ms: stream.first_event_ms,
    error_message: stream.error_message,
    sample_index: step,
    sample_total: totalSteps,
  })

  const fetchResults = [
    ...pingResults,
    ...downloadResults.map((item) => item.result),
    ...uploadResults.map((item) => item.result),
  ]
  const totalCount = fetchResults.length + 1
  const successCount = fetchResults.filter((item) => item.ok).length + (stream.ok ? 1 : 0)
  const pingSuccessCount = pingResults.filter((item) => item.ok).length
  const durations = pingResults.filter((item) => item.ok).map((item) => item.duration_ms)
  const ttfbValues = pingResults.filter((item) => item.ok && item.ttfb_ms != null).map((item) => item.ttfb_ms as number)
  const p50Duration = percentile(durations, 50)
  const p95Duration = percentile(durations, 95)
  const p50TTFB = percentile(ttfbValues, 50)
  const p95TTFB = percentile(ttfbValues, 95)
  const downloadMbps = averageMbps(downloadResults)
  const uploadMbps = averageMbps(uploadResults)
  const small = sizes[0]
  const large = sizes[sizes.length - 1]
  const downloadBySize = speedBySize(downloadResults)
  const uploadBySize = speedBySize(uploadResults)
  const summary: BrowserSummary = {
    success_rate: ratio(successCount, totalCount),
    ping_success_rate: ratio(pingSuccessCount, pingResults.length),
    http_loss_rate: ratio(totalCount - successCount, totalCount),
    p50_duration_ms: p50Duration,
    p95_duration_ms: p95Duration,
    p50_ttfb_ms: p50TTFB,
    p95_ttfb_ms: p95TTFB,
    avg_ping_ms: average(durations),
    avg_ttfb_ms: average(ttfbValues),
    avg_ttft_ms: stream.first_event_ms,
    jitter_ms: p50Duration != null && p95Duration != null ? p95Duration - p50Duration : null,
    timeout_rate: ratio(fetchResults.filter((item) => item.error_kind === 'timeout').length + (stream.error_kind === 'timeout' ? 1 : 0), totalCount),
    download_mbps: downloadMbps,
    upload_mbps: uploadMbps,
    download_mbps_by_size: downloadBySize,
    upload_mbps_by_size: uploadBySize,
    download_small_mbps: speedForSize(downloadResults, small),
    download_large_mbps: speedForSize(downloadResults, large),
    upload_small_mbps: speedForSize(uploadResults, small),
    upload_large_mbps: speedForSize(uploadResults, large),
    first_event_ms: stream.first_event_ms,
    max_chunk_gap_ms: stream.max_event_gap_ms,
    stream_buffered: stream.stream_buffered,
    cors_blocked: fetchResults.some((item) => item.error_message?.toLowerCase().includes('cors') || item.error_message?.toLowerCase().includes('failed to fetch')) ||
      Boolean(stream.error_message?.toLowerCase().includes('cors') || stream.error_message?.toLowerCase().includes('failed to fetch')),
    timing_detail_available: fetchResults.some((item) => item.timing_detail_available),
  }
  const level = scoreLevel(summary)
  return {
    endpoint_id: endpoint.id,
    name: endpoint.name,
    base_url: endpoint.base_url,
    lg_base_url: endpoint.lg_base_url,
    browser: summary,
    level,
    recommendation: recommendation(level, summary),
  }
}

export function buildReport(results: EndpointResult[], iframeContext: Record<string, string>) {
  const best = [...results].sort(compareResults)[0]
  return {
    iframe_context: {
      theme: iframeContext.theme,
      lang: iframeContext.lang,
      ui_mode: iframeContext.ui_mode,
      src_host: iframeContext.src_host,
      src_url: iframeContext.src_url,
    },
    summary: {
      entrypoint_count: results.length,
      best_endpoint_id: best?.endpoint_id || null,
      best_endpoint_name: best?.name || null,
      score: best ? scoreNumber(best.browser) : 0,
      level: best?.level || 'bad',
      main_problem: best ? mainProblem(best.browser) : '没有可用入口',
      recommendation: best ? best.recommendation : '请检查入口配置和诊断路径转发。',
    },
    entrypoints: results,
  }
}

function averageMbps(items: SizedFetchResult[]): number | null {
  const ok = items.filter((item) => item.result.ok && item.bytes > 0 && item.result.duration_ms > 0)
  if (ok.length === 0) return null
  return round(ok.reduce((sum, item) => sum + (mbps(item.bytes, item.result.duration_ms, true) || 0), 0) / ok.length)
}

function speedForSize(items: SizedFetchResult[], size: string): number | null {
  const item = items.find((candidate) => candidate.size === size)
  if (!item) return null
  return mbps(item.bytes, item.result.duration_ms, item.result.ok)
}

function speedBySize(items: SizedFetchResult[]): Record<string, number | null> {
  const out: Record<string, number | null> = {}
  for (const item of items) {
    out[item.size] = mbps(item.bytes, item.result.duration_ms, item.result.ok)
  }
  return out
}

function mbps(bytes: number, durationMs: number, ok: boolean): number | null {
  if (!ok || bytes <= 0 || durationMs <= 0) return null
  return round((bytes * 8) / (durationMs / 1000) / 1_000_000)
}

function average(values: number[]): number | null {
  if (values.length === 0) return null
  return Math.round(values.reduce((sum, item) => sum + item, 0) / values.length)
}

function round(value: number): number {
  return Number(value.toFixed(2))
}

function normalizedSizes(sizes: string[]): string[] {
  const out = Array.from(new Set(sizes.map((item) => item.trim().toLowerCase()).filter(Boolean)))
  return out.length > 0 ? out : ['64k', '1m', '5m', '20m']
}

function sizeToBytes(size: string): number {
  const match = size.trim().toLowerCase().match(/^(\d+)(k|m)?$/)
  if (!match) return 0
  const value = Number(match[1])
  if (match[2] === 'm') return value * 1024 * 1024
  if (match[2] === 'k') return value * 1024
  return value
}

function payload(bytes: number): Blob {
  const body = new Uint8Array(bytes)
  for (let i = 0; i < body.length; i += 1) {
    body[i] = i % 251
  }
  return new Blob([body], { type: 'application/octet-stream' })
}

function scoreLevel(summary: BrowserSummary): 'good' | 'warning' | 'bad' {
  if (summary.cors_blocked) return 'bad'
  if (summary.stream_buffered) return 'warning'
  if (summary.success_rate >= 0.98 && (summary.p95_duration_ms ?? Infinity) < 800) return 'good'
  if (summary.success_rate >= 0.95 && (summary.p95_duration_ms ?? Infinity) < 1500) return 'warning'
  return 'bad'
}

function scoreNumber(summary: BrowserSummary): number {
  let score = Math.round(summary.success_rate * 100)
  if ((summary.p95_duration_ms ?? 0) > 800) score -= 10
  if ((summary.p95_duration_ms ?? 0) > 1500) score -= 20
  if (summary.stream_buffered) score -= 10
  if (summary.cors_blocked) score -= 30
  return Math.max(0, Math.min(100, score))
}

function recommendation(level: 'good' | 'warning' | 'bad', summary: BrowserSummary): string {
  if (summary.cors_blocked) return '浏览器无法跨入口读取诊断接口，请检查 /diag/* 的 CORS 和 Timing-Allow-Origin。'
  if (summary.stream_buffered) return '诊断流事件疑似被缓冲，请检查反向代理或 CDN 的流式响应配置。'
  if (level === 'good') return '当前入口表现稳定，可以继续使用。'
  if (level === 'warning') return '当前入口可用但存在波动，建议和其他入口对比后选择。'
  return '该入口 HTTP 失败率或 p95 耗时偏高，建议检查 CDN、WAF、反向代理或源站链路。'
}

function mainProblem(summary: BrowserSummary): string | null {
  if (summary.cors_blocked) return 'CORS 配置异常'
  if (summary.stream_buffered) return '流式响应疑似缓冲'
  if (summary.http_loss_rate > 0.05) return 'HTTP 失败率偏高'
  if ((summary.p95_duration_ms ?? 0) >= 1500) return '入口整体偏慢'
  return null
}

function compareResults(a: EndpointResult, b: EndpointResult): number {
  return b.browser.success_rate - a.browser.success_rate ||
    a.browser.http_loss_rate - b.browser.http_loss_rate ||
    (a.browser.p95_ttfb_ms ?? Infinity) - (b.browser.p95_ttfb_ms ?? Infinity) ||
    (a.browser.p95_duration_ms ?? Infinity) - (b.browser.p95_duration_ms ?? Infinity) ||
    Number(a.browser.stream_buffered) - Number(b.browser.stream_buffered) ||
    (a.browser.max_chunk_gap_ms ?? Infinity) - (b.browser.max_chunk_gap_ms ?? Infinity)
}
