<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { bootstrap, getCloudflareTrace, getEntrypoints, getReport, iframeContext, submitReport } from './api/client'
import { asnLabel, traceEndpointFromBrowser, uniqueASNLabels } from './diagnose/client-trace'
import { buildReport, diagnoseEndpoint, type DiagnoseProgressEvent } from './diagnose/runner'
import type { BootstrapResponse, ClientTraceInfo, EndpointResult, EntryPoint } from './types'

type RunStatus = 'idle' | 'running' | 'done' | 'failed'

interface LiveMetrics {
  successRate: number | null
  pingSuccessRate: number | null
  avgPing: number | null
  avgTTFB: number | null
  avgTTFT: number | null
  downloadBySize: Record<string, number | null>
  uploadBySize: Record<string, number | null>
}

interface EndpointRunState {
  status: RunStatus
  current: string
  logs: string[]
  samples: DiagnoseProgressEvent[]
  metrics: LiveMetrics
  originPeerIPs: string[]
  clientTrace?: ClientTraceInfo | null
  result?: EndpointResult
}

const loading = ref(true)
const running = ref(false)
const error = ref('')
const boot = ref<BootstrapResponse | null>(null)
const backendEntrypoints = ref<EntryPoint[]>([])
const manualEndpoints = ref<EntryPoint[]>([])
const selectedIds = ref<string[]>([])
const runStates = ref<Record<string, EndpointRunState>>({})
const results = ref<EndpointResult[]>([])
const reportId = ref('')
const shareURL = ref('')
const reportJSON = ref<unknown>(null)
const progress = ref('')
const cfTrace = ref<Record<string, string> | null>(null)
const manualName = ref('')
const manualURL = ref('')

const isReportPage = computed(() => window.location.pathname.includes('/report/'))
const token = computed(() => boot.value?.session_token || sessionStorage.getItem('sub2api_lg_session_token') || '')
const isAdmin = computed(() => boot.value?.user?.role === 'admin')
const entrypoints = computed(() => [...backendEntrypoints.value, ...manualEndpoints.value])
const best = computed(() => [...results.value].sort((a, b) => b.browser.success_rate - a.browser.success_rate)[0])
const rows = computed(() => entrypoints.value.map((endpoint) => ({
  endpoint,
  selected: selectedIds.value.includes(endpoint.id),
  state: endpointState(endpoint.id),
  result: results.value.find((item) => item.endpoint_id === endpoint.id),
})))
const selectedEndpoints = computed(() => entrypoints.value.filter((endpoint) => selectedIds.value.includes(endpoint.id)))
const selectedRows = computed(() => rows.value.filter((row) => row.selected))
const sizeLabels = computed(() => normalizeSizes(boot.value?.probe.blob_sizes || ['64k', '1m', '5m', '20m']))
const largestSize = computed(() => sizeLabels.value[sizeLabels.value.length - 1] || '20m')
const aggregate = computed(() => {
  const states = selectedIds.value.map((id) => endpointState(id)).filter((state) => state.samples.length > 0 || state.result)
  return {
    successRate: averageMetric(states.map((state) => state.metrics.successRate)),
    pingSuccessRate: averageMetric(states.map((state) => state.metrics.pingSuccessRate)),
    avgPing: averageMetric(states.map((state) => state.metrics.avgPing)),
    avgTTFB: averageMetric(states.map((state) => state.metrics.avgTTFB)),
    avgTTFT: averageMetric(states.map((state) => state.metrics.avgTTFT)),
    download: averageMetric(states.map((state) => metricBySize(state.metrics.downloadBySize, largestSize.value))),
    upload: averageMetric(states.map((state) => metricBySize(state.metrics.uploadBySize, largestSize.value))),
  }
})

onMounted(async () => {
  try {
    if (isReportPage.value) {
      await loadReportPage()
      return
    }
    boot.value = await bootstrap()
    backendEntrypoints.value = boot.value.entrypoints || []
    selectAllEndpoints()
    void loadCloudflareTrace()
  } catch (e) {
    error.value = String((e as Error)?.message || e)
  } finally {
    loading.value = false
  }
})

