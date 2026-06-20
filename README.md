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
```

生产环境建议把外部 `{base_url}{public_path}` strip prefix 后转发到容器根路径。如果网关不能 strip prefix，设置 `APP_ROUTER_PREFIX` 为容器实际收到的路径前缀。

## 配置

复制 `config.example.yaml` 为 `config.yaml` 后调整。常用环境变量：

```text
APP_PUBLIC_PATH=/lg
APP_ROUTER_PREFIX=/
SUB2API_ADMIN_BASE_URL=https://sub2api.example.com
SUB2API_ADMIN_API_KEY=...
ALLOWED_PARENT_ORIGINS=https://sub2api.example.com
ALLOWED_SRC_HOSTS=sub2api.example.com
SQLITE_DSN=/data/sub2api-origin-lg.db
```

`/diag/*` 只返回诊断数据并允许跨入口访问；`/api/*` 除 bootstrap 外都需要 `Authorization: Bearer {session_token}`。

`/api/bootstrap` 会使用 iframe 传入的 `token` 调用 `SUB2API_ADMIN_BASE_URL + SUB2API_USERINFO_PATH` 校验用户身份，并要求返回的用户 ID 与 iframe 的 `user_id` 完全一致；未配置 sub2api 用户校验时不会创建诊断 session。

## 构建校验

GitHub Actions 会在 push 到 `main`/`master` 和 PR 时执行：

```sh
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build -o /tmp/sub2api-origin-lg ./backend/cmd/server
npm --prefix frontend ci
npm --prefix frontend run build
docker build -t sub2api-origin-lg:ci .
```
