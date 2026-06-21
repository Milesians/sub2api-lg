import type { BrowserSummary, EndpointResult, EntryPoint, IPInfo, ProbeConfig } from '../types'
import { joinURL, withNonce } from '../utils/url'
import { percentile, ratio } from './stats'
import { timedFetch, type TimedFetchResult } from './timed-fetch'
import { testDiagStream } from './stream-test'

export type TestKind = 'origin_ping' | 'headers' | 'download' | 'upload' | 'stream'

const minPingSamples = 20

export interface DiagnoseProgressEvent {
  endpoint_id: string
  endpoint_public_id: string
  kind: TestKind
  label: string
  size?: string
  ok: boolean
  duration_ms?: number | null
  ttfb_ms?: number | null
  endpoint_ms?: number | null
  ttft_ms?: number | null
  mbps?: number | null
  request_id?: string
  error_message?: string
  error_kind?: string
  timing_detail_available?: boolean
  stream_buffered?: boolean
  origin_peer?: IPInfo
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
  runID: string,
  onProgress?: (event: DiagnoseProgressEvent) => void,
): Promise<EndpointResult> {
  const originPingResults: TimedFetchResult[] = []
  const downloadResults: SizedFetchResult[] = []
  const uploadResults: SizedFetchResult[] = []
  const sizes = normalizedSizes(probe.blob_sizes)
  const pingSamples = Math.max(probe.browser_repeat, minPingSamples)
  const endpointPublicID = endpoint.endpoint_public_id || endpoint.id
  const probeBaseURL = endpoint.probe_base_url || endpoint.lg_base_url || ''
  const totalSteps = pingSamples + sizes.length * 2 + 2
  let step = 0

  for (let i = 0; i < pingSamples; i += 1) {
    const result = await timedFetch(diagURL(probeBaseURL, probe.paths.ping, runID, endpointPublicID), probe.browser_timeout_ms)
    originPingResults.push(result)
    step += 1
    onProgress?.({
      endpoint_id: endpoint.id,
      endpoint_public_id: endpointPublicID,
      kind: 'origin_ping',
      label: `诊断 Ping ${i + 1}/${pingSamples}`,
      ok: result.ok,
      request_id: result.request_id,
      duration_ms: result.duration_ms,
      ttfb_ms: result.ttfb_ms ?? null,
      endpoint_ms: result.endpoint_ms ?? null,
      error_message: result.error_message,
      error_kind: result.error_kind,
      timing_detail_available: Boolean(result.timing_detail_available),
      sample_index: step,
      sample_total: totalSteps,
    })
  }

  const headersResult = await timedFetch(diagURL(probeBaseURL, '/diag/headers', runID, endpointPublicID), probe.browser_timeout_ms, { parseJSON: true })
  step += 1
  const originPeer = safeIPInfo(headersResult.json?.origin_peer)
  onProgress?.({
    endpoint_id: endpoint.id,
    endpoint_public_id: endpointPublicID,
    kind: 'headers',
    label: '回源信息',
    ok: headersResult.ok,
    request_id: headersResult.request_id,
    duration_ms: headersResult.duration_ms,
    ttfb_ms: headersResult.ttfb_ms ?? null,
    endpoint_ms: headersResult.endpoint_ms ?? null,
    error_message: headersResult.error_message,
    error_kind: headersResult.error_kind,
    timing_detail_available: Boolean(headersResult.timing_detail_available),
    origin_peer: originPeer,
    sample_index: step,
    sample_total: totalSteps,
  })

  for (const size of sizes) {
    const url = new URL(diagURL(probeBaseURL, probe.paths.blob, runID, endpointPublicID))
    url.searchParams.set('size', size)
    const result = await timedFetch(url.toString(), transferTimeoutMS(probe.browser_timeout_ms, size))
    const bytes = result.response_bytes || sizeToBytes(size)
    downloadResults.push({ size, bytes, result })
    step += 1
    onProgress?.({
      endpoint_id: endpoint.id,
      endpoint_public_id: endpointPublicID,
      kind: 'download',
      label: `下载 ${size}`,
      size,
      ok: result.ok,
      request_id: result.request_id,
      duration_ms: result.duration_ms,
      ttfb_ms: result.ttfb_ms ?? null,
      mbps: mbps(bytes, result.duration_ms, result.ok),
      error_message: result.error_message,
      error_kind: result.error_kind,
      timing_detail_available: Boolean(result.timing_detail_available),
      sample_index: step,
      sample_total: totalSteps,
    })
  }

  for (const size of sizes) {
    const bytes = sizeToBytes(size)
    const url = new URL(diagURL(probeBaseURL, probe.paths.upload || '/diag/upload', runID, endpointPublicID))
    url.searchParams.set('size', size)
    const result = await timedFetch(url.toString(), transferTimeoutMS(probe.browser_timeout_ms, size), {
      method: 'POST',
      body: payload(bytes),
      contentType: 'application/octet-stream',
    })
    uploadResults.push({ size, bytes, result })
    step += 1
    onProgress?.({
      endpoint_id: endpoint.id,
      endpoint_public_id: endpointPublicID,
      kind: 'upload',
      label: `上传 ${size}`,
      size,
      ok: result.ok,
      request_id: result.request_id,
      duration_ms: result.duration_ms,
      ttfb_ms: result.ttfb_ms ?? null,
      mbps: mbps(bytes, result.duration_ms, result.ok),
      error_message: result.error_message,
      error_kind: result.error_kind,
      timing_detail_available: Boolean(result.timing_detail_available),
      sample_index: step,
      sample_total: totalSteps,
    })
  }

  const streamURL = new URL(diagURL(probeBaseURL, probe.paths.stream, runID, endpointPublicID))
  streamURL.searchParams.set('events', String(probe.stream.events))
  streamURL.searchParams.set('interval_ms', String(probe.stream.interval_ms))
  streamURL.searchParams.set('bytes', String(probe.stream.bytes))
  const stream = await testDiagStream(streamURL.toString(), probe.browser_timeout_ms + probe.stream.events * probe.stream.interval_ms + 1000)
  step += 1
  onProgress?.({
    endpoint_id: endpoint.id,
    endpoint_public_id: endpointPublicID,
    kind: 'stream',
    label: '流式 TTFT',
    ok: stream.ok,
    request_id: stream.request_id,
    duration_ms: stream.total_ms,
    ttft_ms: stream.first_event_ms,
    error_message: stream.error_message,
    error_kind: stream.error_kind,
    stream_buffered: stream.stream_buffered,
    sample_index: step,
    sample_total: totalSteps,
  })

  const fetchResults = [
    ...originPingResults,
    headersResult,
    ...downloadResults.map((item) => item.result),
    ...uploadResults.map((item) => item.result),
  ]
  const totalCount = fetchResults.length + 1
  const successCount = fetchResults.filter((item) => item.ok).length + (stream.ok ? 1 : 0)
  const originPingSuccessCount = originPingResults.filter((item) => item.ok).length
  const endpointDurations = originPingResults.filter((item) => item.ok && item.endpoint_ms != null).map((item) => item.endpoint_ms as number)
  const originDurations = originPingResults.filter((item) => item.ok).map((item) => item.duration_ms)
  const ttfbValues = originPingResults.filter((item) => item.ok && item.ttfb_ms != null).map((item) => item.ttfb_ms as number)
  const p50Duration = percentile(originDurations, 50)
  const p95Duration = percentile(originDurations, 95)
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
    ping_success_rate: ratio(originPingSuccessCount, originPingResults.length),
    origin_ping_success_rate: ratio(originPingSuccessCount, originPingResults.length),
    http_loss_rate: ratio(totalCount - successCount, totalCount),
    p50_duration_ms: p50Duration,
    p95_duration_ms: p95Duration,
    p50_ttfb_ms: p50TTFB,
    p95_ttfb_ms: p95TTFB,
    avg_ping_ms: average(originDurations),
    avg_origin_ping_ms: average(originDurations),
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
    endpoint_public_id: endpointPublicID,
    name: endpoint.name,
    browser: summary,
    netinfo: {
      origin_peer: originPeer,
      dns_records: endpoint.dns_records || [],
    },
    level,
  }
}

