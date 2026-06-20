# sub2api 源站入口性能诊断 iframe 方案

版本：v1.1  
日期：2026-06-20  
目标读者：后续 Coding Agent、sub2api 后端/前端开发者、部署维护人员

---

## 0. v1.1 关键变更

本版按最新约束调整：

1. **内部统一使用 `base_url`**。  
   sub2api Admin API 的标准字段按 `base_url` 处理，诊断项目在归一化、数据库、前端、报告里都只暴露 `base_url`。也就是说：

   ```text
   Admin 标准字段 data.base_url         ->  诊断系统内部 endpoint.base_url
   custom_endpoints[].base_url          ->  诊断系统内部 endpoint.base_url
   旧字段 data.api_base_url / endpoint   ->  仅在 Admin Client 层兼容读取，不向前端输出
   ```

2. **诊断服务挂在每个入口的 `base_url + public_path` 下**。  
   默认 `public_path = /lg`，也就是：

   ```text
   https://api.example.com/v1      -> base_url
   https://api.example.com/v1/lg   -> 转发到本诊断项目
   ```

   如果你的 `base_url` 不带 `/v1`，则是：

   ```text
   https://api.example.com         -> base_url
   https://api.example.com/lg      -> 转发到本诊断项目
   ```

3. **`/lg` 不能硬编码，必须可配置**。  
   统一使用配置项 `app.public_path`，默认值 `/lg`。后续可改成 `/network-diagnose`、`/__lg`、`/tools/lg` 等。

4. **不再默认使用独立 `lg.example.com` 域名**。  
   推荐部署方式是把每个 API 入口的：

   ```text
   {base_url}{public_path}
   ```

   反向代理到 `sub2api-origin-lg` 容器。

5. **只测试到源站/入口的性能，不测试上游模型**。  
   不调用 `/v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/embeddings`，不产生模型消耗。所谓“首包”指 HTTP TTFB 或诊断 SSE 首事件，不是模型首 token。

---

## 1. 背景与目标

本方案用于给 sub2api 增加一个可嵌入客户后台的 **源站入口性能诊断工具**。它用于判断客户访问 sub2api 各个 API 入口时的网络质量，例如：

```text
客户浏览器 -> 默认 API 入口 -> 源站诊断容器
客户浏览器 -> CDN API 入口 -> 源站诊断容器
客户浏览器 -> 备用 API 入口 -> 源站诊断容器
```

目标不是测试上游模型，而是测试客户到你的入口、CDN、反代、源站链路的质量。

核心目标：

1. 作为 sub2api 自定义菜单 iframe 嵌入客户后台。
2. 接收 sub2api iframe 传入的 `user_id`、`token`、`theme`、`lang`、`ui_mode`、`src_host`、`src_url` 等上下文。
3. 通过 sub2api Admin API 查询当前配置了多少个入口端点。
4. 把 Admin API 返回的默认入口和自定义入口统一归一化为 `base_url`。
5. 对每个 `base_url` 自动拼接可配置的 `public_path`，生成诊断入口 `lg_base_url`。
6. 浏览器侧测试客户到每个 `lg_base_url` 的 HTTP 延迟、TTFB、稳定性、下载速度、业务层失败率。
7. 可选服务端补充探针：由诊断容器自身访问各 `lg_base_url`，用于和客户浏览器侧结果对照。
8. 生成报告，给客户和客服排障使用。

本方案明确不做：

- 不测试 OpenAI、Claude、Gemini、Anthropic、OpenRouter 等上游模型。
- 不调用模型生成接口。
- 不统计模型首 token。
- 不使用用户真实 API Key 发起模型请求。
- 不允许用户输入任意 URL 让后端探测，避免 SSRF。

---

## 2. 项目命名与部署形态

新增独立项目建议命名：

```text
sub2api-origin-lg
```

部署方式：

```text
Docker Compose 单容器
  ├─ Go 后端
  ├─ Vue 3 前端
  ├─ SQLite 本地报告库
  └─ 诊断资源路由 /diag/*
```

推荐技术栈：

```text
后端：Go + Gin 或 Fiber
前端：Vue 3 + Vite + ECharts
存储：SQLite
部署：Docker Compose
```

第一版不需要 Redis、PostgreSQL、ClickHouse。后续要做全局质量大盘时再扩展。

---

## 3. 路由挂载模型

### 3.1 外部可见 URL

诊断项目不要求独立域名，而是挂在每个 API 入口的 `base_url + public_path` 下。

示例一：`base_url` 带 `/v1`

```text
base_url:     https://api.example.com/v1
public_path:  /lg
lg_base_url:  https://api.example.com/v1/lg
```

示例二：`base_url` 不带路径

```text
base_url:     https://api.example.com
public_path:  /lg
lg_base_url:  https://api.example.com/lg
```

示例三：路径自定义

```text
base_url:     https://api.example.com/v1
public_path:  /tools/network
lg_base_url:  https://api.example.com/v1/tools/network
```

### 3.2 路径拼接规则

必须实现一个安全的 URL 拼接函数，不要用字符串直接相加。

规则：

```text
joinURL("https://api.example.com/v1", "/lg")       -> https://api.example.com/v1/lg
joinURL("https://api.example.com/v1/", "/lg")      -> https://api.example.com/v1/lg
joinURL("https://api.example.com", "lg")           -> https://api.example.com/lg
joinURL("https://api.example.com", "/tools/lg")    -> https://api.example.com/tools/lg
```

`public_path` 约束：

```text
必须以 / 开头
不能包含 scheme、host、query、fragment
不能包含 .. 路径穿越
不能是空字符串
默认值：/lg
```

### 3.3 诊断项目对外路由

所有外部路由都挂在：

```text
{base_url}{public_path}
```