async function loadLatestEntrypoints(preserveSelection: boolean) {
  if (!token.value) return
  const previous = new Set(selectedIds.value)
  const snapshot = await getEntrypoints(token.value)
  backendEntrypoints.value = snapshot.entrypoints || []
  if (!preserveSelection || previous.size === 0) {
    selectAllEndpoints()
    return
  }
  const liveIDs = new Set(entrypoints.value.map((endpoint) => endpoint.id))
  selectedIds.value = Array.from(previous).filter((id) => liveIDs.has(id))
}

async function run() {
  if (!boot.value || running.value || selectedEndpoints.value.length === 0) return
  running.value = true
  error.value = ''
  clearRun()
  try {
    await loadLatestEntrypoints(true)
    const endpoints = [...selectedEndpoints.value]
    if (endpoints.length === 0) throw new Error('没有端点可测试')
    for (const endpoint of endpoints) {
      setState(endpoint.id, {
        ...blankState(),
        status: 'idle',
        current: '待开始',
      })
    }
    for (const endpoint of endpoints) {
      progress.value = endpoint.name
      patchState(endpoint.id, (state) => ({
        ...state,
        status: 'running',
        current: '准备测试',
        logs: ['准备测试'],
      }))
      try {
        const result = await diagnoseEndpoint(endpoint, boot.value.probe, (event) => recordProgress(event))
        const clientTrace = await diagnoseClientTrace(endpoint, result)
        result.client_trace = clientTrace
        results.value.push(result)
        patchState(endpoint.id, (state) => ({
          ...state,
          status: 'done',
          current: '完成',
          clientTrace,
          result,
          metrics: metricsFromResult(result),
          logs: [`完成，成功率 ${pct(result.browser.success_rate)}`, ...state.logs].slice(0, 8),
        }))
      } catch (e) {
        const message = String((e as Error)?.message || e)
        patchState(endpoint.id, (state) => ({
          ...state,
          status: 'failed',
          current: '失败',
          logs: [`失败：${message}`, ...state.logs].slice(0, 8),
        }))
      }
    }
    if (results.value.length === 0) throw new Error('没有端点完成测试')
    const payload = buildReport(results.value, iframeContext())
    const saved = await submitReport(token.value, payload)
    reportId.value = saved.report_id
    shareURL.value = saved.share_url
    notifyParent(payload.summary)
  } catch (e) {
    error.value = String((e as Error)?.message || e)
  } finally {
    progress.value = ''
    running.value = false
  }
}

async function loadCloudflareTrace() {
  cfTrace.value = await getCloudflareTrace()
}

async function diagnoseClientTrace(endpoint: EntryPoint, result: EndpointResult): Promise<ClientTraceInfo | null> {
  patchState(endpoint.id, (state) => ({
    ...state,
    current: '客户端 Trace 中',
    logs: ['客户端 Trace 中', ...state.logs].slice(0, 8),
  }))
  try {
    const clientTrace = await traceEndpointFromBrowser(endpoint, result.browser)
    patchState(endpoint.id, (state) => ({
      ...state,
      clientTrace,
      logs: [`客户端 Trace 完成：${clientTrace.ips?.length || 0} 个 IP`, ...state.logs].slice(0, 8),
    }))
    return clientTrace
  } catch (e) {
    const clientTrace: ClientTraceInfo = {
      source: 'browser',
      host: endpoint.host,
      checked_at: new Date().toISOString(),
      error: String((e as Error)?.message || e),
    }
    patchState(endpoint.id, (state) => ({
      ...state,
      clientTrace,
      logs: [`客户端 Trace 失败：${clientTrace.error}`, ...state.logs].slice(0, 8),
    }))
    return clientTrace
  }
}

async function loadReportPage() {
  const injected = (window as any).__SUB2API_LG_REPORT__
  if (injected) {
    reportJSON.value = injected
    return
  }
  const id = window.location.pathname.split('/report/')[1]?.split('/')[0]
  if (!id) throw new Error('report id missing')
  reportJSON.value = await getReport(id)
}