export function buildReport(
  runID: string,
  samples: DiagnoseProgressEvent[],
  endpointLabels: Record<string, string> = {},
  customEndpoints: Array<{ endpoint_public_id: string; display_name: string; probe_base_url: string }> = [],
  endpointNetInfo: Record<string, { origin_peer?: IPInfo; dns_records?: IPInfo[] }> = {},
) {
  return {
    schema_version: '2.0',
    run_id: runID,
    client_env: clientEnv(),
    endpoint_labels: endpointLabels,
    endpoint_netinfo: endpointNetInfo,
    custom_endpoints: customEndpoints,
    samples: samples.map((sample) => ({
      endpoint_public_id: sample.endpoint_public_id,
      kind: sample.kind,
      request_id: sample.request_id,
      size: sample.size,
      ok: sample.ok,
      duration_ms: sample.duration_ms ?? null,
      ttfb_ms: sample.ttfb_ms ?? null,
      ttft_ms: sample.ttft_ms ?? null,
      endpoint_ms: sample.endpoint_ms ?? null,
      mbps: sample.mbps ?? null,
      error_kind: sample.error_kind,
      error_message: sample.error_message,
      timing_detail_available: Boolean(sample.timing_detail_available),
      stream_buffered: Boolean(sample.stream_buffered),
      origin_peer: sample.origin_peer,
    })),
  }
}

function diagURL(base: string, path: string, runID: string, endpointPublicID: string): string {
  const url = new URL(withNonce(joinURL(base, path)))
  url.searchParams.set('run_id', runID)
  url.searchParams.set('endpoint_public_id', endpointPublicID)
  return url.toString()
}

function clientEnv(): Record<string, string> {
  return {
    browser: navigator.userAgent,
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || '',
    language: navigator.language,
    viewport: `${window.innerWidth}x${window.innerHeight}`,
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

function transferTimeoutMS(baseTimeoutMS: number, size: string): number {
  if (sizeToBytes(size) >= 20 * 1024 * 1024) return Math.max(baseTimeoutMS, 30_000)
  return baseTimeoutMS
}

function safeIPInfo(value: any): IPInfo | undefined {
  if (!value || typeof value !== 'object' || typeof value.ip !== 'string' || value.ip.trim() === '') return undefined
  return {
    ip: value.ip.trim(),
    asn: typeof value.asn === 'string' ? value.asn.trim() : undefined,
    as_name: typeof value.as_name === 'string' ? value.as_name.trim() : undefined,
  }
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
