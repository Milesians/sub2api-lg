<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { adminBootstrap, bootstrap, getAdminReport, getEntrypointInventory, getEntrypoints, getReport, listAdminReports, resolveCustomNetinfo, submitReport } from './api/client'
import { buildReport, diagnoseEndpoint, type DiagnoseProgressEvent } from './diagnose/runner'
import type { BootstrapResponse, EndpointResult, EntryPoint, IPInfo } from './types'

type RunStatus = 'idle' | 'running' | 'done' | 'failed'
type ViewMode = 'customer' | 'report' | 'admin'
type TraceStatus = 'idle' | 'loading' | 'done' | 'failed'

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
  result?: EndpointResult
}

interface CloudflareTraceState {
  status: TraceStatus
  url: string
  raw: string
  values: Record<string, string>
  error: string
  fetched_at: string
}

const cloudflareTraceURL = 'https://sub2api.htao.ltd/cdn-cgi/trace'

const loading = ref(true)
const running = ref(false)
const error = ref('')
const boot = ref<BootstrapResponse | null>(null)
const backendEntrypoints = ref<EntryPoint[]>([])
const customEntrypoints = ref<EntryPoint[]>([])
const selectedIds = ref<string[]>([])
const runStates = ref<Record<string, EndpointRunState>>({})
const results = ref<EndpointResult[]>([])
const reportId = ref('')
const shareURL = ref('')
const reportJSON = ref<any | null>(null)
const progress = ref('')
const customName = ref('')
const customURL = ref('')
const cloudflareTrace = ref<CloudflareTraceState>(blankCloudflareTrace())
let cloudflareTraceLoad: Promise<void> | null = null

const adminReports = ref<any[]>([])
const adminTotal = ref(0)
const adminReportDetail = ref<any | null>(null)
const adminInventory = ref<any | null>(null)
const adminReportIdFilter = ref('')
const adminUserFilter = ref('')
const adminLevelFilter = ref('')
const adminRawOpen = ref(false)

const pathName = window.location.pathname
const viewMode: ViewMode = pathName.includes('/report/') ? 'report' : pathName.includes('/admin') ? 'admin' : 'customer'
const isReportPage = computed(() => viewMode === 'report')
const isAdminPage = computed(() => viewMode === 'admin')
const token = computed(() => boot.value?.session_token || sessionStorage.getItem('sub2api_lg_session_token') || '')
const adminToken = computed(() => boot.value?.session_token || sessionStorage.getItem('sub2api_lg_admin_session_token') || '')
const entrypoints = computed(() => orderedEntrypoints([...backendEntrypoints.value, ...customEntrypoints.value]))
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

const report = computed(() => reportJSON.value || {})
const reportSummary = computed(() => report.value.summary || {})
const reportEntrypoints = computed<any[]>(() => Array.isArray(report.value.entrypoints) ? report.value.entrypoints : [])
const reportClientEnv = computed<Record<string, string>>(() => report.value.client_env || {})
const reportCloudflareTrace = computed(() => normalizeCloudflareTrace(report.value.cloudflare_trace || {}))
const reportCloudflareTraceVisible = computed(() => hasCloudflareTrace(report.value.cloudflare_trace))
const reportSupportRef = computed(() => report.value.support_reference || {})
const adminCustomerReport = computed(() => adminReportDetail.value?.customer_report || {})
const adminInternalReport = computed(() => adminReportDetail.value?.internal_report || {})
const adminOwnerSession = computed(() => adminReportDetail.value?.owner_session || {})
const adminInternalEntrypoints = computed<any[]>(() => Array.isArray(adminInternalReport.value?.entrypoints) ? adminInternalReport.value.entrypoints : [])
const adminDiagnosis = computed<any[]>(() => Array.isArray(adminInternalReport.value?.diagnosis) ? adminInternalReport.value.diagnosis : [])

