# MeccyRadar Tasks

## 阶段一：项目初始化和窗口

- [x] 锁定 Go、Wails 和 FRP 具体版本。
- [x] 安装 React 19、TypeScript、Tailwind v4、motion/react、lucide-react。
- [x] 配置 760x480、Frameless、DisableResize。
- [x] 创建 40px 无边框标题栏和 Wails 拖拽区域。
- [x] 完成登录页和控制台双栏静态 UI。
- [x] 配置暗色主题、Inter、JetBrains Mono 和 reduced-motion。

验证：前端构建通过，Wails macOS 构建通过，Windows x64 交叉编译通过。

## 阶段二：客户端状态和本地会话

- [x] 创建 `internal/config`。
- [x] 抽象 `CredentialStore`。
- [x] Windows 使用 DPAPI，Mac 开发使用 Keychain 或测试实现。
- [x] 保存、读取、清除卡密会话。
- [x] 区分首次手动登录和缓存自动登录。
- [x] 增加 Windows 单实例 Mutex 和已有窗口唤回。

验证：会话可保存、读取、清除；重复启动不创建第二个实例。

## 阶段三：管理端鉴权客户端

- [x] 创建 `internal/auth`。
- [x] 管理端 URL 通过构建配置或环境配置注入，不写死服务器节点。
- [x] 实现 `POST /api/client/login`。
- [x] 解析卡密类型、到期时间、租约、shareBaseUrl 和结构化 FRP 配置。
- [x] 实现 HTTP 409 占线错误转译。
- [x] 实现 `POST /api/client/heartbeat`。
- [x] 实现 `POST /api/client/logout`。
- [x] 心跳失败、租约失效时通知主流程停止隧道。
- [x] 前端 DTO 脱敏，不返回 serverAddr、Token、remotePort。

验证：使用本地 Mock 管理端测试成功、过期、409、心跳失败和数据缺失。

## 阶段四：内嵌 FRP 引擎

- [x] 创建 `internal/frpengine`。
- [x] 使用 `github.com/fatedier/frp/client`。
- [x] 使用 `source.NewConfigSource()` 和 `source.NewAggregator()`。
- [x] 在内存中创建 `v1.ClientCommonConfig`。
- [x] 组装 TCP `v1.TCPProxyConfig`。
- [x] 组装 HTTP `v1.HTTPProxyConfig`。
- [x] HTTP 必须配置 `customDomains` 或 `subdomain`。
- [x] 保留后续 `v1.ProxyConfigurer` 扩展入口。
- [x] 实现 Start、Stop、Running 状态。
- [x] 接入 FRP 全局日志 Writer，并恢复旧 Logger，避免污染其他模块。

验证：Mock 或真实测试 FRPS 下 TCP、HTTP 隧道启动和停止；非法配置返回明确错误。

## 阶段五：监测、倒计时和事件

- [x] 创建 `internal/monitor`。
- [x] 对管理端返回的 FRP 节点执行 3 次 TCP 测速，取最低值。
- [x] 每 3 秒探测 `127.0.0.1:13133`。
- [x] 端口检测不阻止链接生成。
- [x] 实现客户端到期看门狗。
- [x] 每秒或合理频率推送剩余时间。
- [x] 推送 `tunnel:status`、`port:status`、`log:append`。
- [x] 正确处理 idle、connecting、connected、expired、conflict、error。

验证：模拟到期、端口变化、测速失败和心跳失败，确认 UI 状态与日志同步。

## 阶段六：Wails API 和链接

- [x] 实现 `GetInitState()`。
- [x] 实现 `LoginByCard()`。
- [x] 实现 `StartTunnel()`。
- [x] 实现 `StopTunnel()`。
- [x] 实现 `CopyShareLink(password string) error`。
- [x] 实现 `Logout()`。
- [x] 固定查找代理名 `radar_tcp`。
- [x] 使用 Go `url.QueryEscape` 编码地址和密码。
- [x] 只写系统剪贴板，不把完整链接返回给 React。

验证：输出严格包含 `%3A`，React 调用结果不包含完整链接。

## 阶段七：React 控制台

- [x] 登录页支持卡密掩码和显示切换。
- [x] 展示鉴权错误、过期和占线提示。
- [x] 主控台显示动态剩余时间和精确到期时间。
- [x] 实现连接大开关和 connecting 状态。
- [x] 延迟只显示毫秒，不显示服务器地址和远程端口。
- [x] 显示本地 13133 状态。
- [x] 实现雷达密码输入和复制成功反馈。
- [x] 实现常驻自动滚底日志框。
- [x] 绑定最小化、关闭、退出登录。

验证：手动登录、自动登录、启动、停止、复制、退出和错误状态均可操作。

## 阶段八：联调和发布

- [x] 搭建本地 Mock 管理端。
- [x] 完成首次登录流程测试。
- [x] 完成重启自动登录和自动启动测试。
- [x] 完成单卡多开 409 测试。
- [x] 完成固定远程端口测试。
- [x] 完成服务端到期和客户端到期双重断连测试。
- [x] 完成 TCP/HTTP FRP 实际连接测试（本机 FRPS v0.71.0 数据面联调）。
- [ ] 完成 Windows 10/11 真机测试。
- [ ] 配置 Windows WebView2 策略并测试离线/国内网络场景。
- [ ] 生成绿色版 EXE，必要时使用 `wails build -clean -platform windows/amd64 -upx`。
- [ ] 检查 EXE 启动、单实例、窗口唤回、剪贴板、网络和退出清理。

最终验收命令：

```bash
npm run build --prefix frontend
wails build -clean -platform windows/amd64
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test ./...
git diff --check
```

Mac 上不把 Windows-only 行为测试结果当作已验证，必须在 Windows 10/11 真机或虚拟机完成最终验收。
