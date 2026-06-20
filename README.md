# sub2api-origin-lg

sub2api 源站入口性能诊断工具。服务通过反向代理挂载在每个入口的 `{base_url}{public_path}` 下，默认 `public_path=/lg`。

## 本地运行

```sh
npm --prefix frontend install
npm --prefix frontend run build
go run ./backend/cmd/server
```

默认监听 `0.0.0.0:8080`，访问：

```text
http://localhost:8080/embed
http://localhost:8080/admin
```

生产环境建议把外部 `{base_url}{public_path}` strip prefix 后转发到容器根路径。如果网关不能 strip prefix，设置 `APP_ROUTER_PREFIX` 为容器实际收到的路径前缀。

## 配置

复制 `config.example.yaml` 为 `config.yaml` 后调整。常用环境变量：

```text
APP_PUBLIC_PATH=/lg
APP_ROUTER_PREFIX=/
APP_ADMIN_PUBLIC_URL=https://sub2api.example.com/lg
SUB2API_ADMIN_BASE_URL=https://sub2api.example.com
SUB2API_ADMIN_API_KEY=...
ALLOWED_PARENT_ORIGINS=https://sub2api.example.com
ALLOWED_CUSTOMER_PARENT_ORIGINS=https://sub2api.example.com
ALLOWED_ADMIN_PARENT_ORIGINS=https://sub2api.example.com
ALLOWED_SRC_HOSTS=sub2api.example.com
ALLOWED_ADMIN_HOSTS=sub2api.example.com
SQLITE_DSN=/data/sub2api-origin-lg.db
```

`/diag/*` 只返回诊断数据并允许跨入口访问，不向浏览器暴露 `X-Origin-Peer-IP`。`/api/*` 除 bootstrap 和分享读取外都需要 `Authorization: Bearer {session_token}`。

`/api/customer/bootstrap` 会使用 iframe 传入的 `ticket`，或兼容旧版 `token`，调用 `SUB2API_ADMIN_BASE_URL + SUB2API_USERINFO_PATH` 校验用户身份，并要求返回的用户 ID 与 iframe 的 `user_id` 完全一致；未配置 sub2api 用户校验时不会创建诊断 session。`/api/bootstrap` 保留为客户 bootstrap 兼容入口。

客户报告接口：

```text
GET  /embed
POST /api/customer/bootstrap
GET  /api/customer/entrypoints
POST /api/customer/reports
GET  /api/customer/reports/{id}?share_token=...
GET  /report/{id}?share_token=...
```

管理员接口：

```text
GET  /admin
POST /api/admin/bootstrap
GET  /api/admin/reports
GET  /api/admin/reports/{id}
GET  /api/admin/reports/{id}/events
GET  /api/admin/entrypoints/inventory
```

`/api/reports/{id}` 仅保留兼容读取，返回脱敏后的 `customer_report`，不再返回内部 payload。

## 构建校验

GitHub Actions 会在 push 到 `main`/`master` 和 PR 时执行：

```sh
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build -o /tmp/sub2api-origin-lg ./backend/cmd/server
npm --prefix frontend ci
npm --prefix frontend run build
docker build -t sub2api-origin-lg:ci .
```

## 镜像发布

push 到 `main` 或手动运行 `Publish Image` workflow 会发布镜像：

```text
ghcr.io/milesians/sub2api-lg:latest
ghcr.io/milesians/sub2api-lg:sha-<commit>
```
