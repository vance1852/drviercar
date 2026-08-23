# drviercar

自动驾驶路测运营与车端数据闭环平台。系统同时承载两条互相依赖的业务链：

1. **路测编排链**：路测计划（campaign）→ 车辆与安全员排班（assignment）→ 出车与接管执行（drive session）→ 里程结算（settlement）。
2. **数据闭环链**：车端采集回传（capture batch / frame）→ 清单与质量校验 → 影子模式处置单（triage ticket）→ 训练数据集封板与发布（dataset）。

两条链共享车辆、安全员与审计事件：未决处置单会阻塞里程结算和数据集封板，未关闭的关键接管会在结算时扣减可计费里程。

## 运行环境

- Go 1.22（`go.mod` 声明 `go 1.22`，构建时建议 `GOTOOLCHAIN=local`）
- SQLite（纯 Go 驱动 `modernc.org/sqlite`，无需 CGO）
- 依赖在镜像构建阶段预下载到模块缓存，容器内可离线构建与测试

## 快速开始

```bash
export DRVIERCAR_ADDR=127.0.0.1:8080
export DRVIERCAR_DB_PATH=data/drviercar.sqlite
export DRVIERCAR_BOOTSTRAP_ADMIN=fleet-admin
export DRVIERCAR_BOOTSTRAP_SECRET=change-this-secret
go run ./cmd/server
```

启动后可用的探针：

```bash
curl -s http://127.0.0.1:8080/healthz
curl -s http://127.0.0.1:8080/readyz
curl -s http://127.0.0.1:8080/api/v1/version
```

## 配置项

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `DRVIERCAR_ADDR` | `127.0.0.1:8080` | HTTP 监听地址 |
| `DRVIERCAR_DB_PATH` | `data/drviercar.sqlite` | SQLite 数据文件 |
| `DRVIERCAR_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `DRVIERCAR_SESSION_TTL` | `8h` | 会话有效期 |
| `DRVIERCAR_REQUEST_TIMEOUT` | `15s` | 单请求上下文超时 |
| `DRVIERCAR_SHUTDOWN_TIMEOUT` | `10s` | 优雅关闭超时 |
| `DRVIERCAR_WORKER_INTERVAL` | `1s` | 后台任务轮询间隔 |
| `DRVIERCAR_WORKER_BATCH` | `5` | 单轮领取任务数 |
| `DRVIERCAR_WORKER_BACKOFF` | `2s` | 重试退避基数 |
| `DRVIERCAR_DB_MAX_CONNS` | `4` | 数据库最大连接数 |
| `DRVIERCAR_BOOTSTRAP_ADMIN` | 空 | 首次启动创建的车队管理员登录名 |
| `DRVIERCAR_BOOTSTRAP_SECRET` | 空 | 该管理员口令，长度至少 8 位 |

## 角色与权限

| 角色 | 业务职责 |
| --- | --- |
| `fleet_admin` | 创建/推进路测计划、登记车辆、排班、终止排班、结算与审批、创建与封板数据集、撤销会话 |
| `safety_operator` | 出车、上报里程与接管、回传采集数据、处置影子模式处置单 |

登录成功后返回不可逆存储的 Bearer Token；退出立即撤销，过期会话由后台任务清理。

## 主要接口

```text
POST   /api/v1/auth/login
POST   /api/v1/auth/logout
GET    /api/v1/auth/me
POST   /api/v1/operators
POST   /api/v1/operators/{id}/session-revocations

POST   /api/v1/campaigns
GET    /api/v1/campaigns
GET    /api/v1/campaigns/{id}
POST   /api/v1/campaigns/{id}/transitions
GET    /api/v1/campaigns/{id}/settlement-summary

POST   /api/v1/vehicles
GET    /api/v1/vehicles

POST   /api/v1/assignments            (需要 Idempotency-Key)
GET    /api/v1/assignments
GET    /api/v1/assignments/{id}
POST   /api/v1/assignments/{id}/abort
POST   /api/v1/assignments/batch-abort
POST   /api/v1/assignments/{id}/drives
POST   /api/v1/assignments/{id}/settlement

GET    /api/v1/drives/{id}
POST   /api/v1/drives/{id}/mileage
POST   /api/v1/drives/{id}/takeovers
POST   /api/v1/drives/{id}/close
POST   /api/v1/takeovers/{id}/resolve

GET    /api/v1/settlements/{id}
POST   /api/v1/settlements/{id}/approve

POST   /api/v1/captures
GET    /api/v1/captures
GET    /api/v1/captures/{id}
POST   /api/v1/captures/{id}/validate
POST   /api/v1/captures/{id}/reject

GET    /api/v1/triage-tickets
POST   /api/v1/triage-tickets/{id}/assignee
POST   /api/v1/triage-tickets/{id}/investigate
POST   /api/v1/triage-tickets/{id}/disposition

POST   /api/v1/datasets
GET    /api/v1/datasets/{id}
POST   /api/v1/datasets/{id}/frames
DELETE /api/v1/datasets/{id}/frames/{frame_id}
POST   /api/v1/datasets/{id}/seal
POST   /api/v1/datasets/{id}/release
```

所有错误使用统一信封：

```json
{ "error": { "code": "campaign_capacity_exceeded", "message": "...", "request_id": "req-..." } }
```

## 目录结构

```text
cmd/server                     HTTP 入口与优雅关闭
internal/apperr                错误分类、sentinel 与传输映射
internal/clock                 运营时区、业务日与时间窗口
internal/config                环境变量配置
internal/domain                实体、状态机、不变量与值对象
internal/repository            持久化接口与过滤器
internal/storage/sqlite        真实 SQL 实现、迁移与事务
internal/service/auth          登录、会话与角色
internal/service/fleet         计划、排班、出车、结算
internal/service/dataloop      采集、校验、处置、数据集
internal/audit                 审计事件记录
internal/idem                  幂等键生命周期
internal/worker                后台任务、重试与退避
internal/httpapi               路由、视图与统一响应
internal/middleware            请求 ID、恢复、日志、超时、鉴权
internal/testsupport           集成测试装配
internal/storage/sqlite/migrations  版本化 SQL 迁移
```

## 数据模型

17 张表，含主外键、唯一约束与业务索引：`operators`、`sessions`、`campaigns`、`vehicles`、
`assignments`、`drive_sessions`、`takeover_events`、`settlements`、`audit_events`、
`idempotency_keys`、`capture_batches`、`capture_frames`、`triage_tickets`、`datasets`、
`dataset_members`、`worker_jobs`、`schema_migrations`。

迁移从空库可建，重复启动幂等；数据库中出现未知的更高版本时启动会被拒绝而不是静默降级。

## 验证

```bash
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go test ./... -count=1
GOTOOLCHAIN=local go test -race ./... -count=1
```

或使用 Makefile：`make build`、`make vet`、`make test`、`make race`。

## 容器

```bash
docker build -t drviercar:local .
docker run --rm -p 8080:8080 -e DRVIERCAR_ADDR=0.0.0.0:8080 drviercar:local
```