下方。假设 `public_path = /lg`，外部路由为：

```text
GET    {base_url}/lg/embed
POST   {base_url}/lg/api/bootstrap
GET    {base_url}/lg/api/entrypoints
POST   {base_url}/lg/api/reports
GET    {base_url}/lg/api/reports/:id
GET    {base_url}/lg/diag/ping
GET    {base_url}/lg/diag/blob
GET    {base_url}/lg/diag/stream
OPTIONS {base_url}/lg/diag/*
```

如果 `public_path = /tools/lg`，则变成：

```text
GET    {base_url}/tools/lg/embed
POST   {base_url}/tools/lg/api/bootstrap
GET    {base_url}/tools/lg/diag/ping
```

Coding Agent 注意：**不要在代码里写死 `/lg`**，所有前端和后端路由生成都必须从配置读取 `public_path`。

---

## 4. 推荐反向代理方式

### 4.1 推荐：外部前缀转发到容器，并 strip prefix

假设：

```text
base_url:    https://api.example.com/v1
public_path: /lg
容器端口:    127.0.0.1:8088
外部路径:    /v1/lg
```

Nginx 示例：

```nginx
location ^~ /v1/lg/ {
    proxy_pass http://127.0.0.1:8088/;
    proxy_http_version 1.1;

    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $host;
    proxy_set_header X-Forwarded-Prefix /v1/lg;

    proxy_buffering off;
    proxy_cache off;
    gzip off;
}
```

这个配置会把：

```text
https://api.example.com/v1/lg/embed
```

转发到容器内部：

```text
http://127.0.0.1:8088/embed
```

这种方式最简单，容器内部不需要知道每个入口的真实外部路径。

### 4.2 兼容：不 strip prefix

如果你的网关不会 strip prefix，则容器内部实际收到：

```text
/v1/lg/embed
```

这时需要设置：

```yaml
app:
  router_prefix: "/v1/lg"
```

但不推荐第一版这样做，因为不同 `base_url` 可能有不同路径前缀，会增加路由复杂度。

### 4.3 多入口代理示例

如果 Admin API 返回三个入口：

```text
https://api.example.com/v1
https://cf-api.example.com/v1
https://origin-api.example.com/v1
```

需要保证三个入口都能访问：

```text
https://api.example.com/v1/lg/diag/ping
https://cf-api.example.com/v1/lg/diag/ping
https://origin-api.example.com/v1/lg/diag/ping
```

这三个地址最终都可以转发到同一个 `sub2api-origin-lg` 容器。浏览器访问不同 host/CDN/反代路径时，就能测试对应入口到源站诊断容器的性能。

---

## 5. sub2api iframe 接入

### 5.1 自定义菜单 URL

sub2api 自定义菜单中配置诊断入口 URL。建议使用默认入口的：

```text
{默认 base_url}{public_path}/embed
```

示例：

```json
{
  "id": "network-diagnose",
  "label": "网络诊断",
  "icon_svg": "<svg>...</svg>",
  "url": "https://api.example.com/v1/lg/embed",
  "visibility": "user",
  "sort_order": 90
}
```

如果 `public_path` 后续改成 `/tools/lg`，则菜单 URL 改为：

```text
https://api.example.com/v1/tools/lg/embed
```

### 5.2 iframe 传入参数

sub2api iframe URL 构造器会附加用户上下文。诊断前端必须兼容：

```text
user_id      当前 sub2api 用户 ID
token        当前 sub2api 前端 JWT，敏感
theme        当前 UI 主题，例如 light / dark / system
lang         当前语言，例如 zh-CN / en-US / ja-JP
ui_mode      embedded
src_host     sub2api 前端宿主 host
src_url      sub2api 当前页面 URL
```

示例：

```text
https://api.example.com/v1/lg/embed
  ?user_id=123
  &token=eyJhbGciOi...
  &theme=dark
  &lang=zh-CN
  &ui_mode=embedded
  &src_host=sub2api.example.com
  &src_url=https%3A%2F%2Fsub2api.example.com%2Fcustom%2Fnetwork-diagnose
```

### 5.3 bootstrap 流程

iframe 加载后：

```text
1. 前端读取 query 参数。
2. 前端调用相对路径 POST ./api/bootstrap。
3. 后端校验 src_host 是否在白名单。
4. 后端使用 token 调用 sub2api 当前用户接口，验证用户身份。
5. 后端创建本地诊断 session。
6. 前端用 history.replaceState 清理 URL 中的 token。
7. 前端使用 session_token 调后续诊断 API。
```

请求：

```http
POST {base_url}{public_path}/api/bootstrap
Content-Type: application/json
```

Body：

```json
{
  "user_id": "123",
  "token": "sub2api_jwt_here",
  "theme": "dark",
  "lang": "zh-CN",
  "ui_mode": "embedded",
  "src_host": "sub2api.example.com",
  "src_url": "https://sub2api.example.com/custom/network-diagnose"
}
```

响应：

```json
{
  "session_id": "sess_xxx",
  "session_token": "diag_session_token_xxx",
  "user": {
    "id": "123",
    "username": "demo",
    "email": "d***@example.com"
  },
  "app": {
    "public_path": "/lg",
    "iframe_origin": "https://api.example.com",
    "theme": "dark",
    "lang": "zh-CN"
  },
  "entrypoints": []
}
```

后续接口统一使用：

```http
Authorization: Bearer {session_token}
```

---

## 6. Admin API 入口发现

### 6.1 数据来源

诊断容器后端通过 sub2api Admin API 获取入口端点：

```http
GET /api/v1/admin/settings HTTP/1.1
Host: sub2api.example.com
x-api-key: ${SUB2API_ADMIN_API_KEY}
Accept: application/json
```