function notifyParent(summary: any) {
  const srcURL = iframeContext().src_url
  if (!srcURL) return
  let parentOrigin = ''
  try {
    parentOrigin = new URL(srcURL).origin
  } catch {
    return
  }
  window.parent.postMessage({
    type: 'sub2api-lg:completed',
    report_id: reportId.value,
    score: summary.score,
    best_endpoint_id: summary.best_endpoint_id,
  }, parentOrigin)
}

function clearRun() {
  results.value = []
  reportId.value = ''
  shareURL.value = ''
  progress.value = ''
  runStates.value = {}
}

function selectAllEndpoints() {
  selectedIds.value = entrypoints.value.map((endpoint) => endpoint.id)
}

function clearSelection() {
  if (running.value) return
  selectedIds.value = []
}

function toggleEndpoint(id: string, event: Event) {
  if (running.value) return
  const checked = (event.target as HTMLInputElement).checked
  selectedIds.value = checked
    ? Array.from(new Set([...selectedIds.value, id]))
    : selectedIds.value.filter((item) => item !== id)
}

function addManualEndpoint() {
  if (!isAdmin.value || running.value) return
  error.value = ''
  try {
    const endpoint = buildManualEndpoint(manualURL.value, manualName.value)
    const exists = entrypoints.value.some((item) => item.base_url === endpoint.base_url || item.lg_base_url === endpoint.lg_base_url)
    if (exists) throw new Error('该 endpoint 已存在')
    manualEndpoints.value = [...manualEndpoints.value, endpoint]
    selectedIds.value = Array.from(new Set([...selectedIds.value, endpoint.id]))
    manualName.value = ''
    manualURL.value = ''
  } catch (e) {
    error.value = String((e as Error)?.message || e)
  }
}

function removeManualEndpoint(id: string) {
  if (running.value) return
  manualEndpoints.value = manualEndpoints.value.filter((endpoint) => endpoint.id !== id)
  selectedIds.value = selectedIds.value.filter((item) => item !== id)
}

function endpointState(id: string): EndpointRunState {
  return runStates.value[id] || blankState()
}

function blankState(): EndpointRunState {
  return {
    status: 'idle',
    current: '待开始',
    logs: [],
    samples: [],
    metrics: emptyMetrics(),
    originPeerIPs: [],
  }
}

function emptyMetrics(): LiveMetrics {
  return {
    successRate: null,
    pingSuccessRate: null,
    avgPing: null,
    avgTTFB: null,
    avgTTFT: null,
    downloadBySize: {},
    uploadBySize: {},
  }
}

function setState(id: string, state: EndpointRunState) {
  runStates.value = { ...runStates.value, [id]: state }
}

function patchState(id: string, update: (state: EndpointRunState) => EndpointRunState) {
  setState(id, update(endpointState(id)))
}

function recordProgress(event: DiagnoseProgressEvent) {
  patchState(event.endpoint_id, (state) => {
    const samples = [...state.samples, event]
    const originPeerIPs = event.origin_peer_ip ? appendUnique(state.originPeerIPs, event.origin_peer_ip) : state.originPeerIPs
    const status = event.ok ? '成功' : '失败'
    const speed = event.mbps != null ? ` · ${formatMbps(event.mbps)}` : ''
    const latency = event.ttft_ms != null ? ` · TTFT ${formatMs(event.ttft_ms)}` : event.ttfb_ms != null ? ` · TTFB ${formatMs(event.ttfb_ms)}` : ''
    return {
      ...state,
      current: `${event.label} (${event.sample_index}/${event.sample_total})`,
      samples,
      originPeerIPs,
      metrics: summarizeSamples(samples),
      logs: [`${event.label} ${status}${latency}${speed}`, ...state.logs].slice(0, 8),
    }
  })
}

