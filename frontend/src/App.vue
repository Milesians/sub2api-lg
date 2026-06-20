<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { bootstrap, getCloudflareTrace, getEntrypoints, getReport, iframeContext, submitReport } from './api/client'
import { buildReport, diagnoseEndpoint, type DiagnoseProgressEvent } from './diagnose/runner'
import type { BootstrapResponse, EndpointResult, EntryPoint } from './types'

type RunStatus = 'idle' | 'running' | 'done' | 'failed'

interface LiveMetrics {
  successRate: number | null
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
  result?: EndpointResult
}

const loading = ref(true)
const running = ref(false)
const error = ref('')
const boot = ref<BootstrapResponse | null>(null)
const entrypoints = ref<EntryPoint[]>([])
const selectedIds = ref<string[]>([])
const runStates = ref<Record<string, EndpointRunState>>({})
const results = ref<EndpointResult[]>([])
const reportId = ref('')
const shareURL = ref('')
const reportJSON = ref<unknown>(null)
const progress = ref('')
const cfTrace = ref<Record<string, string> | null>(null)

const isReportPage = computed(() => window.location.pathname.includes('/report/'))
const token = computed(() => boot.value?.session_token || sessionStorage.getItem('sub2api_lg_session_token') || '')
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
    entrypoints.value = boot.value.entrypoints || []
    selectAllEndpoints()
    void loadCloudflareTrace()
  } catch (e) {
    error.value = String((e as Error)?.message || e)
  } finally {
    loading.value = false
  }
})

async function refreshEntrypoints() {
  if (!token.value) return
  error.value = ''
  clearRun()
  const snapshot = await getEntrypoints(token.value, true)
  entrypoints.value = snapshot.entrypoints || []
  selectAllEndpoints()
}

async function run() {
  if (!boot.value || running.value || selectedEndpoints.value.length === 0) return
  running.value = true
  error.value = ''
  clearRun()
  const endpoints = [...selectedEndpoints.value]
  for (const endpoint of endpoints) {
    setState(endpoint.id, {
      ...blankState(),
      status: 'idle',
      current: '待开始',
    })
  }
  try {
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
        results.value.push(result)
        patchState(endpoint.id, (state) => ({
          ...state,
          status: 'done',
          current: '完成',
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
  }
}

function emptyMetrics(): LiveMetrics {
  return {
    successRate: null,
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
    const status = event.ok ? '成功' : '失败'
    const speed = event.mbps != null ? ` · ${formatMbps(event.mbps)}` : ''
    const latency = event.ttft_ms != null ? ` · TTFT ${formatMs(event.ttft_ms)}` : event.ttfb_ms != null ? ` · TTFB ${formatMs(event.ttfb_ms)}` : ''
    return {
      ...state,
      current: `${event.label} (${event.sample_index}/${event.sample_total})`,
      samples,
      metrics: summarizeSamples(samples),
      logs: [`${event.label} ${status}${latency}${speed}`, ...state.logs].slice(0, 8),
    }
  })
}

function summarizeSamples(samples: DiagnoseProgressEvent[]): LiveMetrics {
  const ping = samples.filter((sample) => sample.kind === 'ping' && sample.ok)
  const ttfb = samples.filter((sample) => sample.ttfb_ms != null && sample.ok).map((sample) => sample.ttfb_ms as number)
  const ttft = samples.filter((sample) => sample.ttft_ms != null && sample.ok).map((sample) => sample.ttft_ms as number)
  const successRate = samples.length > 0 ? samples.filter((sample) => sample.ok).length / samples.length : null
  return {
    successRate,
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
          <button :disabled="running" @click="refreshEntrypoints">刷新端点</button>
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
          <span class="label">成功率</span>
          <strong>{{ pct(aggregate.successRate) }}</strong>
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
              <span class="label">成功率</span>
              <strong>{{ pct(row.state.metrics.successRate) }}</strong>
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
            <div v-for="size in sizeLabels" :key="`${row.endpoint.id}-download-${size}`">
              <span class="label">下载 {{ size }}</span>
              <strong>{{ formatMbps(metricBySize(row.state.metrics.downloadBySize, size)) }}</strong>
            </div>
            <div v-for="size in sizeLabels" :key="`${row.endpoint.id}-upload-${size}`">
              <span class="label">上传 {{ size }}</span>
              <strong>{{ formatMbps(metricBySize(row.state.metrics.uploadBySize, size)) }}</strong>
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