Admin API 原始响应示例：

```json
{
  "code": 0,
  "message": "success",
  "data": {
    "base_url": "https://api.example.com/v1",
    "custom_endpoints": [
      {
        "name": "Cloudflare 入口",
        "base_url": "https://cf-api.example.com/v1",
        "description": "Cloudflare CDN"
      },
      {
        "name": "源站直连",
        "base_url": "https://origin-api.example.com/v1",
        "description": "Origin"
      }
    ]
  }
}
```

兼容字段：

```text
默认入口：
  data.base_url
  data.baseUrl
  data.api_base_url       # 旧字段兼容
  data.apiBaseUrl         # 旧字段兼容

自定义入口：
  data.custom_endpoints[].base_url
  data.customEndpoints[].baseUrl
  data.custom_endpoints[].endpoint      # 旧字段兼容
  data.customEndpoints[].endpoint       # 旧字段兼容
```

但进入本诊断系统后，字段统一叫：

```text
base_url
```

### 6.2 归一化后的入口结构

归一化结构示例：

```json
{
  "id": "abc123def456",
  "source": "admin_default | admin_custom | static_fallback",
  "name": "默认入口",
  "description": "default base_url from admin settings",
  "raw_value": "https://api.example.com/v1/",
  "base_url": "https://api.example.com/v1",
  "public_path": "/lg",
  "lg_base_url": "https://api.example.com/v1/lg",
  "origin": "https://api.example.com",
  "host": "api.example.com",
  "scheme": "https",
  "enabled": true
}
```

注意：这里没有 `api_base_url` 字段。Admin API 新字段应使用 `base_url`；如果遇到旧字段，只在 Admin Client 层兼容，归一化结果仍必须使用 `base_url`。

### 6.3 归一化规则

1. 只允许 `http` 和 `https`。
2. 生产环境默认只允许 `https`，除非 `security.allow_http_endpoints=true`。
3. 去掉 `base_url` 尾部 `/`。
4. `base_url` 允许带路径，例如 `/v1`。
5. `lg_base_url = joinURL(base_url, public_path)`。
6. 对 `base_url` 去重。
7. 端点 ID 建议为 `sha256(base_url)[0:12]`。
8. 默认禁止私网、本地、metadata 地址，除非 `security.allow_private_endpoints=true`。
9. 自定义入口如果缺少名称，使用 host 或 `入口 N`。

需要拦截的地址范围：

```text
127.0.0.0/8
0.0.0.0/8
10.0.0.0/8
172.16.0.0/12
192.168.0.0/16
169.254.0.0/16
::1/128
fc00::/7
fe80::/10
localhost
```

### 6.4 入口数量

入口数量定义：

```text
entrypoint_count = 去重后的有效 base_url 数量
```

例如：

```text
默认入口 1 个 + custom_endpoints 2 个 = 3 个
```

如果默认入口和某个自定义入口的 `base_url` 相同，只算 1 个。

### 6.5 缓存策略

- 默认缓存 TTL：60 秒。
- 每次 iframe bootstrap 可以返回缓存端点。
- 页面提供“刷新端点列表”按钮，触发后端强制刷新 Admin API。
- Admin API 失败时保留上一份成功缓存，并在 UI 标记“端点列表来自缓存”。
- 如果从未成功获取，可使用静态 fallback 配置。

缓存结构：

```json
{
  "fetched_at": "2026-06-20T10:30:00+09:00",
  "expires_at": "2026-06-20T10:31:00+09:00",
  "source": "admin_api",
  "entrypoint_count": 3,
  "public_path": "/lg",
  "entrypoints": []
}
```

---

## 7. 诊断目标与指标

### 7.1 测试目标

本项目只测试本诊断容器暴露的轻量接口。对每个入口端点，浏览器访问：

```text
{lg_base_url}/diag/ping
{lg_base_url}/diag/blob?size=64k
{lg_base_url}/diag/blob?size=1m
{lg_base_url}/diag/stream?events=20&interval_ms=200
```

假设：

```text
base_url:    https://cf-api.example.com/v1
public_path: /lg
lg_base_url: https://cf-api.example.com/v1/lg
```

实际测试：

```text
https://cf-api.example.com/v1/lg/diag/ping
https://cf-api.example.com/v1/lg/diag/blob?size=64k
https://cf-api.example.com/v1/lg/diag/blob?size=1m
https://cf-api.example.com/v1/lg/diag/stream?events=20&interval_ms=200
```

不调用：

```text
/v1/chat/completions
/v1/responses
/v1/messages
/v1/embeddings
/v1beta/models/...:generateContent
```

### 7.2 浏览器侧指标

每个入口默认测试：

1. `ping`：小 JSON 响应，测基础延迟、TTFB、成功率。
2. `blob 64k`：小文件下载，测轻量吞吐。
3. `blob 1m`：中等文件下载，测稳定吞吐，可配置是否开启。
4. `stream`：诊断 SSE 流，不涉及模型，测长连接、分块、首事件、chunk gap、是否被反代缓冲。

指标：

| 指标 | 说明 |
|---|---|
| `success_rate` | 成功请求数 / 总请求数 |
| `http_loss_rate` | 网络错误、超时、非预期状态码、CORS 阻断数量 / 总请求数 |
| `p50_duration_ms` | 总耗时中位数 |
| `p95_duration_ms` | 总耗时 p95 |
| `p50_ttfb_ms` | HTTP 首字节中位数 |
| `p95_ttfb_ms` | HTTP 首字节 p95 |
| `jitter_ms` | 建议用 p95 - p50 表示抖动 |
| `timeout_rate` | 超时占比 |
| `download_mbps` | blob 下载速度 |
| `first_event_ms` | SSE 第一个事件到达时间，不是模型 token |
| `max_chunk_gap_ms` | SSE 相邻事件最大间隔 |
| `stream_buffered` | 是否疑似被 CDN/Nginx 缓冲，最后一次性返回 |
| `cors_blocked` | 是否被浏览器 CORS 阻断 |
| `timing_detail_available` | 是否能读取详细 PerformanceResourceTiming |