onMounted(async () => {
  applyTheme(new URLSearchParams(window.location.search).get('theme') || '')
  try {
    if (isReportPage.value) {
      await loadReportPage()
      return
    }
    if (isAdminPage.value) {
      await loadAdminPage()
      return
    }
    boot.value = await bootstrap()
    applyTheme(boot.value.app?.theme || '')
    backendEntrypoints.value = normalizeEntrypoints(boot.value.entrypoints || [])
    selectAllEndpoints()
    void ensureCloudflareTrace()
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
  backendEntrypoints.value = normalizeEntrypoints(snapshot.entrypoints || [])
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
    const runID = `run_${crypto.randomUUID()}`
    for (const endpoint of endpoints) {
      setState(endpoint.id, { ...blankState(), status: 'idle', current: '待开始' })
    }
    for (const endpoint of endpoints) {
      progress.value = displayName(endpoint)
      patchState(endpoint.id, (state) => ({ ...state, status: 'running', current: '准备测试', logs: ['准备测试'] }))
      try {
        const result = await diagnoseEndpoint(endpoint, boot.value.probe, runID, (event) => recordProgress(event))
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
    await ensureCloudflareTrace()
    const payload = buildReport(runID, allSamples(), endpointLabels(), customEndpointPayload(), endpointNetInfoPayload(), cloudflareTracePayload())
    const saved = await submitReport(token.value, payload)
    reportId.value = saved.report_id
    shareURL.value = saved.share_url
    notifyParent(saved.customer_summary)
  } catch (e) {
    error.value = String((e as Error)?.message || e)
  } finally {
    progress.value = ''
    running.value = false
  }
}

async function addCustomEndpoint() {
  if (running.value) return
  error.value = ''
  try {
    if (!boot.value) throw new Error('页面尚未初始化')
    let endpoint = buildCustomEndpoint(customURL.value, customName.value, boot.value.app.public_path)
    if (entrypoints.value.some((item) => item.id === endpoint.id)) throw new Error('该自定义端点已存在')
    endpoint = await enrichCustomEndpoint(endpoint)
    customEntrypoints.value = [...customEntrypoints.value, endpoint]
    selectedIds.value = Array.from(new Set([...selectedIds.value, endpoint.id]))
    customName.value = ''
    customURL.value = ''
  } catch (e) {
    error.value = String((e as Error)?.message || e)
  }
}

async function enrichCustomEndpoint(endpoint: EntryPoint): Promise<EntryPoint> {
  if (!token.value) return endpoint
  const body = await resolveCustomNetinfo(token.value, [{
    endpoint_public_id: endpoint.endpoint_public_id || endpoint.id,
    display_name: displayName(endpoint),
    probe_base_url: endpoint.probe_base_url || '',
  }])
  const item = (body.items || []).find((candidate: any) => candidate.endpoint_public_id === (endpoint.endpoint_public_id || endpoint.id))
  return { ...endpoint, dns_records: Array.isArray(item?.dns_records) ? item.dns_records : [] }
}

function removeCustomEndpoint(id: string) {
  if (running.value) return
  customEntrypoints.value = customEntrypoints.value.filter((endpoint) => endpoint.id !== id)
  selectedIds.value = selectedIds.value.filter((item) => item !== id)
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

async function loadAdminPage() {
  boot.value = await adminBootstrap()
  applyTheme(boot.value.app?.theme || '')
  await Promise.all([refreshAdminReports(), loadAdminInventory()])
}

async function refreshAdminReports() {
  if (!adminToken.value) return
  const params: Record<string, string> = {}
  if (adminReportIdFilter.value.trim()) params.report_id = adminReportIdFilter.value.trim()
  if (adminUserFilter.value.trim()) params.user_id = adminUserFilter.value.trim()
  if (adminLevelFilter.value.trim()) params.level = adminLevelFilter.value.trim()
  const body = await listAdminReports(adminToken.value, params)
  adminReports.value = body.items || []
  adminTotal.value = body.total || 0
}

async function openAdminReport(reportID: string) {
  if (!adminToken.value || !reportID) return
  adminReportDetail.value = await getAdminReport(adminToken.value, reportID)
  adminRawOpen.value = false
}

async function loadAdminInventory() {
  if (!adminToken.value) return
  adminInventory.value = await getEntrypointInventory(adminToken.value)
}

function notifyParent(summary: any) {
  const params = new URLSearchParams(window.location.search)
  const srcURL = params.get('src_url') || ''
  if (!srcURL) return
  try {
    window.parent.postMessage({
      type: 'sub2api-lg:customer-report-created',
      report_id: reportId.value,
      score: summary?.score,
      level: summary?.level,
    }, new URL(srcURL).origin)
  } catch {
    // Parent notification is optional.
  }
}

function blankCloudflareTrace(status: TraceStatus = 'idle'): CloudflareTraceState {
  return { status, url: cloudflareTraceURL, raw: '', values: {}, error: '', fetched_at: '' }
}

async function ensureCloudflareTrace() {
  if (isReportPage.value || isAdminPage.value) return
  if (cloudflareTrace.value.status === 'done' || cloudflareTrace.value.status === 'failed') return
  if (!cloudflareTraceLoad) {
    cloudflareTraceLoad = loadCloudflareTrace().finally(() => {
      cloudflareTraceLoad = null
    })
  }
  await cloudflareTraceLoad
}

async function loadCloudflareTrace() {
  cloudflareTrace.value = { ...blankCloudflareTrace('loading'), fetched_at: new Date().toISOString() }
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(), 5000)
  try {
    const res = await fetch(cloudflareTraceURL, { cache: 'no-store', signal: controller.signal })
    const raw = (await res.text()).trim()
    if (!res.ok) throw new Error(`trace failed: ${res.status}`)
    cloudflareTrace.value = {
      status: 'done',
      url: cloudflareTraceURL,
      raw,
      values: parseCloudflareTrace(raw),
      error: '',
      fetched_at: new Date().toISOString(),
    }
  } catch (e) {
    cloudflareTrace.value = {
      ...blankCloudflareTrace('failed'),
      error: String((e as Error)?.message || e),
      fetched_at: new Date().toISOString(),
    }
  } finally {
    window.clearTimeout(timeout)
  }
}

function parseCloudflareTrace(raw: string): Record<string, string> {
  const values: Record<string, string> = {}
  for (const line of raw.split(/\r?\n/)) {
    const index = line.indexOf('=')
    if (index <= 0) continue
    const key = line.slice(0, index).trim()
    const value = line.slice(index + 1).trim()
    if (key && value) values[key] = value
  }
  return values
}

function cloudflareTracePayload(): Record<string, string> {
  const state = cloudflareTrace.value
  return {
    url: state.url,
    status: state.status,
    fetched_at: state.fetched_at,
    error: state.error,
    raw: state.raw,
    ...state.values,
  }
}

function normalizeCloudflareTrace(value: any): CloudflareTraceState {
  const rawValues = value && typeof value === 'object' ? value : {}
  const values: Record<string, string> = {}
  for (const [key, raw] of Object.entries(rawValues)) {
    if (['url', 'status', 'fetched_at', 'error', 'raw'].includes(key)) continue
    if (raw != null && String(raw).trim()) values[key] = String(raw).trim()
  }
  const raw = typeof rawValues.raw === 'string' ? rawValues.raw.trim() : ''
  const parsed = raw ? parseCloudflareTrace(raw) : {}
  const status = traceStatus(rawValues.status)
  const hasData = raw || Object.keys(values).length > 0 || typeof rawValues.error === 'string'
  return {
    status: status === 'idle' && hasData ? 'done' : status,
    url: typeof rawValues.url === 'string' && rawValues.url.trim() ? rawValues.url.trim() : cloudflareTraceURL,
    raw,
    values: { ...parsed, ...values },
    error: typeof rawValues.error === 'string' ? rawValues.error.trim() : '',
    fetched_at: typeof rawValues.fetched_at === 'string' ? rawValues.fetched_at.trim() : '',
  }
}

function hasCloudflareTrace(value: any) {
  if (!value || typeof value !== 'object') return false
  return Object.values(value).some((item) => item != null && String(item).trim() !== '')
}

function traceStatus(value: unknown): TraceStatus {
  return value === 'loading' || value === 'done' || value === 'failed' ? value : 'idle'
}

function traceStatusText(status: TraceStatus) {
  if (status === 'loading') return '获取中'
  if (status === 'done') return '已获取'
  if (status === 'failed') return '失败'
  return '待获取'
}

function traceRows(trace: CloudflareTraceState) {
  const keys = orderedTraceKeys(trace.values)
  const rows = [{ key: 'url', label: '来源', value: trace.url }]
  if (trace.fetched_at) rows.push({ key: 'fetched_at', label: '获取时间', value: formatDate(trace.fetched_at) })
  for (const key of keys) {
    rows.push({ key, label: traceLabel(key), value: trace.values[key] })
  }
  return rows.filter((row) => row.value)
}

function orderedTraceKeys(values: Record<string, string>): string[] {
  const order = ['ip', 'colo', 'loc', 'http', 'tls', 'warp', 'gateway', 'visit_scheme', 'h', 'ts', 'fl', 'sni', 'kex', 'sliver', 'rbi', 'uag']
  const known = order.filter((key) => values[key])
  const rest = Object.keys(values).filter((key) => !order.includes(key)).sort()
  return [...known, ...rest]
}

function traceLabel(key: string) {
  const labels: Record<string, string> = {
    ip: '客户端 IP',
    colo: 'CF 机房',
    loc: '地区',
    http: 'HTTP',
    tls: 'TLS',
    warp: 'WARP',
    gateway: 'Gateway',
    visit_scheme: '访问协议',
    h: 'Host',
    ts: 'Trace 时间',
    fl: 'FL',
    sni: 'SNI',
    kex: 'KEX',
    sliver: 'Sliver',
    rbi: 'RBI',
    uag: 'User-Agent',
  }
  return labels[key] || key
}

function normalizeEntrypoints(items: EntryPoint[]): EntryPoint[] {
  return items.map((item) => ({
    ...item,
    id: item.endpoint_public_id || item.id,
    name: item.display_name || item.name,
    description: item.description || '系统入口',
  }))
}

function orderedEntrypoints(items: EntryPoint[]): EntryPoint[] {
  return items
    .map((endpoint, index) => ({ endpoint, index }))
    .sort((a, b) => {
      const aOrder = typeof a.endpoint.display_order === 'number' && Number.isFinite(a.endpoint.display_order) ? a.endpoint.display_order : null
      const bOrder = typeof b.endpoint.display_order === 'number' && Number.isFinite(b.endpoint.display_order) ? b.endpoint.display_order : null
      if (aOrder != null && bOrder != null && aOrder !== bOrder) return aOrder - bOrder
      if (aOrder != null && bOrder == null) return -1
      if (aOrder == null && bOrder != null) return 1
      return a.index - b.index
    })
    .map((item) => item.endpoint)
}

function buildCustomEndpoint(rawURL: string, rawName: string, publicPath: string): EntryPoint {
  const raw = rawURL.trim()
  if (!raw) throw new Error('请输入自定义端点地址')
  const input = raw.includes('://') ? raw : `https://${raw}`
  const parsed = new URL(input)
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') throw new Error('自定义端点只支持 http 或 https')
  parsed.search = ''
  parsed.hash = ''
  const cleanPublicPath = publicPath.startsWith('/') ? publicPath : `/${publicPath}`
  const path = parsed.pathname.replace(/\/+$/, '')
  if (path === '' || path === '/') {
    parsed.pathname = cleanPublicPath
  } else if (!path.endsWith(cleanPublicPath)) {
    parsed.pathname = `${path}${cleanPublicPath}`.replace(/\/+/g, '/')
  }
  const probeBaseURL = parsed.toString().replace(/\/+$/, '')
  const displayURL = stripPublicPath(probeBaseURL, cleanPublicPath)
  const id = `custom_${stableHash(probeBaseURL)}`
  const name = safeCustomName(rawName) || `自定义入口 ${customEntrypoints.value.length + 1}`
  return {
    id,
    endpoint_public_id: id,
    name,
    display_name: name,
    description: '自定义入口 · 仅本次浏览器测试',
    display_url: displayURL,
    probe_base_url: probeBaseURL,
    source: 'custom',
    capabilities: ['ping', 'blob', 'upload', 'stream'],
  }
}

function stripPublicPath(rawURL: string, publicPath: string): string {
  const parsed = new URL(rawURL)
  const cleanPublicPath = publicPath.startsWith('/') ? publicPath : `/${publicPath}`
  const normalizedPath = parsed.pathname.replace(/\/+$/, '')
  if (normalizedPath === cleanPublicPath || normalizedPath.endsWith(cleanPublicPath)) {
    parsed.pathname = normalizedPath.slice(0, -cleanPublicPath.length) || '/'
  }
  return parsed.toString().replace(/\/+$/, '')
}

function endpointLabels(): Record<string, string> {
  const labels: Record<string, string> = {}
  for (const endpoint of entrypoints.value) {
    labels[endpoint.endpoint_public_id || endpoint.id] = displayName(endpoint)
  }
  return labels
}

function customEndpointPayload() {
  const selected = new Set(selectedIds.value)
  return customEntrypoints.value
    .filter((endpoint) => selected.has(endpoint.id))
    .map((endpoint) => ({
      endpoint_public_id: endpoint.endpoint_public_id || endpoint.id,
      display_name: displayName(endpoint),
      probe_base_url: endpoint.probe_base_url || '',
    }))
}

function endpointNetInfoPayload() {
  const out: Record<string, { origin_peer?: IPInfo; dns_records?: IPInfo[] }> = {}
  for (const endpoint of entrypoints.value) {
    const id = endpoint.endpoint_public_id || endpoint.id
    const result = results.value.find((item) => item.endpoint_public_id === id || item.endpoint_id === endpoint.id)
    out[id] = {
      origin_peer: result?.netinfo?.origin_peer,
      dns_records: result?.netinfo?.dns_records || endpoint.dns_records || [],
    }
  }
  return out
}

function allSamples(): DiagnoseProgressEvent[] {
  return Object.values(runStates.value).flatMap((state) => state.samples)
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
  return { status: 'idle', current: '待开始', logs: [], samples: [], metrics: emptyMetrics() }
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
    const status = event.ok ? '成功' : '失败'
    const speed = event.mbps != null ? ` · ${formatMbps(event.mbps)}` : ''
    const latency = event.ttft_ms != null ? ` · TTFT ${formatMs(event.ttft_ms)}` : event.ttfb_ms != null ? ` · TTFB ${formatMs(event.ttfb_ms)}` : ''
    const origin = event.origin_peer?.ip ? ` · 回源 ${formatIPInfo(event.origin_peer)}` : ''
    return {
      ...state,
      current: `${event.label} (${event.sample_index}/${event.sample_total})`,
      samples,
      metrics: summarizeSamples(samples),
      logs: [`${event.label} ${status}${latency}${speed}${origin}`, ...state.logs].slice(0, 7),
    }
  })
}

function summarizeSamples(samples: DiagnoseProgressEvent[]): LiveMetrics {
  const ping = samples.filter((sample) => sample.kind === 'origin_ping')
  const pingOK = ping.filter((sample) => sample.ok)
  const ttfb = samples.filter((sample) => sample.ttfb_ms != null && sample.ok).map((sample) => sample.ttfb_ms as number)
  const ttft = samples.filter((sample) => sample.ttft_ms != null && sample.ok).map((sample) => sample.ttft_ms as number)
  return {
    successRate: samples.length > 0 ? samples.filter((sample) => sample.ok).length / samples.length : null,
    pingSuccessRate: ping.length > 0 ? pingOK.length / ping.length : null,
    avgPing: averageMetric(pingOK.map((sample) => sample.duration_ms ?? null)),
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
  for (const size of sizeLabels.value) out[size] = speedByKind(samples, kind, size)
  return out
}

function speedByKind(samples: DiagnoseProgressEvent[], kind: 'download' | 'upload', size: string): number | null {
  const values = samples.filter((sample) => sample.kind === kind && sample.size === size && sample.mbps != null && sample.ok).map((sample) => sample.mbps as number)
  return averageMetric(values)
}

function legacySizeMap(small: number | null, large: number | null): Record<string, number | null> {
  const sizes = sizeLabels.value
  return { [sizes[0] || '64k']: small, [sizes[sizes.length - 1] || '20m']: large }
}

function metricBySize(values: Record<string, number | null>, size: string): number | null {
  return values[size] ?? null
}

function averageMetric(values: Array<number | null | undefined>): number | null {
  const ok = values.filter((value): value is number => value != null && Number.isFinite(value))
  if (ok.length === 0) return null
  return Number((ok.reduce((sum, item) => sum + item, 0) / ok.length).toFixed(2))
}

function displayName(endpoint: EntryPoint): string {
  return endpoint.display_name || endpoint.name || endpoint.id
}

function endpointURL(endpoint: EntryPoint): string {
  return endpoint.display_url || endpoint.base_url || endpoint.raw_value || endpoint.origin || endpoint.host || '-'
}

function statusText(status: RunStatus) {
  if (status === 'running') return '测试中'
  if (status === 'done') return '完成'
  if (status === 'failed') return '失败'
  return '待开始'
}

function levelText(level: string | undefined) {
  if (level === 'good') return '正常'
  if (level === 'warning') return '波动'
  if (level === 'bad') return '异常'
  return level || '-'
}

function formatDate(value: string | number | Date | undefined) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return String(value)
  return date.toLocaleString()
}

function formatMs(value: number | null | undefined) {
  return value == null ? '-' : `${Math.round(value)} ms`
}

function formatMbps(value: number | null | undefined) {
  return value == null ? '-' : `${Number(value).toFixed(2)} Mbps`
}

function formatIPInfo(value: IPInfo | null | undefined) {
  if (!value?.ip) return '-'
  const asn = value.asn ? `AS${value.asn}` : ''
  const name = value.as_name ? ` ${value.as_name}` : ''
  return `${value.ip}${asn ? ` (${asn}${name})` : ''}`
}

function endpointDNS(endpoint: EntryPoint, result?: EndpointResult) {
  return result?.netinfo?.dns_records || endpoint.dns_records || []
}

function endpointOriginPeer(result?: EndpointResult) {
  return result?.netinfo?.origin_peer
}

function progressPercent(row: { state: EndpointRunState }) {
  const first = row.state.samples[0]
  if (!first?.sample_total) return 0
  return Math.min(100, Math.round((row.state.samples.length / first.sample_total) * 100))
}

function visibleIPs(values: IPInfo[] | null | undefined, limit = 4) {
  return (values || []).slice(0, limit)
}

function hiddenIPCount(values: IPInfo[] | null | undefined, limit = 4) {
  return Math.max(0, (values?.length || 0) - limit)
}

function asnText(value: IPInfo | null | undefined) {
  if (!value?.asn) return '-'
  return `AS${value.asn}${value.as_name ? ` · ${value.as_name}` : ''}`
}

function pct(value: number | null | undefined) {
  return value == null ? '-' : `${Math.round(value * 100)}%`
}

function normalizeSizes(sizes: string[]): string[] {
  const out = Array.from(new Set(sizes.map((item) => item.trim().toLowerCase()).filter(Boolean)))
  return out.length > 0 ? out : ['64k', '1m', '5m', '20m']
}

function safeCustomName(value: string): string {
  const trimmed = value.trim()
  if (!trimmed || trimmed.includes('://')) return ''
  return trimmed.slice(0, 32)
}

function stableHash(value: string): string {
  let hash = 0
  for (let i = 0; i < value.length; i += 1) hash = ((hash << 5) - hash + value.charCodeAt(i)) | 0
  return Math.abs(hash).toString(36)
}

function applyTheme(theme: string) {
  const normalized = theme.toLowerCase()
  if (normalized === 'dark' || normalized === 'light') {
    document.documentElement.dataset.theme = normalized
  }
}
</script>

<template>
  <main class="page" :class="`view-${viewMode}`">
    <section v-if="loading" class="state">加载中</section>

    <section v-else-if="isReportPage" class="stack">
      <header class="hero compact">
        <div>
          <span class="eyebrow">Customer report</span>
          <h1>诊断报告 {{ reportSupportRef.short_code || report.report_id || '' }}</h1>
          <p>客户可见的脱敏诊断数据</p>
        </div>
        <div :class="['score-ring', reportSummary.level]">
          <strong>{{ reportSummary.score ?? '-' }}</strong>
          <span>{{ levelText(reportSummary.level) }}</span>
        </div>
      </header>

      <section class="summary">
        <div>
          <span class="label">报告编号</span>
          <strong>{{ report.report_id || reportSupportRef.report_id || '-' }}</strong>
        </div>
        <div>
          <span class="label">生成时间</span>
          <strong>{{ formatDate(report.created_at) }}</strong>
        </div>
        <div>
          <span class="label">语言/时区</span>
          <strong>{{ reportClientEnv.language || '-' }} / {{ reportClientEnv.timezone || '-' }}</strong>
        </div>
      </section>

      <section v-if="reportCloudflareTraceVisible" class="panel trace-panel">
        <div class="panel-head">
          <h2>Cloudflare Trace</h2>
          <span>{{ traceStatusText(reportCloudflareTrace.status) }}</span>
        </div>
        <div class="trace-grid">
          <div v-for="item in traceRows(reportCloudflareTrace)" :key="`report-trace-${item.key}`" :class="{ wide: item.key === 'url' || item.key === 'uag' }">
            <span>{{ item.label }}</span>
            <strong class="mono">{{ item.value }}</strong>
          </div>
        </div>
        <p v-if="reportCloudflareTrace.error" class="trace-error">{{ reportCloudflareTrace.error }}</p>
      </section>

      <section class="panel endpoint-detail-panel">
        <div class="panel-head">
          <h2>端点详情</h2>
          <span>{{ reportEntrypoints.length }} 个入口</span>
        </div>
        <div class="endpoint-detail-grid">
          <article v-for="ep in reportEntrypoints" :key="`report-detail-${ep.endpoint_public_id}`" class="endpoint-detail-card report-detail-card">
            <header>
              <div>
                <h2>{{ ep.display_name }}</h2>
                <p>{{ ep.endpoint_public_id }}</p>
              </div>
              <span :class="['badge', ep.level]">{{ levelText(ep.level) }}</span>
            </header>

            <div class="metric-grid endpoint-metrics report-metrics">
              <div><span class="label">成功率</span><strong>{{ pct(ep.success_rate) }}</strong></div>
              <div><span class="label">HTTP 失败率</span><strong>{{ pct(ep.http_loss_rate) }}</strong></div>
              <div><span class="label">超时率</span><strong>{{ pct(ep.timeout_rate) }}</strong></div>
              <div><span class="label">p50 / p95</span><strong>{{ formatMs(ep.latency_p50_ms) }} / {{ formatMs(ep.latency_p95_ms) }}</strong></div>
              <div><span class="label">TTFB p95</span><strong>{{ formatMs(ep.ttfb_p95_ms) }}</strong></div>
              <div><span class="label">下载 / 上传</span><strong>{{ formatMbps(ep.download_mbps) }} / {{ formatMbps(ep.upload_mbps) }}</strong></div>
              <div><span class="label">流式首事件</span><strong>{{ formatMs(ep.stream_first_event_ms) }}</strong></div>
              <div><span class="label">CORS / Timing</span><strong>{{ ep.cors_ok ? '正常' : '异常' }} / {{ ep.timing_detail_available ? '可用' : '不可用' }}</strong></div>
            </div>

            <div class="path-section dns-section">
              <span class="label">端点 DNS</span>
              <div v-if="ep.endpoint_dns?.length" class="ip-list">
                <span v-for="ip in visibleIPs(ep.endpoint_dns)" :key="`${ep.endpoint_public_id}-dns-${ip.ip}`" class="ip-chip">
                  <strong>{{ ip.ip }}</strong>
                  <small>{{ asnText(ip) }}</small>
                </span>
                <span v-if="hiddenIPCount(ep.endpoint_dns)" class="ip-chip muted">+{{ hiddenIPCount(ep.endpoint_dns) }}</span>
              </div>
              <strong v-else>-</strong>
            </div>

            <div class="path-section origin-section">
              <span class="label">源站回源 IP</span>
              <div v-if="ep.origin_peer?.ip" class="ip-list">
                <span class="ip-chip accent">
                  <strong>{{ ep.origin_peer.ip }}</strong>
                  <small>{{ asnText(ep.origin_peer) }}</small>
                </span>
              </div>
              <strong v-else>-</strong>
            </div>
          </article>
        </div>
      </section>
      <p v-if="error" class="error">{{ error }}</p>
    </section>

    <section v-else-if="isAdminPage" class="stack">
      <header class="hero compact">
        <div>
          <span class="eyebrow">Admin console</span>
          <h1>管理员排障</h1>
          <p>{{ adminTotal }} 份报告 · {{ adminInventory?.valid_count ?? 0 }} 个有效入口 · 内部信息仅管理员可见</p>
        </div>
        <button class="primary" @click="refreshAdminReports">刷新</button>
      </header>

      <form class="filter-bar" @submit.prevent="refreshAdminReports">
        <input v-model="adminReportIdFilter" placeholder="报告 ID">
        <input v-model="adminUserFilter" placeholder="用户 ID">
        <select v-model="adminLevelFilter">
          <option value="">全部等级</option>
          <option value="good">正常</option>
          <option value="warning">波动</option>
          <option value="bad">异常</option>
          <option value="legacy">历史</option>
        </select>
        <button class="primary" type="submit">查询</button>
      </form>
      <p v-if="error" class="error">{{ error }}</p>

      <section class="admin-layout">
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>报告</th>
                <th>时间</th>
                <th>用户</th>
                <th>等级</th>
                <th>分数</th>
                <th>问题代码</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in adminReports" :key="item.report_id">
                <td class="mono">{{ item.report_id }}</td>
                <td>{{ formatDate(item.created_at) }}</td>
                <td class="mono">{{ item.user_id || '-' }}</td>
                <td><span :class="['badge', item.level]">{{ levelText(item.level) }}</span></td>
                <td>{{ item.score }}</td>
                <td>{{ (item.problem_codes || []).join(', ') || '-' }}</td>
                <td><button @click="openAdminReport(item.report_id)">查看</button></td>
              </tr>
            </tbody>
          </table>
        </div>

        <aside class="panel inventory-panel">
          <div class="panel-head">
            <h2>入口 Inventory</h2>
            <span>{{ adminInventory?.source || '-' }}</span>
          </div>
          <div class="kv-grid">
            <div><span>public_path</span><strong>{{ adminInventory?.public_path || '-' }}</strong></div>
            <div><span>有效入口</span><strong>{{ adminInventory?.valid_count ?? '-' }}</strong></div>
            <div><span>过滤入口</span><strong>{{ adminInventory?.filtered_count ?? '-' }}</strong></div>
          </div>
        </aside>
      </section>

      <section v-if="adminReportDetail" class="admin-detail">
        <div class="panel">
          <div class="panel-head">
            <h2>客户视图摘要</h2>
            <span>{{ adminReportDetail.report_id }}</span>
          </div>
          <div class="summary">
            <div><span class="label">等级/分数</span><strong>{{ levelText(adminCustomerReport.summary?.level) }} / {{ adminCustomerReport.summary?.score ?? '-' }}</strong></div>
            <div><span class="label">报告时间</span><strong>{{ formatDate(adminCustomerReport.created_at) }}</strong></div>
            <div><span class="label">分享状态</span><strong>{{ adminReportDetail.customer_share_enabled ? '开启' : '关闭' }}</strong></div>
          </div>
        </div>

        <div class="panel">
          <div class="panel-head">
            <h2>原始用户上下文</h2>
            <span>admin only</span>
          </div>
          <div class="kv-grid">
            <div><span>user_id</span><strong>{{ adminOwnerSession.user_id || adminInternalReport.owner_user_id || '-' }}</strong></div>
            <div><span>username</span><strong>{{ adminOwnerSession.username || '-' }}</strong></div>
            <div><span>session</span><strong class="mono">{{ adminOwnerSession.session_id || '-' }}</strong></div>
            <div><span>src_host</span><strong>{{ adminOwnerSession.src_host || '-' }}</strong></div>
            <div><span>src_url</span><strong>{{ adminOwnerSession.src_url || '-' }}</strong></div>
            <div><span>theme/lang</span><strong>{{ adminOwnerSession.theme || '-' }} / {{ adminOwnerSession.lang || '-' }}</strong></div>
          </div>
        </div>

        <div class="panel">
          <div class="panel-head">
            <h2>内部入口明细</h2>
            <span>{{ adminInternalEntrypoints.length }} 个</span>
          </div>
          <div class="endpoint-results">
            <article v-for="ep in adminInternalEntrypoints" :key="ep.endpoint_public_id" class="result-card internal">
              <header>
                <div>
                  <h2>{{ ep.name }}</h2>
                  <p class="mono">{{ ep.endpoint_public_id }}</p>
                </div>
                <span class="badge">{{ ep.source || '-' }}</span>
              </header>
              <div class="kv-grid">
                <div><span>base_url</span><strong>{{ ep.base_url || '-' }}</strong></div>
                <div><span>lg_base_url</span><strong>{{ ep.lg_base_url || '-' }}</strong></div>
                <div><span>diag refs</span><strong>{{ (ep.diag_event_refs || []).join(', ') || '-' }}</strong></div>
                <div><span>score</span><strong>{{ ep.browser_metrics?.score ?? '-' }}</strong></div>
              </div>
            </article>
          </div>
        </div>

        <div class="panel">
          <div class="panel-head">
            <h2>诊断结论</h2>
            <span>{{ adminDiagnosis.length }} 条</span>
          </div>
          <div class="diagnosis-list">
            <div v-for="item in adminDiagnosis" :key="item.code">
              <strong>{{ item.code }}</strong>
              <span>{{ item.severity }} · {{ item.operator_hint }}</span>
            </div>
            <div v-if="adminDiagnosis.length === 0"><strong>无</strong><span>没有内部诊断结论</span></div>
          </div>
        </div>

        <div class="panel">
          <div class="panel-head">
            <h2>Raw JSON</h2>
            <button @click="adminRawOpen = !adminRawOpen">{{ adminRawOpen ? '隐藏' : '查看' }}</button>
          </div>
          <pre v-if="adminRawOpen" class="json">{{ JSON.stringify(adminReportDetail, null, 2) }}</pre>
        </div>
      </section>
    </section>

    <section v-else class="stack customer-stack">
      <header class="hero customer-hero">
        <div>
          <span class="eyebrow">Looking glass</span>
          <h1>入口访问诊断</h1>
          <p>{{ entrypoints.length }} 个入口 · 已选 {{ selectedEndpoints.length }} 个 · {{ boot?.entrypoint_source || '未获取' }}</p>
        </div>
        <div class="actions">
          <button :disabled="running" @click="selectAllEndpoints">全选</button>
          <button :disabled="running" @click="clearSelection">清空</button>
          <button class="primary" :disabled="running || selectedEndpoints.length === 0" @click="run">{{ running ? '测试中' : '开始诊断' }}</button>
        </div>
      </header>

      <section class="panel control-panel">
        <div class="control-row">
          <form class="custom-form compact" @submit.prevent="addCustomEndpoint">
            <input v-model="customName" :disabled="running" placeholder="自定义名称">
            <input v-model="customURL" :disabled="running" placeholder="https://example.com/lg">
            <button :disabled="running" type="submit">添加</button>
          </form>
          <div v-if="running" class="progress compact-progress">正在测试：{{ progress }}</div>
          <div v-else class="state compact-state">{{ results.length ? '诊断完成' : '待开始' }}</div>
        </div>

        <div class="endpoint-picker">
          <label v-for="row in rows" :key="row.endpoint.id" :class="['endpoint-choice', { selected: row.selected }]">
            <input type="checkbox" :checked="row.selected" :disabled="running" @change="toggleEndpoint(row.endpoint.id, $event)">
            <span>
              <strong>{{ displayName(row.endpoint) }}</strong>
              <small>{{ row.endpoint.description || '浏览器诊断入口' }}</small>
              <small class="endpoint-url">{{ endpointURL(row.endpoint) }}</small>
            </span>
            <span :class="['run-status', row.state.status]">{{ statusText(row.state.status) }}</span>
            <button v-if="row.endpoint.source === 'custom'" class="ghost small" :disabled="running" type="button" @click.prevent.stop="removeCustomEndpoint(row.endpoint.id)">移除</button>
          </label>
        </div>
      </section>

      <p v-if="error" class="error">{{ error }}</p>

      <section class="panel trace-panel">
        <div class="panel-head">
          <h2>Cloudflare Trace</h2>
          <span>{{ traceStatusText(cloudflareTrace.status) }}</span>
        </div>
        <div class="trace-grid">
          <div v-for="item in traceRows(cloudflareTrace)" :key="`live-trace-${item.key}`" :class="{ wide: item.key === 'url' || item.key === 'uag' }">
            <span>{{ item.label }}</span>
            <strong class="mono">{{ item.value }}</strong>
          </div>
        </div>
        <p v-if="cloudflareTrace.error" class="trace-error">{{ cloudflareTrace.error }}</p>
        <p v-else-if="traceRows(cloudflareTrace).length === 0" class="trace-empty">等待 Cloudflare 返回 trace</p>
      </section>

      <section class="panel endpoint-detail-panel">
        <div class="panel-head">
          <h2>端点详情</h2>
          <span>{{ selectedRows.length }} 个入口</span>
        </div>
        <div class="endpoint-detail-grid">
          <article v-for="row in selectedRows" :key="`endpoint-detail-${row.endpoint.id}`" class="endpoint-detail-card">
            <header>
              <div>
                <h2>{{ displayName(row.endpoint) }}</h2>
                <p>{{ row.endpoint.description || '浏览器诊断入口' }}</p>
                <code>{{ endpointURL(row.endpoint) }}</code>
              </div>
              <span :class="['run-status', row.state.status]">{{ statusText(row.state.status) }}</span>
            </header>

            <div class="endpoint-progress">
              <div>
                <span class="label">当前进度</span>
                <strong>{{ row.state.current }}</strong>
              </div>
              <div class="progress-track"><span :style="{ width: `${progressPercent(row)}%` }"></span></div>
            </div>

            <div class="metric-grid endpoint-metrics">
              <div><span class="label">完整成功率</span><strong>{{ pct(row.state.metrics.successRate) }}</strong></div>
              <div><span class="label">Ping 成功率</span><strong>{{ pct(row.state.metrics.pingSuccessRate) }}</strong></div>
              <div><span class="label">平均 Ping</span><strong>{{ formatMs(row.state.metrics.avgPing) }}</strong></div>
              <div><span class="label">平均 TTFB</span><strong>{{ formatMs(row.state.metrics.avgTTFB) }}</strong></div>
              <div><span class="label">平均 TTFT</span><strong>{{ formatMs(row.state.metrics.avgTTFT) }}</strong></div>
              <div v-for="size in sizeLabels" :key="`${row.endpoint.id}-detail-download-${size}`"><span class="label">下载 {{ size }}</span><strong>{{ formatMbps(metricBySize(row.state.metrics.downloadBySize, size)) }}</strong></div>
              <div><span class="label">上传 {{ largestSize }}</span><strong>{{ formatMbps(metricBySize(row.state.metrics.uploadBySize, largestSize)) }}</strong></div>
            </div>

            <div class="path-section dns-section">
              <span class="label">端点 DNS</span>
              <div v-if="endpointDNS(row.endpoint, row.result).length" class="ip-list">
                <span v-for="ip in visibleIPs(endpointDNS(row.endpoint, row.result))" :key="`${row.endpoint.id}-dns-${ip.ip}`" class="ip-chip">
                  <strong>{{ ip.ip }}</strong>
                  <small>{{ asnText(ip) }}</small>
                </span>
                <span v-if="hiddenIPCount(endpointDNS(row.endpoint, row.result))" class="ip-chip muted">+{{ hiddenIPCount(endpointDNS(row.endpoint, row.result)) }}</span>
              </div>
              <strong v-else>-</strong>
            </div>

            <div class="path-section origin-section">
              <span class="label">源站回源 IP</span>
              <div v-if="endpointOriginPeer(row.result)?.ip" class="ip-list">
                <span class="ip-chip accent">
                  <strong>{{ endpointOriginPeer(row.result)?.ip }}</strong>
                  <small>{{ asnText(endpointOriginPeer(row.result)) }}</small>
                </span>
              </div>
              <strong v-else>-</strong>
            </div>
          </article>
        </div>
      </section>

      <section v-if="shareURL" class="report-line">
        <span>报告：{{ reportId }}</span>
        <a :href="shareURL" target="_blank" rel="noreferrer">打开报告</a>
      </section>
    </section>
  </main>
</template>
