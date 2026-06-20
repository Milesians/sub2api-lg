export interface BootstrapResponse {
  session_id: string
  session_token: string
  user: { id?: string; username?: string; email?: string; role?: string; is_admin?: boolean; isAdmin?: boolean; admin?: boolean }
  app: { public_path: string; iframe_origin: string; theme?: string; lang?: string }
  probe: ProbeConfig
  entrypoint_count: number
  entrypoints: EntryPoint[]
  entrypoint_source: string
}

export interface ProbeConfig {
  browser_repeat: number
  browser_timeout_ms: number
  paths: {
    ping: string
    blob: string
    upload: string
    stream: string
  }
  blob_sizes: string[]
  stream: {
    events: number
    interval_ms: number
    bytes: number
  }
}

export interface EntryPoint {
  id: string
  source: string
  name: string
  description: string
  raw_value: string
  base_url: string
  public_path: string
  lg_base_url: string
  origin: string
  host: string
  scheme: string
  enabled: boolean
}

export interface BrowserSummary {
  success_rate: number
  ping_success_rate?: number
  http_loss_rate: number
  p50_duration_ms: number | null
  p95_duration_ms: number | null
  p50_ttfb_ms: number | null
  p95_ttfb_ms: number | null
  avg_ping_ms: number | null
  avg_ttfb_ms: number | null
  avg_ttft_ms: number | null
  jitter_ms: number | null
  timeout_rate: number
  download_mbps: number | null
  upload_mbps: number | null
  download_mbps_by_size?: Record<string, number | null>
  upload_mbps_by_size?: Record<string, number | null>
  download_small_mbps: number | null
  download_large_mbps: number | null
  upload_small_mbps: number | null
  upload_large_mbps: number | null
  first_event_ms: number | null
  max_chunk_gap_ms: number | null
  stream_buffered: boolean
  cors_blocked: boolean
  timing_detail_available: boolean
}

export interface EndpointResult {
  endpoint_id: string
  name: string
  base_url: string
  lg_base_url: string
  browser: BrowserSummary
  level: 'good' | 'warning' | 'bad'
  recommendation: string
}