### 7.3 “首 token”文案处理

本项目不测试模型首 token。为了避免客户误解，UI 和报告建议统一使用：

```text
首包 / TTFB
流式首事件
```

不要写：

```text
首 token
模型首 token
first token
```

如果前端已有“首 token”字段名，Coding Agent 应改成：

```text
first_byte_ms      HTTP 首字节
first_event_ms     诊断流首事件
```

### 7.4 HTTP 丢包率定义

浏览器不能发真实 ICMP，也不能测网络层丢包。这里的“丢包率”是业务层近似指标：

```text
http_loss_rate = 失败请求数 / 总请求数
```

失败请求包括：

```text
fetch 网络错误
AbortController 超时
HTTP 状态码非预期
CORS 阻断
SSE 未收到 done 事件
SSE 中途断流
```

报告中建议显示为：

```text
HTTP 失败率 / 业务层丢包率
```

不要声称这是 ICMP 网络层丢包。

---

## 8. 诊断接口设计

所有接口都在 `{base_url}{public_path}` 下。下文用 `{lg_base_url}` 表示。

### 8.1 前端页面

```http
GET {lg_base_url}/embed
```

返回 Vue SPA 页面。

要求：

```text
Referrer-Policy: no-referrer
Content-Security-Policy: frame-ancestors ...
Cache-Control: no-store 或短缓存
```

### 8.2 bootstrap

```http
POST {lg_base_url}/api/bootstrap
```

用途：用 iframe query 参数换本地诊断 session。

响应包含：

```json
{
  "session_id": "sess_xxx",
  "session_token": "diag_xxx",
  "public_path": "/lg",
  "entrypoint_count": 3,
  "entrypoints": []
}
```

### 8.3 入口列表

```http
GET {lg_base_url}/api/entrypoints?refresh=0
Authorization: Bearer {session_token}
```

响应：

```json
{
  "source": "admin_api | cache | static_fallback",
  "entrypoint_count": 3,
  "public_path": "/lg",
  "entrypoints": [
    {
      "id": "abc123def456",
      "name": "默认入口",
      "description": "default",
      "base_url": "https://api.example.com/v1",
      "lg_base_url": "https://api.example.com/v1/lg",
      "origin": "https://api.example.com",
      "host": "api.example.com"
    }
  ]
}
```

### 8.4 报告提交

```http
POST {lg_base_url}/api/reports
Authorization: Bearer {session_token}
Content-Type: application/json
```

请求体包含浏览器检测结果和可选服务端检测结果。

响应：

```json
{
  "report_id": "rpt_xxx",
  "share_url": "https://api.example.com/v1/lg/report/rpt_xxx"
}
```

### 8.5 报告查看

```http
GET {lg_base_url}/report/:report_id
GET {lg_base_url}/api/reports/:report_id
```

页面和 JSON 分开。

### 8.6 诊断 ping

```http
GET {lg_base_url}/diag/ping?nonce=xxx
```

响应：

```json
{
  "ok": true,
  "service": "sub2api-origin-lg",
  "server_time": "2026-06-20T10:30:00+09:00",
  "request_id": "req_xxx",
  "public_path": "/lg"
}
```

响应头：

```http
Cache-Control: no-store
Content-Type: application/json
Timing-Allow-Origin: *
Access-Control-Allow-Origin: <Origin 或 *，见 CORS 章节>
Access-Control-Expose-Headers: Server-Timing, X-Request-Id, Content-Length
X-Request-Id: req_xxx
Server-Timing: app;dur=1
```

### 8.7 下载测试

```http
GET {lg_base_url}/diag/blob?size=64k&nonce=xxx
GET {lg_base_url}/diag/blob?size=1m&nonce=xxx
```

限制：

```text
size 只允许白名单：16k、64k、256k、1m、5m
默认最大 1m
5m 需要 config 开启
必须 no-store，避免浏览器缓存污染测速
```

### 8.8 诊断流式接口

```http
GET {lg_base_url}/diag/stream?events=20&interval_ms=200&bytes=32&nonce=xxx
```

这是诊断 SSE，不是模型接口。用于测试 CDN/反代/浏览器到源站的流式分块能力。

响应头：

```http
Content-Type: text/event-stream; charset=utf-8
Cache-Control: no-cache, no-transform
Connection: keep-alive
X-Accel-Buffering: no
Timing-Allow-Origin: *
```

事件示例：

```text
event: hello
data: {"request_id":"req_xxx","server_time":"..."}

event: tick
data: {"seq":1,"server_time":"...","padding":"..."}

event: tick
data: {"seq":2,"server_time":"...","padding":"..."}

event: done
data: {"ok":true,"events":20}
```

前端统计：

```text
first_event_ms
stream_total_ms
event_count
max_event_gap_ms
avg_event_gap_ms
done_seen
stream_interrupted
stream_buffered
```

疑似缓冲判断：

```text
如果 first_event_ms 接近 total_ms，且多个事件在极短时间内集中到达，则标记 stream_buffered=true。
```

---

## 9. 浏览器测速实现

### 9.1 基础 fetch 计时