function summarizeSamples(samples: DiagnoseProgressEvent[]): LiveMetrics {
  const ping = samples.filter((sample) => sample.kind === 'ping' && sample.ok)
  const pingTotal = samples.filter((sample) => sample.kind === 'ping')
  const ttfb = samples.filter((sample) => sample.ttfb_ms != null && sample.ok).map((sample) => sample.ttfb_ms as number)
  const ttft = samples.filter((sample) => sample.ttft_ms != null && sample.ok).map((sample) => sample.ttft_ms as number)
  const successRate = samples.length > 0 ? samples.filter((sample) => sample.ok).length / samples.length : null
  return {
    successRate,
    pingSuccessRate: pingTotal.length > 0 ? ping.length / pingTotal.length : null,
    avgPing: averageMetric(ping.map((sample) => sample.duration_ms ?? null)),
    avgTTFB: averageMetric(ttfb),
    avgTTFT: averageMetric(ttft),
    downloadBySize: speedsByKind(samples, 'download'),
    uploadBySize: speedsByKind(samples, 'upload'),
  }
}

function metricsFromResult(result: EndpointResult): LiveMetrics {
  return {
    successRate: result.browser.success_rate,
    pingSuccessRate: result.browser.ping_success_rate ?? result.browser.success_rate,
    avgPing: result.browser.avg_ping_ms,
    avgTTFB: result.browser.avg_ttfb_ms,
    avgTTFT: result.browser.avg_ttft_ms,
    downloadBySize: result.browser.download_mbps_by_size || legacySizeMap(result.browser.download_small_mbps, result.browser.download_large_mbps),
    uploadBySize: result.browser.upload_mbps_by_size || legacySizeMap(result.browser.upload_small_mbps, result.browser.upload_large_mbps),
  }
}

function speedsByKind(samples: DiagnoseProgressEvent[], kind: 'download' | 'upload'): Record<string, number | null> {
  const out: Record<string, number | null> = {}
  for (const size of sizeLabels.value) {
    out[size] = speedByKind(samples, kind, size)
  }
  return out
}

function speedByKind(samples: DiagnoseProgressEvent[], kind: 'download' | 'upload', size: string): number | null {
  const values = samples
    .filter((sample) => sample.kind === kind && sample.size === size && sample.mbps != null && sample.ok)
    .map((sample) => sample.mbps as number)
  return averageMetric(values)
}

function legacySizeMap(small: number | null, large: number | null): Record<string, number | null> {
  const sizes = sizeLabels.value
  return {
    [sizes[0] || '64k']: small,
    [sizes[sizes.length - 1] || '20m']: large,
  }
}

function metricBySize(values: Record<string, number | null>, size: string): number | null {
  return values[size] ?? null
}

function averageMetric(values: Array<number | null | undefined>): number | null {
  const ok = values.filter((value): value is number => value != null && Number.isFinite(value))
  if (ok.length === 0) return null
  return Number((ok.reduce((sum, item) => sum + item, 0) / ok.length).toFixed(2))
}

function formatMs(value: number | null | undefined) {
  return value == null ? '-' : `${Math.round(value)} ms`
}

function formatMbps(value: number | null | undefined) {
  return value == null ? '-' : `${Number(value).toFixed(2)} Mbps`
}

function pct(value: number | null | undefined) {
  return value == null ? '-' : `${Math.round(value * 100)}%`
}

function originPeerIPsText(values: string[]): string {
  if (values.length === 0) return '-'
  const visible = values.slice(0, 4)
  const suffix = values.length > visible.length ? ` / +${values.length - visible.length}` : ''
  return `${visible.join(' / ')}${suffix}`
}

function appendUnique(values: string[], value: string): string[] {
  return values.includes(value) ? values : [...values, value]
}

function statusText(status: RunStatus) {
  if (status === 'running') return '测试中'
  if (status === 'done') return '完成'
  if (status === 'failed') return '失败'
  return '待开始'
}

function normalizeSizes(sizes: string[]): string[] {
  const out = Array.from(new Set(sizes.map((item) => item.trim().toLowerCase()).filter(Boolean)))
  return out.length > 0 ? out : ['64k', '1m', '5m', '20m']
}

function traceValue(key: string): string {
  return cfTrace.value?.[key] || '-'
}

