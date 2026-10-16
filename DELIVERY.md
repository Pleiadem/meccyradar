# MeccyRadar Delivery Guide

## 状态

当前仓库已完成任务 1–8 中可在现有 macOS 环境验证的实现。代码支持 Windows 目标交叉编译，但 Windows 10/11 真机行为尚未在本环境验证。

## 安装依赖

### Go

需要 Go 1.22+，项目当前模块使用 Go 1.25 工具链兼容配置。

```bash
go mod download
```

### 前端

```bash
npm ci --prefix frontend
```

### Wails

需要 Wails v2.15.0 CLI。项目 Makefile 会优先使用 PATH 中的 `wails`，否则使用本机默认安装路径。

## 配置

管理端地址通过环境变量配置：

```bash
export MECCYRADAR_MANAGEMENT_URL=https://management.example.com
```

生产环境必须使用 HTTPS。仅 localhost/127.0.0.1 允许 HTTP，便于本地 Mock 测试。

管理端需要提供：

- `POST /api/client/login`
- `POST /api/client/heartbeat`
- `POST /api/client/logout`
- 结构化 FRP 配置
- 固定 `radar_tcp` 代理
- 租约、心跳间隔和到期时间

客户端不会写死 FRP 服务器、端口、Token 或远程端口。

## 运行

### 本地 Mock 登录测试

```bash
go run ./cmd/mockserver
```

默认测试卡密为 `CARD-1`，管理端地址为 `http://127.0.0.1:18080`。另开终端设置：

```bash
export MECCYRADAR_MANAGEMENT_URL=http://127.0.0.1:18080
wails dev
```

登录页输入 `CARD-1`。该 Mock 不包含真实生产凭据；未启动 FRPS 时，只能验证登录、占线、登出和会话流程，隧道启动会失败。

开发模式：

```bash
wails dev
```

生产构建：

```bash
make build
```

macOS 构建产物：

```text
build/bin/meccyradar.app
```

Windows 交叉编译：

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags='-H windowsgui -s -w' \
  -o build/meccyradar.exe .
```

## 测试与质量门禁

```bash
make test
make lint
make build
make smoke
```

专项测试覆盖：

- 会话保存、读取和清除
- 管理端登录、心跳、登出和 409 占线
- FRP TCP/HTTP 内存配置
- 非法 FRP 配置
- 节点三次测速和本地端口监测
- 心跳失败、到期停止回调
- 雷达链接 `%3A` 编码
- 固定查找 `radar_tcp`
- 手动登录不自动启动
- 登出清除本地会话

## 部署

发布 Windows 绿色版时分发构建生成的 EXE 及其必要运行环境。Wails/WebView2 依赖和单实例、DPAPI、剪贴板行为必须在 Windows 10/11 目标机器验证后发布。

生产环境必须：

- 使用 HTTPS 管理端
- 不在前端或日志写入 Token、服务器地址和远程端口
- 使用管理端动态下发配置
- 提供可用的 FRPS 节点和固定远程端口

## 已知限制

1. 当前开发环境为 macOS，无法执行 Windows 测试二进制。
2. Windows 10/11 真机、WebView2、DPAPI、Windows 单实例唤回和 Windows 剪贴板尚未完成实机验证。
3. 真实管理端 URL、签名/Header 契约和生产 FRPS 节点尚未提供，只完成了配置化客户端契约和本地 Mock。
4. TCP/HTTP 真实 FRP 数据面已通过本机 FRPS v0.71.0 联调测试；生产 FRPS 节点尚未提供，生产网络行为仍需部署环境复验。
5. 本地 Mock 的 FRP 节点地址仅用于契约测试，不能作为生产配置。
6. 阶段八中的 Windows 真机相关项目仍待真实环境完成；自动登录、客户端/租约到期停止、本地 Mock 联调及本机 FRPS TCP/HTTP 数据面已有自动化测试。

## 回滚

任务按阶段提交，可按提交回滚：

```bash
git log --oneline --decorate

git revert <commit>
```

当前任务提交：

```text
Task 1  63799eb
Task 2  dcf290a / fd21b20
Task 3  a8565a7
Task 4  0074110
Task 5  051196c
Task 6  2616751
Task 7  4c806bb
Task 8  4214d29 / f52ee11 / 4f413db / 9939bd5 / bcd3201
```

后续质量修复（未改变产品范围）：包含 FRP 日志脱敏、异常退出联动处理和本机 FRPS TCP/HTTP 数据面集成测试。

回滚后必须重新执行：

```bash
make test
make lint
make build
make smoke
```