```ts
async function timedFetch(url: string, timeoutMs: number) {
  const controller = new AbortController()
  const timer = window.setTimeout(() => controller.abort(), timeoutMs)
  const started = performance.now()

  try {
    const res = await fetch(url, {
      method: 'GET',
      cache: 'no-store',
      credentials: 'omit',
      signal: controller.signal,
    })

    const firstHeadersAt = performance.now()
    const body = await res.arrayBuffer()
    const ended = performance.now()

    return {
      ok: res.ok,
      status: res.status,
      duration_ms: Math.round(ended - started),
      header_ms: Math.round(firstHeadersAt - started),
      response_bytes: body.byteLength,
    }
  } catch (e: any) {
    return {
      ok: false,
      duration_ms: Math.round(performance.now() - started),
      error_kind: e?.name === 'AbortError' ? 'timeout' : 'network_error',
      error_message: String(e?.message || e),
    }
  } finally {
    window.clearTimeout(timer)
  }
}
```

### 9.2 TTFB 采集

优先使用 `PerformanceResourceTiming`：

```ts
function getResourceTiming(url: string) {
  const entries = performance.getEntriesByName(url, 'resource') as PerformanceResourceTiming[]
  const e = entries[entries.length - 1]
  if (!e) return null

  return {
    dns_ms: e.domainLookupEnd - e.domainLookupStart,
    tcp_ms: e.connectEnd - e.connectStart,
    tls_ms: e.secureConnectionStart > 0 ? e.connectEnd - e.secureConnectionStart : 0,
    ttfb_ms: e.responseStart - e.requestStart,
    transfer_ms: e.responseEnd - e.responseStart,
    total_ms: e.responseEnd - e.startTime,
    detail_available: e.responseStart > 0,
  }
}
```

对跨入口测试，需要诊断接口返回 `Timing-Allow-Origin`，否则浏览器会隐藏详细 timing。

### 9.3 SSE 计时

```ts
async function testDiagStream(url: string, timeoutMs: number) {
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
        const eventName = parseSseEventName(chunk) || 'message'
        events.push({ name: eventName, at: now })
        if (eventName === 'done') doneSeen = true
      }
    }

    const ended = performance.now()
    const first = events[0]
    const gaps = events.slice(1).map((e, i) => e.at - events[i].at)

    return {
      ok: doneSeen,
      first_event_ms: first ? Math.round(first.at - started) : null,
      total_ms: Math.round(ended - started),
      event_count: events.length,
      max_event_gap_ms: gaps.length ? Math.round(Math.max(...gaps)) : null,
      done_seen: doneSeen,
      stream_interrupted: !doneSeen,
      stream_buffered: detectBuffered(events, started, ended),
    }
  } catch (e: any) {
    return {
      ok: false,
      error_kind: e?.name === 'AbortError' ? 'timeout' : 'network_error',
      error_message: String(e?.message || e),
    }
  } finally {
    window.clearTimeout(timer)
  }
}
```

---

## 10. CORS 与 Timing 策略

### 10.1 为什么仍需要 CORS

iframe 页面从某一个入口加载，例如：

```text
https://api.example.com/v1/lg/embed
```

它测试默认入口时是同源：

```text
https://api.example.com/v1/lg/diag/ping
```

但测试其他入口时可能跨源：

```text
https://cf-api.example.com/v1/lg/diag/ping
https://origin-api.example.com/v1/lg/diag/ping
```

因此 `/diag/*` 必须支持跨源访问。

### 10.2 诊断接口 CORS

`/diag/*` 不返回敏感数据，可以使用宽松策略：

```http
Access-Control-Allow-Origin: *
Access-Control-Allow-Methods: GET, OPTIONS
Access-Control-Allow-Headers: Content-Type, X-Requested-With
Access-Control-Expose-Headers: Server-Timing, X-Request-Id, Content-Length
Timing-Allow-Origin: *
```

`/api/*` 包含 session 和报告，应使用严格来源白名单：

```text
allowed_parent_origins
allowed_entrypoint_origins
```

如果 `Origin` 不在允许列表，拒绝。

### 10.3 CSP frame-ancestors

`/embed` 页面必须允许被 sub2api 后台 iframe 嵌入。

示例：

```http
Content-Security-Policy: frame-ancestors https://sub2api.example.com;
```

如果支持多个后台域名，用配置：

```yaml
security:
  allowed_parent_origins:
    - "https://sub2api.example.com"
    - "https://admin.example.com"
```

不要使用：

```http
X-Frame-Options: DENY
X-Frame-Options: SAMEORIGIN
```

---

## 11. 服务端补充探针

服务端补充探针可选，默认开启轻量 HTTP 探针，不测试上游模型。

目标仍然是每个入口的 `lg_base_url`：

```text
GET {lg_base_url}/diag/ping
GET {lg_base_url}/diag/blob?size=64k
GET {lg_base_url}/diag/stream?events=5&interval_ms=100
```

服务端采集：

```text
DNS 时间
TCP connect 时间
TLS handshake 时间
HTTP TTFB
HTTP 总耗时
状态码
错误类型
```

Go 后端建议使用 `net/http/httptrace`。

注意：服务端探针是：

```text
诊断容器 -> 入口域名 -> 诊断容器
```

它主要用于发现 DNS、反代、CDN 回源、TLS、WAF 配置问题，不代表客户真实网络质量。报告里要标记“服务端补充参考”。

### 11.1 ICMP

第一版默认关闭 ICMP。

如果开启，只允许 ping 已发现的入口 host，不能让用户输入任意目标。

Docker 需要：

```yaml
cap_add:
  - NET_RAW
```

报告中必须注明：ICMP 丢包不等同于 HTTP 业务失败率，很多 CDN/API 网关会禁用或降权 ICMP。

---

## 12. 报告结构

### 12.1 报告 JSON

