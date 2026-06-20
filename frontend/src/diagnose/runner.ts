import type { BrowserSummary, EndpointResult, EntryPoint, ProbeConfig } from '../types'
import { joinURL, withNonce } from '../utils/url'
import { percentile, ratio } from './stats'
import { timedFetch, type TimedFetchResult } from './timed-fetch'
import { testDiagStream } from './stream-test'

export async function diagnoseEndpoint(endpoint: EntryPoint, probe: ProbeConfig): Promise<EndpointResult> {
  const pingResults: TimedFetchResult[] = []
  const blobResults: TimedFetchResult[] = []
  for (let i = 0; i < probe.browser_repeat; i += 1) {
    pingResults.push(await timedFetch(withNonce(joinURL(endpoint.lg_base_url, probe.paths.ping)), probe.browser_timeout_ms))
  }
  for (const size of probe.blob_sizes) {
    const url = new URL(joinURL(endpoint.lg_base_url, probe.paths.blob))
    url.searchParams.set('size', size)
    url.searchParams.set('nonce', crypto.randomUUID())
    blobResults.push(await timedFetch(url.toString(), probe.browser_timeout_ms))
  }
  const streamURL = new URL(joinURL(endpoint.lg_base_url, probe.paths.stream))
  streamURL.searchParams.set('events', String(probe.stream.events))
  streamURL.searchParams.set('interval_ms', String(probe.stream.interval_ms))
  streamURL.searchParams.set('bytes', String(probe.stream.bytes))
  streamURL.searchParams.set('nonce', crypto.randomUUID())
  const stream = await testDiagStream(streamURL.toString(), probe.browser_timeout_ms + probe.stream.events * probe.stream.interval_ms + 1000)

  const all = [...pingResults, ...blobResults]
  const successCount = all.filter((item) => item.ok).length + (stream.ok ? 1 : 0)
  const totalCount = all.length + 1
  const durations = pingResults.filter((item) => item.ok).map((item) => item.duration_ms)
  const ttfbValues = pingResults.filter((item) => item.ok && item.ttfb_ms != null).map((item) => item.ttfb_ms as number)
  const p50Duration = percentile(durations, 50)
  const p95Duration = percentile(durations, 95)
  const p50TTFB = percentile(ttfbValues, 50)
  const p95TTFB = percentile(ttfbValues, 95)
  const downloadMbps = calculateDownloadMbps(blobResults)
  const summary: BrowserSummary = {
    success_rate: ratio(successCount, totalCount),
    http_loss_rate: ratio(totalCount - successCount, totalCount),
    p50_duration_ms: p50Duration,
    p95_duration_ms: p95Duration,
    p50_ttfb_ms: p50TTFB,
    p95_ttfb_ms: p95TTFB,
    jitter_ms: p50Duration != null && p95Duration != null ? p95Duration - p50Duration : null,
    timeout_rate: ratio(all.filter((item) => item.error_kind === 'timeout').length + (stream.error_kind === 'timeout' ? 1 : 0), totalCount),
    download_mbps: downloadMbps,
    first_event_ms: stream.first_event_ms,
    max_chunk_gap_ms: stream.max_event_gap_ms,
    stream_buffered: stream.stream_buffered,
    cors_blocked: all.some((item) => item.error_message?.toLowerCase().includes('cors') || item.error_message?.toLowerCase().includes('failed to fetch')),
    timing_detail_available: all.some((item) => item.timing_detail_available),
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

function calculateDownloadMbps(items: TimedFetchResult[]): number | null {
  const ok = items.filter((item) => item.ok && item.response_bytes && item.duration_ms > 0)
  if (ok.length === 0) return null
  const mbps = ok.map((item) => ((item.response_bytes || 0) * 8) / (item.duration_ms / 1000) / 1_000_000)
  return Number((mbps.reduce((sum, item) => sum + item, 0) / mbps.length).toFixed(2))
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
