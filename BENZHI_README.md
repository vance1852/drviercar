# BENZHI_README

## 项目说明

- 项目：vance1852/drviercar
- 项目用途：自动驾驶路测运营与车端数据闭环平台。系统同时承载两条互相依赖的业务链：
- Go 工具链：`golang:1.22`
- 前端工具链：无

## 标准构建、运行和测试命令

进入容器后执行：

```bash
# 编译
cd '/app' && GOTOOLCHAIN=local go build ./...

# 启动
cd '/app' && GOTOOLCHAIN=local go run ./cmd/server

# 测试
cd '/app' && GOTOOLCHAIN=local go test ./...
```

## Docker 构建和进入容器

```bash
chmod +x build_benzhi_docker.sh
./build_benzhi_docker.sh benzhi-task-112-amd64 linux/amd64
./build_benzhi_docker.sh benzhi-task-112-arm64 linux/arm64
docker run -it benzhi-task-112-amd64:latest
docker run -it --platform linux/arm64 benzhi-task-112-arm64:latest
```

## 题目验证命令

1. 预期退出码 0：`go test ./internal/worker -run '^TestSessionPurgeKeepsLiveSessions$' -count=1`
2. 预期退出码 0：`go test ./...`
3. 预期退出码 0：`GOTOOLCHAIN=local go build -buildvcs=false ./... && GOTOOLCHAIN=local go vet ./...`