```json
{
  "report_id": "rpt_20260620_xxx",
  "session_id": "sess_xxx",
  "created_at": "2026-06-20T10:30:00+09:00",
  "user": {
    "id": "123",
    "username": "demo"
  },
  "iframe_context": {
    "theme": "dark",
    "lang": "zh-CN",
    "ui_mode": "embedded",
    "src_host": "sub2api.example.com",
    "src_url": "https://sub2api.example.com/custom/network-diagnose"
  },
  "summary": {
    "entrypoint_count": 3,
    "best_endpoint_id": "abc123def456",
    "best_endpoint_name": "默认入口",
    "score": 92,
    "level": "good",
    "main_problem": null,
    "recommendation": "当前默认入口表现最佳，可以继续使用。"
  },
  "entrypoints": [
    {
      "endpoint_id": "abc123def456",
      "name": "默认入口",
      "base_url": "https://api.example.com/v1",
      "lg_base_url": "https://api.example.com/v1/lg",
      "browser": {
        "success_rate": 1,
        "http_loss_rate": 0,
        "p50_duration_ms": 120,
        "p95_duration_ms": 220,
        "p50_ttfb_ms": 80,
        "p95_ttfb_ms": 150,
        "download_mbps": 12.4,
        "first_event_ms": 110,
        "max_chunk_gap_ms": 240,
        "stream_buffered": false,
        "cors_blocked": false,
        "timing_detail_available": true
      },
      "server": {
        "enabled": true,
        "dns_ms": 3,
        "tcp_ms": 12,
        "tls_ms": 25,
        "ttfb_ms": 60,
        "duration_ms": 90,
        "status": 200
      }
    }
  ]
}
```

### 12.2 UI 展示字段

入口结果表格：

```text
入口名称
Base URL
诊断 URL
状态
成功率
HTTP 失败率 / 业务层丢包率
p50 总耗时
p95 总耗时
p50 首包
p95 首包
下载速度
流式首事件
最大事件间隔
是否疑似缓冲
CORS 状态
Timing 详情
推荐等级
```

### 12.3 自动结论规则

建议阈值：

```text
success_rate >= 0.98 且 p95_duration_ms < 800     -> good
success_rate >= 0.95 且 p95_duration_ms < 1500    -> warning
success_rate < 0.95 或 p95_duration_ms >= 1500    -> bad
stream_buffered = true                            -> warning，提示反代/CDN 可能缓冲流式响应
cors_blocked = true                               -> bad，提示 /diag/* CORS 配置异常
```

推荐入口排序：

```text
1. success_rate 高
2. http_loss_rate 低
3. p95_ttfb_ms 低
4. p95_duration_ms 低
5. stream_buffered=false
6. max_chunk_gap_ms 低
```

---

## 13. 数据库设计

SQLite 表建议：

```sql
CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT,
  username TEXT,
  src_host TEXT,
  src_url TEXT,
  theme TEXT,
  lang TEXT,
  created_at DATETIME NOT NULL,
  expires_at DATETIME NOT NULL
);

CREATE TABLE endpoint_cache (
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL,
  public_path TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  fetched_at DATETIME NOT NULL,
  expires_at DATETIME NOT NULL
);

CREATE TABLE reports (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  user_id TEXT,
  summary_json TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  created_at DATETIME NOT NULL
);

CREATE INDEX idx_reports_user_id ON reports(user_id);
CREATE INDEX idx_reports_created_at ON reports(created_at);
```

不需要长期保存 token。`token` 只在 bootstrap 时临时使用，不能写日志、不能入库。

---

## 14. 配置文件

`config.example.yaml`：

```yaml
app:
  listen: "0.0.0.0:8080"

  # 外部可见路径，追加到每个 base_url 后面。
  # 默认 /lg，但必须允许改成 /network-diagnose、/__lg、/tools/lg 等。
  public_path: "/lg"

  # 容器内部路由前缀。
  # 推荐反代 strip prefix 后保持 /。
  # 如果反代不 strip prefix，可配置成实际收到的前缀。
  router_prefix: "/"

  # 可选。为空时根据当前请求和 entrypoint 自动生成。
  public_url: ""

  env: "production"
  trust_forwarded_headers: true

security:
  allowed_parent_origins:
    - "https://sub2api.example.com"
  allowed_src_hosts:
    - "sub2api.example.com"
  diag_session_secret: "change_this_very_long_random_secret"
  diag_session_ttl_seconds: 1800
  allow_http_endpoints: false
  allow_private_endpoints: false

sub2api:
  # sub2api 主站/Admin API 地址，不是入口 base_url。
  admin_base_url: "https://sub2api.example.com"
  admin_api_key: "change_this_admin_api_key"
  endpoint_cache_ttl_seconds: 60

probe:
  browser_repeat: 5
  browser_timeout_ms: 8000
  server_probe_enabled: true
  server_repeat: 3
  server_timeout_ms: 5000
  enable_icmp: false

  # 诊断路由也做成配置，避免前端硬编码。
  paths:
    ping: "/diag/ping"
    blob: "/diag/blob"
    stream: "/diag/stream"

  blob_sizes:
    - "64k"
    - "1m"
  max_blob_size: "1m"

  stream:
    events: 20
    interval_ms: 200
    bytes: 32

storage:
  driver: "sqlite"
  dsn: "/data/sub2api-origin-lg.db"

fallback:
  static_endpoints: []
  # - name: "默认入口"
  #   base_url: "https://api.example.com/v1"
  #   description: "fallback"
```

环境变量覆盖规则建议：

```text
APP_PUBLIC_PATH=/lg
APP_ROUTER_PREFIX=/
SUB2API_ADMIN_BASE_URL=https://sub2api.example.com
SUB2API_ADMIN_API_KEY=xxx
```

---

## 15. Docker Compose 示例