function traceIPs(trace: ClientTraceInfo | null | undefined): string {
  if (!trace?.ips?.length) return '-'
  return trace.ips.map((item) => item.ip).join(' / ')
}

function traceASNs(trace: ClientTraceInfo | null | undefined): string {
  return uniqueASNLabels(trace?.ips)
}

function traceNetworks(trace: ClientTraceInfo | null | undefined): string {
  if (!trace?.ips?.length) return '-'
  const names = trace.ips.map((item) => item.asn?.name).filter(Boolean) as string[]
  return Array.from(new Set(names)).join(' / ') || '-'
}

function traceNote(trace: ClientTraceInfo | null | undefined): string {
  if (trace?.error) return trace.error
  if (trace?.note === 'browser_hop_trace_unavailable') return '浏览器不可获取逐跳 hop'
  return '-'
}

function buildManualEndpoint(rawURL: string, rawName: string): EntryPoint {
  const raw = rawURL.trim()
  if (!raw) throw new Error('请输入 endpoint URL')
  const input = raw.includes('://') ? raw : `https://${raw}`
  const parsed = new URL(input)
  if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') {
    throw new Error('endpoint 只支持 http 或 https')
  }
  parsed.search = ''
  parsed.hash = ''
  const publicPath = boot.value?.app.public_path || '/lg'
  let basePath = parsed.pathname.replace(/\/+$/, '')
  if (basePath === publicPath || basePath.endsWith(publicPath)) {
    basePath = basePath.slice(0, -publicPath.length).replace(/\/+$/, '')
  }
  parsed.pathname = basePath || '/'
  const baseURL = parsed.toString().replace(/\/+$/, '')
  const lgBaseURL = `${baseURL}${publicPath}`
  const name = rawName.trim() || parsed.hostname
  return {
    id: manualEndpointID(baseURL),
    source: 'manual',
    name,
    description: 'manual endpoint',
    raw_value: raw,
    base_url: baseURL,
    public_path: publicPath,
    lg_base_url: lgBaseURL,
    origin: parsed.origin,
    host: parsed.host,
    scheme: parsed.protocol.replace(':', ''),
    enabled: true,
  }
}

function manualEndpointID(value: string): string {
  let hash = 0
  for (let i = 0; i < value.length; i += 1) {
    hash = ((hash << 5) - hash + value.charCodeAt(i)) | 0
  }
  return `manual_${Math.abs(hash).toString(36)}`
}
</script>

