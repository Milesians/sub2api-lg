<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { bootstrap, getEntrypoints, getReport, iframeContext, submitReport } from './api/client'
import { buildReport, diagnoseEndpoint } from './diagnose/runner'
import type { BootstrapResponse, EndpointResult, EntryPoint } from './types'

const loading = ref(true)
const running = ref(false)
const error = ref('')
const boot = ref<BootstrapResponse | null>(null)
const entrypoints = ref<EntryPoint[]>([])
const results = ref<EndpointResult[]>([])
const reportId = ref('')
const shareURL = ref('')
const reportJSON = ref<unknown>(null)
const progress = ref('')

const isReportPage = computed(() => window.location.pathname.includes('/report/'))
const token = computed(() => boot.value?.session_token || sessionStorage.getItem('sub2api_lg_session_token') || '')
const best = computed(() => [...results.value].sort((a, b) => b.browser.success_rate - a.browser.success_rate)[0])
const rows = computed(() => entrypoints.value.map((endpoint) => ({
  endpoint,
  result: results.value.find((item) => item.endpoint_id === endpoint.id),
})))

onMounted(async () => {
  try {
    if (isReportPage.value) {
      await loadReportPage()
      return
    }
    boot.value = await bootstrap()
    entrypoints.value = boot.value.entrypoints || []
    if (entrypoints.value.length > 0) {
      await run()
    }
  } catch (e) {
    error.value = String((e as Error)?.message || e)
  } finally {
    loading.value = false
  }
})

async function refreshEntrypoints() {
  if (!token.value) return
  const snapshot = await getEntrypoints(token.value, true)
  entrypoints.value = snapshot.entrypoints || []
}

async function run() {
  if (!boot.value || running.value) return
  running.value = true
  error.value = ''
  results.value = []
  reportId.value = ''
  shareURL.value = ''
  try {
    for (const endpoint of entrypoints.value) {
      progress.value = endpoint.name
      const result = await diagnoseEndpoint(endpoint, boot.value.probe)
      results.value.push(result)
    }
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

async function loadReportPage() {
  const injected = (window as any).__SUB2API_LG_REPORT__
  if (injected) {
    reportJSON.value = injected
    return
  }
  const id = window.location.pathname.split('/report/')[1]?.split('/')[0]
  if (!id) throw new Error('report id missing')
  if (!token.value) throw new Error('当前报告 JSON 需要诊断会话授权访问')
  reportJSON.value = await getReport(id, token.value)
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
          <p>{{ entrypoints.length }} 个入口 · {{ boot?.entrypoint_source || '未获取' }}</p>
        </div>
        <div class="actions">
          <button :disabled="running" @click="refreshEntrypoints">刷新端点</button>
          <button class="primary" :disabled="running || entrypoints.length === 0" @click="run">
            {{ running ? '诊断中' : '开始诊断' }}
          </button>
        </div>
      </header>

      <div v-if="running" class="progress">正在测试：{{ progress }}</div>
      <p v-if="error" class="error">{{ error }}</p>

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

      <section class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>入口名称</th>
              <th>Base URL</th>
              <th>诊断 URL</th>
              <th>状态</th>
              <th>成功率</th>
              <th>HTTP 失败率 / 业务层丢包率</th>
              <th>p50 总耗时</th>
              <th>p95 总耗时</th>
              <th>p50 首包</th>
              <th>p95 首包</th>
              <th>下载速度</th>
              <th>流式首事件</th>
              <th>最大事件间隔</th>
              <th>疑似缓冲</th>
              <th>CORS</th>
              <th>Timing</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in rows" :key="row.endpoint.id">
              <td>{{ row.endpoint.name }}</td>
              <td class="url">{{ row.endpoint.base_url }}</td>
              <td class="url">{{ row.endpoint.lg_base_url }}</td>
              <template v-if="row.result">
                <td><span :class="['badge', row.result.level]">{{ row.result.level }}</span></td>
                <td>{{ Math.round(row.result.browser.success_rate * 100) }}%</td>
                <td>{{ Math.round(row.result.browser.http_loss_rate * 100) }}%</td>
                <td>{{ row.result.browser.p50_duration_ms ?? '-' }} ms</td>
                <td>{{ row.result.browser.p95_duration_ms ?? '-' }} ms</td>
                <td>{{ row.result.browser.p50_ttfb_ms ?? '-' }} ms</td>
                <td>{{ row.result.browser.p95_ttfb_ms ?? '-' }} ms</td>
                <td>{{ row.result.browser.download_mbps ?? '-' }} Mbps</td>
                <td>{{ row.result.browser.first_event_ms ?? '-' }} ms</td>
                <td>{{ row.result.browser.max_chunk_gap_ms ?? '-' }} ms</td>
                <td>{{ row.result.browser.stream_buffered ? '是' : '否' }}</td>
                <td>{{ row.result.browser.cors_blocked ? '异常' : '正常' }}</td>
                <td>{{ row.result.browser.timing_detail_available ? '可用' : '受限' }}</td>
              </template>
              <template v-else>
                <td colspan="13">待测试</td>
              </template>
            </tr>
          </tbody>
        </table>
      </section>

      <section v-if="shareURL" class="report-line">
        <span>报告：{{ reportId }}</span>
        <a :href="shareURL" target="_blank" rel="noreferrer">打开报告</a>
      </section>
    </section>
  </main>
</template>