```yaml
version: "3.9"

services:
  sub2api-origin-lg:
    image: ghcr.io/your-org/sub2api-origin-lg:latest
    container_name: sub2api-origin-lg
    restart: unless-stopped

    ports:
      - "8088:8080"

    volumes:
      - ./data:/data
      - ./config.yaml:/app/config.yaml:ro

    environment:
      APP_ENV: "production"
      APP_LISTEN: "0.0.0.0:8080"
      APP_PUBLIC_PATH: "/lg"
      APP_ROUTER_PREFIX: "/"
      APP_TRUST_FORWARDED_HEADERS: "true"

      DB_DRIVER: "sqlite"
      SQLITE_DSN: "/data/sub2api-origin-lg.db"

      SUB2API_ADMIN_BASE_URL: "https://sub2api.example.com"
      SUB2API_ADMIN_API_KEY: "change_this_admin_api_key"
      SUB2API_ENDPOINT_CACHE_TTL_SECONDS: "60"

      ALLOWED_PARENT_ORIGINS: "https://sub2api.example.com"
      ALLOWED_SRC_HOSTS: "sub2api.example.com"

      DIAG_SESSION_SECRET: "change_this_very_long_random_secret"
      DIAG_SESSION_TTL_SECONDS: "1800"

      BROWSER_PROBE_REPEAT: "5"
      BROWSER_PROBE_TIMEOUT_MS: "8000"
      SERVER_PROBE_ENABLED: "true"
      SERVER_PROBE_REPEAT: "3"
      SERVER_PROBE_TIMEOUT_MS: "5000"
      ENABLE_ICMP: "false"

    security_opt:
      - no-new-privileges:true

    networks:
      - sub2api-net

networks:
  sub2api-net:
    external: true
```

如果确实需要 ICMP：

```yaml
cap_add:
  - NET_RAW
```

---

## 16. 后端模块设计

建议目录：

```text
backend/
  cmd/server/main.go
  internal/config/
  internal/server/
    router.go
    middleware.go
  internal/adminclient/
    client.go
    settings.go
  internal/entrypoints/
    normalize.go
    cache.go
  internal/session/
  internal/report/
  internal/probe/
    browser_contract.go
    server_httptrace.go
    diag_handlers.go
  internal/security/
    cors.go
    ssrf.go
    token.go
```

### 16.1 Admin Client

Go 类型：

```go
type AdminClient interface {
    GetSettings(ctx context.Context) (*SystemSettings, error)
}

type SystemSettings struct {
    APIBaseURL      string           `json:"api_base_url"`
    APIBaseUrlCamel string           `json:"apiBaseUrl"`
    BaseURL         string           `json:"base_url"`
    BaseUrlCamel    string           `json:"baseUrl"`
    CustomEndpoints []CustomEndpoint `json:"custom_endpoints"`
    CustomCamel     []CustomEndpoint `json:"customEndpoints"`
}

type CustomEndpoint struct {
    Name        string `json:"name"`
    Endpoint    string `json:"endpoint"`
    BaseURL     string `json:"base_url"`
    BaseUrlCamel string `json:"baseUrl"`
    Description string `json:"description"`
}
```

归一化后类型：

```go
type EntryPoint struct {
    ID          string `json:"id"`
    Source      string `json:"source"`
    Name        string `json:"name"`
    Description string `json:"description"`
    RawValue    string `json:"raw_value"`
    BaseURL     string `json:"base_url"`
    PublicPath  string `json:"public_path"`
    LGBaseURL   string `json:"lg_base_url"`
    Origin      string `json:"origin"`
    Host        string `json:"host"`
    Scheme      string `json:"scheme"`
    Enabled     bool   `json:"enabled"`
}
```

注意：对外 JSON 用 `base_url`，不要输出 `api_base_url`。

### 16.2 路由注册

应支持 `router_prefix`：

```go
prefix := cfg.App.RouterPrefix
if prefix == "/" {
    registerRoutes(r.Group(""))
} else {
    registerRoutes(r.Group(prefix))
}
```

但推荐生产反代 strip prefix，所以内部 `router_prefix=/`。

### 16.3 CORS 逻辑

`/diag/*`：

```text
允许 GET, OPTIONS
允许 Origin: * 或回显 Origin
不允许 credentials
```

`/api/*`：

```text
只允许 bootstrap iframe 所在 origin、allowed_parent_origins、已发现 entrypoint origins
需要 Authorization: Bearer session_token
```

---

## 17. 前端模块设计

建议目录：

```text
frontend/
  src/
    main.ts
    App.vue
    api/
      bootstrap.ts
      entrypoints.ts
      report.ts
    diagnose/
      runner.ts
      timed-fetch.ts
      stream-test.ts
      scoring.ts
      stats.ts
    components/
      EndpointTable.vue
      ProgressPanel.vue
      ReportSummary.vue
      RawJsonPanel.vue
    utils/
      url.ts
      iframe.ts
      timing.ts
```

### 17.1 前端不得硬编码 `/lg`

前端入口加载后，应优先从 bootstrap 响应读取：

```ts
app.public_path
probe.paths.ping
probe.paths.blob
probe.paths.stream
```

构造测试 URL 使用后端返回的 `lg_base_url`：

```ts
const pingUrl = joinURL(entry.lg_base_url, config.probe.paths.ping)
const blobUrl = joinURL(entry.lg_base_url, config.probe.paths.blob) + '?size=64k'
const streamUrl = joinURL(entry.lg_base_url, config.probe.paths.stream) + '?events=20&interval_ms=200'
```

不要这样写：

```ts
entry.base_url + '/lg/diag/ping'
```

### 17.2 页面流程

```text
1. 读取 iframe query 参数。
2. POST ./api/bootstrap。
3. 清理 URL 中的 token。
4. 获取 entrypoints。
5. 对每个 entrypoint 执行 ping/blob/stream 测试。
6. 汇总 p50/p95/失败率/推荐入口。
7. POST ./api/reports。
8. postMessage 通知父页面诊断完成。
```