<template>
  <main class="page">
    <section v-if="loading" class="state">加载中</section>
    <section v-else-if="isReportPage" class="stack">
      <header class="toolbar">
        <div>
          <h1>诊断报告</h1>
          <p>报告 JSON</p>
        </div>
      </header>
      <pre class="json">{{ JSON.stringify(reportJSON, null, 2) }}</pre>
      <p v-if="error" class="error">{{ error }}</p>
    </section>
    <section v-else class="stack">
      <header class="toolbar">
        <div>
          <h1>源站入口性能诊断</h1>
          <p>{{ entrypoints.length }} 个入口 · 已选 {{ selectedEndpoints.length }} 个 · {{ boot?.entrypoint_source || '未获取' }}</p>
        </div>
        <div class="actions">
          <button :disabled="running" @click="selectAllEndpoints">全选</button>
          <button :disabled="running" @click="clearSelection">清空</button>
          <button class="primary" :disabled="running || selectedEndpoints.length === 0" @click="run">
            {{ running ? '测试中' : '开始测试' }}
          </button>
        </div>
      </header>

      <div v-if="running" class="progress">正在测试：{{ progress }}</div>
      <div v-else-if="results.length === 0" class="state">待开始</div>
      <p v-if="error" class="error">{{ error }}</p>

      <section class="summary">
        <div>
          <span class="label">综合成功率</span>
          <strong>{{ pct(aggregate.successRate) }}</strong>
        </div>
        <div>
          <span class="label">Ping 成功率</span>
          <strong>{{ pct(aggregate.pingSuccessRate) }}</strong>
        </div>
        <div>
          <span class="label">平均 Ping</span>
          <strong>{{ formatMs(aggregate.avgPing) }}</strong>
        </div>
        <div>
          <span class="label">平均 TTFB</span>
          <strong>{{ formatMs(aggregate.avgTTFB) }}</strong>
        </div>
        <div>
          <span class="label">平均 TTFT</span>
          <strong>{{ formatMs(aggregate.avgTTFT) }}</strong>
        </div>
        <div>
          <span class="label">最大包下载 {{ largestSize }}</span>
          <strong>{{ formatMbps(aggregate.download) }}</strong>
        </div>
        <div>
          <span class="label">最大包上传 {{ largestSize }}</span>
          <strong>{{ formatMbps(aggregate.upload) }}</strong>
        </div>
      </section>

      <section v-if="cfTrace" class="selector-panel">
        <div class="panel-head">
          <h2>Cloudflare Trace</h2>
          <span>{{ traceValue('h') }}</span>
        </div>
        <div class="metric-grid trace-grid">
          <div>
            <span class="label">边缘节点</span>
            <strong>{{ traceValue('colo') }}</strong>
          </div>
          <div>
            <span class="label">访问地区</span>
            <strong>{{ traceValue('loc') }}</strong>
          </div>
          <div>
            <span class="label">HTTP/TLS</span>
            <strong>{{ traceValue('http') }} / {{ traceValue('tls') }}</strong>
          </div>
          <div>
            <span class="label">WARP</span>
            <strong>{{ traceValue('warp') }}</strong>
          </div>
          <div>
            <span class="label">SNI</span>
            <strong>{{ traceValue('sni') }}</strong>
          </div>
        </div>
      </section>

      <section class="selector-panel">
        <div class="panel-head">
          <h2>选择端点</h2>
          <span>{{ selectedEndpoints.length }}/{{ entrypoints.length }}</span>
        </div>
        <form v-if="isAdmin" class="manual-endpoint" @submit.prevent="addManualEndpoint">
          <input v-model="manualName" :disabled="running" placeholder="名称">
          <input v-model="manualURL" :disabled="running" placeholder="https://example.com">
          <button :disabled="running" type="submit">添加测试端点</button>
        </form>
        <div class="endpoint-grid">
          <label
            v-for="row in rows"
            :key="row.endpoint.id"
            :class="['endpoint-option', { selected: row.selected }]"
          >
            <input
              type="checkbox"
              :checked="row.selected"
              :disabled="running"
              @change="toggleEndpoint(row.endpoint.id, $event)"
            >
            <span class="endpoint-main">
              <strong>{{ row.endpoint.name }}</strong>
              <span>{{ row.endpoint.lg_base_url }}</span>
            </span>
            <span :class="['run-status', row.state.status]">{{ statusText(row.state.status) }}</span>
            <button
              v-if="row.endpoint.source === 'manual'"
              class="icon-button"
              :disabled="running"
              type="button"
              @click.prevent.stop="removeManualEndpoint(row.endpoint.id)"
            >
              移除
            </button>
          </label>
        </div>
      </section>

      <section v-if="best" class="summary">
        <div>
          <span class="label">推荐入口</span>
          <strong>{{ best.name }}</strong>
        </div>
        <div>
          <span class="label">成功率</span>
          <strong>{{ Math.round(best.browser.success_rate * 100) }}%</strong>
        </div>
        <div>
          <span class="label">p95 总耗时</span>
          <strong>{{ best.browser.p95_duration_ms ?? '-' }} ms</strong>
        </div>
        <div>
          <span class="label">p95 首包</span>
          <strong>{{ best.browser.p95_ttfb_ms ?? '-' }} ms</strong>
        </div>
      </section>

      <section class="live-grid">
        <article v-for="row in selectedRows" :key="row.endpoint.id" class="live-card">
          <header class="card-head">
            <div>
              <h2>{{ row.endpoint.name }}</h2>
              <p>{{ row.endpoint.lg_base_url }}</p>
            </div>
            <span :class="['run-status', row.state.status]">{{ statusText(row.state.status) }}</span>
          </header>
          <div class="current-line">{{ row.state.current }}</div>
          <div class="meter">
            <span :style="{ width: `${row.state.samples.length ? Math.round(row.state.samples.length / (row.state.samples[0]?.sample_total || 1) * 100) : 0}%` }"></span>
          </div>
          <div class="metric-grid">
            <div>
              <span class="label">综合成功率</span>
              <strong>{{ pct(row.state.metrics.successRate) }}</strong>
            </div>
            <div>
              <span class="label">Ping 成功率</span>
              <strong>{{ pct(row.state.metrics.pingSuccessRate) }}</strong>
            </div>
            <div>
              <span class="label">Ping 平均</span>
              <strong>{{ formatMs(row.state.metrics.avgPing) }}</strong>
            </div>
            <div>
              <span class="label">TTFB 平均</span>
              <strong>{{ formatMs(row.state.metrics.avgTTFB) }}</strong>
            </div>
            <div>
              <span class="label">TTFT 平均</span>
              <strong>{{ formatMs(row.state.metrics.avgTTFT) }}</strong>
            </div>
            <div>
              <span class="label">回源连接 IP</span>
              <strong>{{ originPeerIPsText(row.state.originPeerIPs) }}</strong>
            </div>
            <div v-for="size in sizeLabels" :key="`${row.endpoint.id}-download-${size}`">
              <span class="label">下载 {{ size }}</span>
              <strong>{{ formatMbps(metricBySize(row.state.metrics.downloadBySize, size)) }}</strong>
            </div>
            <div v-for="size in sizeLabels" :key="`${row.endpoint.id}-upload-${size}`">
              <span class="label">上传 {{ size }}</span>
              <strong>{{ formatMbps(metricBySize(row.state.metrics.uploadBySize, size)) }}</strong>
            </div>
          </div>
          <div v-if="row.state.clientTrace" class="client-trace">
            <div class="route-summary">
              <div>
                <span class="label">端点解析 IP</span>
                <strong>{{ traceIPs(row.state.clientTrace) }}</strong>
              </div>
              <div>
                <span class="label">端点 ASN</span>
                <strong>{{ traceASNs(row.state.clientTrace) }}</strong>
              </div>
              <div>
                <span class="label">网络归属</span>
                <strong>{{ traceNetworks(row.state.clientTrace) }}</strong>
              </div>
              <div>
                <span class="label">Ping 平均</span>
                <strong>{{ formatMs(row.state.clientTrace.avg_ping_ms) }}</strong>
              </div>
              <div>
                <span class="label">TTFB 平均</span>
                <strong>{{ formatMs(row.state.clientTrace.avg_ttfb_ms) }}</strong>
              </div>
              <div>
                <span class="label">TTFT 平均</span>
                <strong>{{ formatMs(row.state.clientTrace.avg_ttft_ms) }}</strong>
              </div>
              <div>
                <span class="label">Trace 状态</span>
                <strong>{{ traceNote(row.state.clientTrace) }}</strong>
              </div>
            </div>
            <div v-if="row.state.clientTrace.ips?.length" class="trace-table">
              <div class="trace-row trace-head">
                <span>IP</span>
                <span>ASN</span>
                <span>网络</span>
                <span>Prefix</span>
              </div>
              <div v-for="ip in row.state.clientTrace.ips" :key="`${row.endpoint.id}-${ip.ip}`" class="trace-row">
                <span>{{ ip.ip }}</span>
                <span>{{ asnLabel(ip.asn) }}</span>
                <span>{{ ip.asn?.name || '-' }}</span>
                <span>{{ ip.asn?.prefix || '-' }}</span>
              </div>
            </div>
          </div>
          <ol class="log-list">
            <li v-for="item in row.state.logs" :key="item">{{ item }}</li>
            <li v-if="row.state.logs.length === 0">待测试</li>
          </ol>
        </article>
      </section>

      <section v-if="shareURL" class="report-line">
        <span>报告：{{ reportId }}</span>
        <a :href="shareURL" target="_blank" rel="noreferrer">打开报告</a>
      </section>
    </section>
  </main>
</template>