### 17.3 postMessage

iframe 向父页面发送：

```js
window.parent.postMessage({
  type: 'sub2api-lg:completed',
  report_id: reportId,
  score,
  best_endpoint_id,
}, parentOrigin)
```

`parentOrigin` 必须来自校验过的 `src_url` 或配置白名单，不要默认 `*`。

---

## 18. 安全要求

1. `token` 只能用于 bootstrap，不能入库，不能写日志。
2. bootstrap 成功后前端必须 `history.replaceState` 清理 URL。
3. Admin API Key 只存在诊断容器后端环境变量。
4. 后端探针目标只能来自 Admin API 或静态白名单。
5. 禁止用户输入任意 URL。
6. 禁止探测私网、本地、metadata 地址。
7. `/api/*` 必须鉴权。
8. `/diag/*` 不返回敏感信息。
9. `public_path` 必须校验，防止路径穿越。
10. 报告中不要展示完整 JWT、Admin Key、用户敏感字段。

---

## 19. 故障文案

### 19.1 路径未转发

```text
该入口的诊断路径无法访问：{lg_base_url}/diag/ping。
请确认已将 {base_url}{public_path} 转发到 sub2api-origin-lg 容器，并检查反向代理是否正确 strip prefix。
```

### 19.2 CORS 异常

```text
浏览器无法跨入口读取该诊断接口。请确认 {lg_base_url}/diag/* 返回 Access-Control-Allow-Origin 和 Timing-Allow-Origin。
```

### 19.3 流式疑似缓冲

```text
该入口的诊断流事件疑似被一次性缓冲返回。请检查 Nginx/CDN 配置：proxy_buffering off、gzip off、Cache-Control: no-transform、X-Accel-Buffering: no。
```

### 19.4 入口整体偏慢

```text
该入口 p95 首包和总耗时偏高，可能与 CDN 节点、客户运营商链路、WAF、反向代理或源站连接有关。建议对比其他入口结果后切换推荐入口。
```

---

## 20. 开发阶段拆分

### Phase 1：MVP

- [ ] Go 后端启动，读取 config。
- [ ] 支持 `app.public_path` 和 `app.router_prefix`。
- [ ] 实现 `/embed` 静态页面。
- [ ] 实现 `/diag/ping`、`/diag/blob`、`/diag/stream`。
- [ ] 实现 bootstrap，读取 iframe 参数并清理 token。
- [ ] 实现 Admin API Client。
- [ ] 从 Admin API 读取 `base_url` 和 `custom_endpoints`。
- [ ] 归一化为内部 `base_url` 和 `lg_base_url`。
- [ ] 前端完成 entrypoints 展示和基础 ping 测试。
- [ ] 生成本地报告。

### Phase 2：完整浏览器诊断

- [ ] blob 下载测速。
- [ ] SSE 诊断流测速。
- [ ] PerformanceResourceTiming 详情采集。
- [ ] p50/p95/失败率/抖动计算。
- [ ] 推荐入口算法。
- [ ] 报告页和 JSON 导出。
- [ ] postMessage 回传父页面。

### Phase 3：服务端补充探针

- [ ] Go httptrace。
- [ ] 服务端访问各 `lg_base_url`。
- [ ] 报告中展示服务端对照结果。
- [ ] 可选 ICMP。

### Phase 4：体验和运维

- [ ] 多语言。
- [ ] 暗色主题。
- [ ] 日志脱敏。
- [ ] Prometheus 指标。
- [ ] 管理后台查看历史报告。

---

## 21. 验收标准

1. `APP_PUBLIC_PATH=/lg` 时，`{base_url}/lg/embed` 能正常打开。
2. `APP_PUBLIC_PATH=/tools/lg` 时，`{base_url}/tools/lg/embed` 能正常打开。
3. 代码中没有硬编码 `'/lg'` 作为业务路径；只能作为默认配置值出现。
4. Admin API 标准 `base_url` 能被读取；旧字段仅做兼容，对外响应字段统一为 `base_url`。
5. custom endpoints 能全部归一化为 `base_url`。
6. `lg_base_url` 正确等于 `joinURL(base_url, public_path)`。
7. 前端测试 URL 使用后端返回的 `lg_base_url`，不自行拼硬编码路径。
8. 不调用任何模型接口。
9. 不产生任何上游模型消耗。
10. 报告能展示入口数量、每个入口的成功率、HTTP 失败率、p50/p95、TTFB、下载速度、流式首事件、是否疑似缓冲。
11. iframe token bootstrap 后会从 URL 清理。
12. Admin API Key 不会下发给前端。
13. `/diag/*` 可跨入口访问并暴露 Timing。
14. `/api/*` 必须 session 鉴权。
15. 任意用户不能输入自定义探测目标。

---

## 22. 最终结论

本项目应实现为一个独立的 `sub2api-origin-lg` 诊断容器，并通过反向代理挂载到每个 sub2api 入口的：

```text
{base_url}{public_path}
```

默认：

```text
{base_url}/lg
```

`base_url` 来自 sub2api Admin API 的默认入口和自定义入口；旧字段只在 Admin Client 层兼容，本诊断项目内部和对外输出统一使用 `base_url`。`public_path` 必须可配置，不能硬编码 `/lg`。

浏览器侧通过访问每个入口下的诊断接口：

```text
{base_url}{public_path}/diag/ping
{base_url}{public_path}/diag/blob
{base_url}{public_path}/diag/stream
```

来测试客户到源站入口的延迟、首包、稳定性、业务层失败率和流式分块质量。整个过程不测试上游模型，不调用模型接口，不消耗模型额度。
