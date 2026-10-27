# MeccyRadar 架构说明

> 本文基于**实际代码**逐行核对写成，不是照着 SPEC.md 复述。
> 凡实现与文档/截图不一致的地方，第 9 节单独列出并标注「需确认」。
>
> 核对基准：`main` @ `90f35d1`

---

## 1. 项目简介

MeccyRadar 是一个 **Windows 桌面 FRP 客户端**，用来把本机某个端口（默认 `127.0.0.1:13133`）
通过自建 FRPS 服务器"穿透"到公网，供另一台机器或远程用户访问。

它本身**不产生任何内容**。它的定位是：用户本机已经有一个叫 **Fast 雷达** 的程序在
`127.0.0.1:13133` 上监听，MeccyRadar 负责把这条链路代理出去，并给出一键复制的分享链接。

客户端**不写死任何连接参数**：FRPS 地址、端口、user、token、远程端口、代理配置
全部由服务端（RentForge）在登录时按卡密动态下发。客户端只负责鉴权、建立隧道、
保活、到期处理。

技术栈：Go 1.22+ / Wails v2 / React 19 + TypeScript + Tailwind CSS v4。
FRP 是**编译进二进制**的库（`github.com/fatedier/frp/client`，锁定 v0.71.0），
不依赖外部 `frpc.exe`，不需要额外运行时。

---

## 2. 核心概念

| 概念 | 含义 | 权威方 |
|---|---|---|
| **卡密（cardKey）** | 用户购买的一串授权码，唯一身份凭证 | RentForge |
| **租约（lease）** | 一次登录产生的会话，`leaseId` 有 90 秒 TTL，需心跳续期。一个卡密同时只有一个 active 租约 | RentForge |
| **FRP 配置** | `{serverAddr, serverPort, user, token, proxies[]}`，登录时下发 | RentForge |
| **本地端口** | 本机上被代理的端口，默认 `13133`。Fast 雷达监听这里 | 用户本机（不由客户端控制） |
| **远程端口（remotePort）** | FRPS 服务器上对外暴露的端口，客户端连它拿数据 | RentForge 分配 |
| **隧道** | 客户端 ↔ FRPS 的 FRP 连接。跑起来后 `本机:13133 → FRPS:remotePort` | 客户端 |
| **心跳** | 每 30 秒一次 `POST /api/client/heartbeat`，续租 + 拉取权威到期时间 | 双向 |
| **顶替（takeover）** | 同一卡密在另一台设备登录时，服务端释放旧租约，旧设备心跳收到 401 | RentForge |
| **到期** | 卡密 `expires_at` 到达。服务端踢下线 + 客户端本地倒计时归零停隧道（双保险） | 双向 |

一句话概括数据流：

```
Fast 雷达 :13133  ──► FRPC(内嵌) ──FRP协议──► FRPS:remotePort ──► 远程浏览器/客户端
        （用户程序）      （本客户端）           （自建服务器）        （雷达页面）
```

---

## 3. 架构图

### 3.1 进程与层

```
┌─────────────────────────────────────────────────────────────────┐
│ Windows 进程（单实例，Wails 打包）                                │
│                                                                 │
│  ┌───────────────────────────┐   ┌───────────────────────────┐  │
│  │ 前端（WebView2）           │   │ Go 主进程                  │  │
│  │ React 19 + TS + Tailwind  │   │                           │  │
│  │                           │◄──┤ app.go        Wails 绑定   │  │
│  │ App.tsx                   │事件│                           │  │
│  │  ├ WindowShell（标题栏）   │──►│ ┌───────────────────────┐ │  │
│  │  ├ LoginView（未登录）     │   │ │ internal/monitor      │ │  │
│  │  └ ConsoleView（已登录）   │   │ │  心跳·测速·端口·到期   │ │  │
│  │ tunnel_button.js（纯逻辑） │   │ └───────────────────────┘ │  │
│  └───────────────────────────┘   │ ┌───────────────────────┐ │  │
│                                   │ │ internal/frpengine    │ │  │
│                                   │ │  内嵌 frp client      │ │  │
│                                   │ └───────────────────────┘ │  │
│                                   │ ┌───────────────────────┐ │  │
│                                   │ │ internal/auth         │ │  │
│                                   │ │  login/heartbeat/...  │ │  │
│                                   │ └───────────────────────┘ │  │
│                                   │ ┌───────────────────────┐ │  │
│                                   │ │ internal/config       │ │  │
│                                   │ │  session.dat（DPAPI） │ │  │
│                                   │ └───────────────────────┘ │  │
│                                   └─────────┬─────────────────┘  │
└─────────────────────────────────────────────┼───────────────────┘
                                              │
              ┌───────────────────────────────┼────────────────────┐
              │                               │                    │
              ▼ HTTPS/HTTP                    ▼ FRP 协议           ▼ 本地 TCP
   ┌──────────────────────┐        ┌──────────────────┐  ┌──────────────────┐
   │ RentForge 管理端      │        │ FRPS 服务器       │  │ Fast 雷达         │
   │ 43.138.163.188:10003 │        │ (自建)           │  │ 127.0.0.1:13133  │
   │                      │        │                  │  │                  │
   │ POST /client/login   │        │ 对外暴露          │  │ 用户自己的程序     │
   │ POST /client/heartbeat│       │ :remotePort      │  │ 客户端不启动它     │
   │ POST /client/logout  │        │                  │  │                  │
   └──────────────────────┘        └──────────────────┘  └──────────────────┘
```

### 3.2 前端视图切换

**只有一个窗口、一个页面**，靠 `init.authenticated` 切换两个视图组件（不是两个 HTML 页面）：

```
                    ┌──────────────────────┐
                    │ App.tsx              │
                    │  hydrated? ──否──► "正在加载…"
                    └──────────┬───────────┘
                               │ 是
                    ┌──────────▼───────────┐
                    │ WindowShell（标题栏） │  常驻：最小化 / 关闭
                    │                      │  仅在已登录时显示：品牌名 + 退出登录
                    └──────────┬───────────┘
                               │
                authenticated? │
              ┌────────────────┴────────────────┐
              ▼                                 ▼
      ┌───────────────┐                 ┌───────────────┐
      │ LoginView     │                 │ ConsoleView   │
      │ 左：品牌介绍   │                 │ 左：授权/延迟  │
      │ 右：卡密表单   │                 │    隧道/端口/按钮│
      │               │                 │ 右：密码/复制  │
      │               │                 │    /运行日志   │
      └───────────────┘                 └───────────────┘
```

---

## 4. 业务流程

### 4.1 登录

```
用户输入卡密
   │
   ▼
前端 handleLogin → backend.LoginByCard(cardKey)
   │
   ▼
App.LoginByCard (app.go:401)
   │  auth.Client.Login → POST /api/client/login  {cardKey}
   ▼
RentForge 校验卡密/状态/到期 → 分配或复用 FRPS 资源 → 创建租约
   │
   ▼ 返回 {"code":200,"data": AuthData}
   │
   ▼
validateAuthData 校验必需字段（appName/shareBaseUrl/expireAtUnix/
session.leaseId/frpConfig.serverAddr/port/token/proxies 非空）
   │
   ▼
store.Save(Session{CardKey, RadarPassword})   ← DPAPI 加密写盘
   │
   ▼
内存保存 authData（含 token，不出 Go）
   │
   ▼
返回 AuthResultDTO{appName, expireAtStr, expireAtUnix, tunnelStatus:"idle"}
   │
   ▼
前端切到 ConsoleView（**不自动启动隧道**，符合 SPEC）
```

**登录返回的数据结构**（`internal/auth/client.go:52`）：

```go
type AuthData struct {
    AppName      string    `json:"appName"`
    ShareBaseURL string    `json:"shareBaseUrl"`   // 客户端实际不信任它，见 9.3
    ExpireAtUnix int64     `json:"expireAtUnix"`   // 卡密到期时刻
    ExpireAtStr  string    `json:"expireAtStr"`    // 北京时间 "YYYY-MM-DD HH:MM"
    Session      Session   `json:"session"`
    FRPConfig    FRPConfig `json:"frpConfig"`
}

type Session struct {
    LeaseID           string `json:"leaseId"`
    HeartbeatInterval int    `json:"heartbeatInterval"`  // 秒，服务端给 30
    LeaseExpireAtUnix int64  `json:"leaseExpireAtUnix"`  // 租约 TTL，服务端给 +90s
}

type FRPConfig struct {
    ServerAddr string            `json:"serverAddr"`
    ServerPort int               `json:"serverPort"`
    User       string            `json:"user"`
    Token      string            `json:"token"`      // 只存 Go 内存
    Meta       map[string]string `json:"meta,omitempty"`
    Proxies    []Proxy           `json:"proxies"`
}

type Proxy struct {
    Name          string   `json:"name"`       // "radar_tcp" 是雷达用的那个
    Type          string   `json:"type"`       // "tcp" | "http"
    LocalIP       string   `json:"localIP"`    // 127.0.0.1
    LocalPort     int      `json:"localPort"`  // 13133
    RemotePort    int      `json:"remotePort"` // 服务端分配的对外端口
    CustomDomains []string `json:"customDomains,omitempty"`
    Subdomain     string   `json:"subdomain,omitempty"`
}
```

### 4.2 连接（启动隧道）

```
用户点"启动隧道"
   │
   ▼
前端 toggleTunnel → backend.StartTunnel()
   │
   ▼
App.startTunnel (app.go:444)
   │
   ├─(0) refreshStaleLeaseBeforeStart：租约明知失效就先静默重登
   │
   ├─(1) frpengine.NewWithExit(frpConfig, onLog, onExit)
   │        └─ buildConfig 把 DTO 转成 frp 的 v1.ClientCommonConfig
   │           + []v1.ProxyConfigurer
   │           LoginFailExit = true
   │           Metadatas["token"] = token   ← 注意：走 metadata，不是 auth.token
   │
   ├─(2) engine.Start()  → client.NewService(...).Run()
   │        └─ 内嵌 frp 建立到 serverAddr:serverPort 的连接
   │
   ├─(3) 构造 monitor.Config 并 monitor.Start()
   │
   └─(4) 返回 nil，前端置 tunnelStatus="connecting"
```

`monitor.run` 起来后依次做：

1. `probePort` 立即探一次本地端口
2. 同步测速 `measurePing`（10 次采样，去极值取平均）
3. 首次心跳
4. 进入循环：心跳（30s）/ 端口探测（3s）/ 到期检查（1s）

**FRP 代理配置的实际内容**（`internal/frpengine/engine.go:193`）：

```go
common := &v1.ClientCommonConfig{
    ServerAddr:    config.ServerAddr,
    ServerPort:    config.ServerPort,
    User:          config.User,
    LoginFailExit: &loginFailExit,        // 登录失败直接退出，不重试
    Metadatas:     map[string]string{"token": config.Token},
}
// 每个 proxy：
base := v1.ProxyBaseConfig{
    Name: proxy.Name, Type: proxy.Type,
    ProxyBackend: v1.ProxyBackend{LocalIP: proxy.LocalIP, LocalPort: proxy.LocalPort},
}
// type=="tcp"  → v1.TCPProxyConfig{RemotePort: proxy.RemotePort}
// type=="http" → v1.HTTPProxyConfig{CustomDomains, SubDomain}
```

即：**`127.0.0.1:13133` → FRPS 的 `remotePort`**。TCP 走端口，HTTP 走域名。

### 4.3 心跳

```
每 30 秒（服务端下发的 heartbeatInterval）
   │
   ▼
auth.Client.Heartbeat(leaseId) → POST /api/client/heartbeat
   │
   ▼ 成功：{"status":"ok","lease_id":"…","expires_at":"2026-12-31T23:59:59+08:00"}
   │
   ├─ heartbeatTracker.onResult(nil)  → leaseGuard.markHealthy()  租约本地估算 +90s
   └─ applyExpiresAt(expires_at)      → 到期时间跟上服务端
                                        （变了就回调 UpdateExpiry → 刷界面）

   ▼ 失败，按类型分三条路：
   │
   ├─ 网络类（TransportError：超时/断网/DNS）
   │     └─ retryHeartbeat：2s → 4s → 8s 共 3 次
   │          成功 → 继续；全失败 → 停隧道
   │
   ├─ 业务类 401（StatusError）
   │     ├─ reason=ttl / migrate / unknown → recoverLease 静默重登（最多 3 次，
   │     │     连续 3 个回合都失败就放弃）；重登后配置变了会重启 FRPC
   │     ├─ reason=takeover → **绝不重登**，日志高亮"已在其他设备登录，本机已停止"
   │     └─ reason=logout   → 不重登（用户自己关的会话不该被拉起来）
   │
   └─ 其他 → 停隧道并上报原文
```

### 4.4 到期检测

**双保险**（客户端本地 + 服务端）：

```
客户端：
  monitor.expireAtUnix（初值 = 登录时 expireAtUnix，之后每次心跳被刷新）
       │
       ├─ 每 1 秒 expiryTicker 检查 expiresIn <= 0  → expire() → stopTunnel
       └─ 每次心跳前也检查一次（所以改期限后不必等满 1 秒）

服务端：
  卡密到期后，租约不再续期 / 心跳返回 401 → 客户端停隧道
  FRPS Panel 也会在到期后禁用用户，断开隧道（第三重）
```

到期时前端收到 `tunnel:status{status:"expired"}`，左侧"隧道状态"显示**已断开**（红色）。

### 4.5 换服 / 配置变更

设置面板的「更换服务器」调用 `POST /api/client/change-server`，请求带 `cardKey + leaseId`。
服务端复用登录的卡密校验，且要求 leaseId 是该卡未过期的 active 租约。每卡 60 秒冷却。
客户端只发起换服请求并接收服务端返回的新 envelope；服务器负责目标服务器、端口、Panel 用户和 Token。
RentForge 在返回 envelope 前等待新用户出现在 Panel 读取/鉴权索引，客户端不负责等待同步或创建资源。

成功语义是「新激活，但保留期限」：新选另一台可用服务器，生成新用户名、token、端口，
`expires_at` 和备注沿用原卡；本地切到新资源并签新租约，然后删除旧服务器用户。
成功响应和 `/client/login` 使用相同 envelope，并可能携带 `warning`；客户端复用 `AuthData`
解析，刷新 `leaseGuard` 后自动重连；雷达分享链接随新的 FRP 地址/端口更新，用户必须重新复制最新链接。

如果旧用户删除失败，换服仍可能已经成功，但服务端会返回 warning，并以
`cleanup_action=delete` 持久化待办，管理端 retry endpoint 会重试 **DELETE**。本地提交失败时
不能删除旧资源；目标资源补偿失败时必须保留可恢复的 cleanup 意图。
后台 `migrate_card` 也是目标提交成功后删除旧用户，和客户端自助换服的区别主要是目标由管理员指定，
而客户端自动选择另一台可用服务器。

后台管理端改服（`migrate_card`）则是管理员指定目标服务器，旧用户删除；两者语义不同。


客户端设置：标题栏齿轮打开 Glass 模态框，包含开机自启、更换服务器和退出登录。
开机自启偏好持久化在 `UserConfigDir()/meccyradar/settings.json`，与加密凭据分开；
Windows 系统侧的快捷方式是 `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\MeccyRadar.lnk`，
指向当前 exe 并传 `--autostart`。启动时会核对设置与快捷方式是否一致并修复缺失/残留/旧路径。
不开注册表、不安装 Windows 服务。非 Windows 平台显示不支持。

### 4.6 退出（三条路，行为不同）

| 动作 | 行为 | 是否释放租约 | 是否清凭据 |
|---|---|---|---|
| 点窗口 X（已登录） | 隐藏窗口，隧道继续 | ❌ | ❌ |
| 点窗口 X（未登录） | 真正退出进程 | — | ❌ |
| 托盘「退出」/ `Quit()` | 断开 → 释放租约 → 退出 | ✅（重试 2 次） | ❌ |
| 「退出登录」`Logout()` | 停隧道 → 释放租约 → 清 session | ✅ | ✅ |
| Ctrl+C / SIGTERM | `Quit()` | ✅ | ❌ |
| 任务管理器强杀 | 无处理 | ❌（等 90s TTL 回收） | ❌ |

---

## 5. 模块说明

| 包/文件 | 职责 | 关键点 |
|---|---|---|
| `main.go` | 进程入口 | WebView2 预检、760×480 无边框、单实例锁、关闭即隐藏 |
| `app.go` | Wails App 层，所有前端可调方法 + 事件推送 | 唯一持有 `authData`（含 token）的地方 |
| `app_lease.go` | 租约自愈：`leaseGuard` / `recoverLease` / `refreshStaleLeaseBeforeStart` / `applyServerExpiry` | 重登有界，takeover 不重登 |
| `app_log.go` | 日志清洗：剥时间戳/run id、英文错误换中文、噪音丢弃 | 返回 `""` 表示该行不显示 |
| `tray_icon.go` / `tray_windows.go` / `tray_other.go` | 系统托盘（仅 Windows） | 图标来自 `build/tray/*.png`（由 SVG 渲染） |
| `webview2_*.go` | 精简版 Windows 的 WebView2 缺失提示 | 必须在 `wails.Run` 之前检查 |
| `internal/auth` | HTTP 客户端 + 错误分类 | `StatusError`（业务）/ `TransportError`（网络）/ `ConflictError`（409）；HTTPS 强制，明文仅白名单 |
| `internal/config` | 会话持久化 | `%AppData%\meccyradar\session.dat`，Windows 用 DPAPI |
| `internal/frpengine` | 内嵌 frp client 封装 | DTO → frp 配置；日志脱敏；退出回调 |
| `internal/monitor` | 心跳、测速、端口探测、到期看门狗 | 所有周期性行为的唯一出处 |
| `internal/diagnosis` | 把 FRPC 英文错误分类成结构化提示 | 规则顺序敏感 |
| `internal/mockserver` + `cmd/mockserver` | 本地假管理端，仅自测 | 默认 `127.0.0.1:18080`，卡密 `CARD-1` |
| `frontend/src/App.tsx` | 全部 UI | 单页双视图 |
| `frontend/src/tunnel_button.js` | 连接按钮可用性纯逻辑 | 可被 `node --test` 直接测 |

### 事件与绑定方法

**Go → 前端（事件）**

| 事件 | 载荷 | 时机 |
|---|---|---|
| `tunnel:status` | `TunnelStatusDTO{status, pingMs?, message?, done?, timeout?}` | 状态变化、每轮测速、每秒到期检查 |
| `port:status` | `PortStatusDTO{open}` | 每次端口探测（3s） |
| `log:append` | `LogEventDTO{message, at:"HH:MM:SS"}` | 引擎日志、客户端自身日志 |
| `tunnel:diagnosis` | `DiagnosisDTO{level, title, hint?, raw?}` | 命中诊断规则时 |
| `session:restored` | `InitStateDTO` | 自动登录成功、到期时间被心跳刷新 |

**前端 → Go（绑定方法）**

```
GetInitState()              InitStateDTO
LoginByCard(cardKey)        AuthResultDTO
StartTunnel() / StopTunnel() error
RemeasurePing()             error
CopyShareLink(password)     error      // 链接只在 Go 侧生成并写剪贴板
Logout()                    error
CloseWindow()               void
RadarPassword() / SaveRadarPassword(pw)
SavedCardKey()              string
```

---

## 6. 前端 UI 状态机

### 6.1 顶层

```
未 hydrated ──► "正在加载…"
     │
     ▼
authenticated=false ──► LoginView
     │  LoginByCard 成功
     ▼
authenticated=true  ──► ConsoleView
     │  Logout
     └──► 回 LoginView
```

### 6.2 隧道状态（`tunnelStatus`）

后端 `emitStatus` 是唯一来源，前端只做展示映射：

| `tunnelStatus` | 前端显示 | 颜色 |
|---|---|---|
| `idle` | 未启动 | 灰 `text-white/45` |
| `connecting` | 连接中 | 黄 `text-amber-200` |
| `connected` | 运行中 | 绿 `text-emerald-200` |
| `error` | **已断开** | 红 `text-rose-200` |
| `expired` | 未启动（映射到 default 分支） | 灰 |

> ⚠️ **`connected` 并不代表 FRP 真的登录成功了。**
>
> `StatusConnected` 只有三处会发：`monitor.run` 里 1 秒一次到期 ticker
> （`monitor.go:294`）和测速成功事件（`monitor.go:381`/`412`）。
> 也就是说只要监控循环跑起来了，1 秒内界面就会显示"运行中"，**与 FRP 登录结果无关**。
>
> FRP 的真实结果走另一条路：`frpengine` 的 `onExit` 回调
> （`engine.go:98`，只在 `runErr != nil` 且非 `context.Canceled` 时触发）→
> `App.handleEngineExit` → `status="error"`。
>
> 所以存在一个短暂窗口：FRP 还在握手，界面已显示"运行中"。
> 登录失败时会被 `onExit` 拉回"已断开"。
>
> 这也解释了「隧道状态：运行中 + 本地端口：未监听」同时在目 ——
> 两者互不依赖：前者是客户端自己的循环在跑，后者是本机 13133 有没有程序在听。

### 6.3 连接按钮可用性（`tunnel_button.js`）

| 条件 | 可点 | 提示 |
|---|---|---|
| `connected` / `connecting` | ✅（必须能断开） | — |
| 未登录 | ❌ | 请先登录 |
| 已登录 + 已过期 + 空闲 | ❌ | 授权已过期，无法连接 |
| 已登录 + 未过期 + 空闲 | ✅ | — |

### 6.4 本地端口行

只受 `port:status` 事件驱动：`true → 已监听`（绿）/ `false → 未监听`（灰）。

> ⚠️ 这一行有已知问题，见 9.1 和 9.2。

---

## 7. 关键数据结构

### 7.1 凭据（落盘）

```go
// internal/config/session.go:20
type Session struct {
    CardKey       string `json:"cardKey"`
    SavedAtUnix   int64  `json:"savedAtUnix"`
    RadarPassword string `json:"radarPassword,omitempty"`
}
```

- 路径：`os.UserConfigDir()/meccyradar/session.dat`
  （Windows 即 `%AppData%\meccyradar\session.dat`）
- 加密：Windows `CryptProtectData`（DPAPI，绑定当前用户）；非 Windows 用本地文件密钥（仅开发用）
- 写入是"临时文件 + 0600 + fsync + rename"，避免写一半损坏
- **只存卡密和雷达密码**，不存 token / serverAddr / remotePort

### 7.2 前端可见状态

```go
// app.go:51
type InitStateDTO struct {
    Authenticated bool
    AppName       string
    ExpireAtStr   string   // 北京时间
    ExpireAtUnix  int64
    ExpiresIn     int64    // 后端从不填（见 9.4）
    TunnelStatus  string
}
```

**刻意不含**：token、serverAddr、remotePort —— 敏感连接参数不出 Go。

### 7.3 监控内部状态

```go
type Monitor struct {
    cfg          Config
    pinging      atomic.Bool   // 测速互斥
    mu           sync.Mutex
    cancel       context.CancelFunc
    done         chan struct{}
    running      bool
    leaseID      string        // 重登后会被换掉
    expireAtUnix int64         // 被心跳刷新，每秒被读 → 必须加锁
}
```

---

## 8. 文件组织

```
meccyradar/
├── main.go                    进程入口
├── app.go                     Wails 绑定 / 事件 / 生命周期
├── app_lease.go               租约自愈
├── app_log.go                 日志清洗
├── tray_*.go                  托盘（Windows/其他）
├── webview2_*.go              WebView2 运行时预检
├── internal/
│   ├── auth/client.go         管理端 HTTP 客户端 + 错误类型
│   ├── config/                session.dat 持久化 + 平台保护器
│   ├── frpengine/engine.go    内嵌 frp client 封装
│   ├── monitor/monitor.go     心跳/测速/端口/到期
│   ├── diagnosis/diagnosis.go FRPC 错误分类
│   └── mockserver/            假管理端
├── cmd/mockserver/            假管理端可执行入口
├── build/                     Wails 资源（appicon.svg/png、icon.ico、tray/）
├── scripts/gen_icon.py        SVG → PNG/ICO/托盘（resvg-py）
└── frontend/
    ├── src/App.tsx            全部 UI
    ├── src/tunnel_button.js   按钮可用性纯逻辑（+ .d.ts）
    ├── tests/*.test.mjs       node --test
    └── dist/                  （gitignored）构建产物，被 go:embed 进二进制
```

---

## 9. 实现与文档/截图不一致的地方（需确认）

### 9.1 ✅ 本地端口探测已改为独立循环并跟随代理配置

修复后，探测不再依赖 monitor/隧道是否运行，而是在**已登录时**单独启动，周期 2 秒；
退出登录时停止并把 UI 状态复位为「未监听」。

探测地址取雷达代理的 `localIP:localPort`；找不到代理或端口无效时回退到
`127.0.0.1:13133`。对应实现：`app_port.go` 的 `radarLocalAddr` / `runPortProbe`；
循环与 `App` 实例绑定，不使用会产生 race 的包级探测周期变量。

### 9.2 ✅ 停止隧道后端口状态继续刷新

独立探测循环在隧道停止时继续运行，Fast 雷达关闭/启动都能在最多 2 秒内反映到
「本地端口」行。只有退出登录才停探测并发一次 `false` 复位事件。

### 9.3 ⚠️ 截图里的「本地端口：已断开」在本代码库中不存在

当前代码的端口行只有两个文案：`已监听` / `未监听`。
`已断开` 只出现在**隧道状态**行（`tunnelStatus === 'error'` 时）。

可能原因（需确认）：
- 截图来自旧版本（`306b39f` 之前是"已启动/未启动"）
- 或用户把"隧道状态：已断开"看成了端口行

**请确认截图对应哪个版本 / 哪一行**，否则我会照着不存在的文案改。

### 9.4 `InitStateDTO.ExpiresIn` 后端从不填

`emitStatus` 的签名里没有 `ExpiresIn`（`app.go:652` 只有 status/pingMS/message/done/timeout），
`handleMonitorEvent` 也把它丢掉了。前端倒计时**完全靠自己**每秒用 `expireAtUnix`
现算（`App.tsx:155`）。字段存在但恒为 0，属于历史残留。

### 9.5 SPEC 与实现的其它出入

| SPEC 描述 | 实际实现 |
|---|---|
| 标题栏有 `:13133` 徽章 | 没有。标题栏只有品牌名 + 退出登录/最小化/关闭 |
| 主控台是"圆形穿透开关" | 是矩形按钮（`rounded-xl h-12`） |
| 「禁止使用第一个 TCP/HTTP 代理作为兜底」 | `findRadarProxy` 有第二段兜底：任意 `card_*_tcp` 命名的 TCP 代理也会被采用 |
| 「同一卡密在线时拒绝新设备（409）」 | 服务端已改为 **takeover 顶掉**（2026-10 变更）。客户端仍保留 `ConflictError` 处理 409 |
| 心跳失败即停隧道 | 现在网络类会先重试 3 次，业务类会先尝试静默重登 |
| 明文 HTTP 一律禁止 | 有窄白名单：本机 + `43.138.163.188` |

### 9.6 其它值得知道的实现细节

- **FRP token 走 metadata 不走 auth.token**（`engine.go:203` 注释）：
  该面板的插件从 `content.Metas["token"]` 取凭证，放 `auth.token` 会被拒两次。
- **`LoginFailExit = true`**：FRP 登录失败不重试，直接退出。
  所以"到期后还能点连接"会立刻看到 `login to the server failed` ——
  这正是上一轮加日志拦截 + 禁用按钮的原因。
- **`RemeasurePing` 是异步的**，同一时刻只允许一轮（`pinging` CAS），
  每轮**恰好**发一个 `Done=true` 的终态事件，前端凭它解除"测速中"。
- **测速连的是 FRPS 节点**（`serverAddr:serverPort`），不是 13133。
  所以"节点延迟"反映的是到服务器的 RTT，与本地雷达无关。

---

## 10. 已知限制与注意事项

1. **本地雷达不在跑时，隧道依然"连接成功"**。FRP 只负责端口映射，
   不会检查后端是否可连。所以 `隧道状态=运行中` + `本地端口=未监听`
   是完全可能的正常状态——**含义是"隧道通了，但本机 13133 没有程序在听"**。
   这两个状态互相独立。

2. **任务管理器强杀无法捕获**，租约要等 90 秒 TTL 才回收。期间重新登录会走 takeover。

3. **Windows 精简版可能缺 WebView2**。已在 `wails.Run` 前预检并给中文提示。

4. **开机自启快捷方式尚未在 Windows 实机验证**：PowerShell/WScript.Shell 创建逻辑、`StartHidden` 参数已实现并做 Windows 交叉编译；当前开发环境没有 Windows 桌面，快捷方式是否在真实启动时按预期隐藏窗口需实机确认。

4. **只支持 Windows 打包**（`wails build -platform windows/amd64`）。
   macOS 可用于开发（`tray_other.go` 提供空实现）。

5. **`frontend/dist` 被 gitignore 但被 `go:embed`**。
   改完前端必须 `npm run build`（或走 `wails build`），
   否则二进制里是旧界面。

6. **图标唯一源是 `build/appicon.svg`**，其余由 `scripts/gen_icon.py` 派生：

   ```bash
   env -u PYTHONPATH uv run --isolated --with resvg-py python scripts/gen_icon.py
   ```

7. **`hermes verify` 的 build 阶段会 `wails build -clean`，清空 `build/bin`**。
   出包顺序必须是 verify → `wails build` → zip，反了会交出不存在的 exe。

8. **测试里改了全局退避间隔**：`monitor.SetHeartbeatRetryBackoffForTest` 和
   `App.leaseRetryBackoff`。前者是 atomic，后者是实例字段，都是为了避免 `-race`。

---

## 11. 本地开发

```bash
# 假管理端（不需要 FRPS）
go run ./cmd/mockserver            # 127.0.0.1:18080，卡密 CARD-1

# 指向假服务端启动
export MECCYRADAR_MANAGEMENT_URL=http://127.0.0.1:18080
wails dev

# 测试
go test ./...
go test -race ./...
make lint && make smoke            # smoke 含前端单测
cd frontend && npm test

# 出包
hermes verify
wails build -platform windows/amd64
zip -j /tmp/meccyradar-windows-amd64.zip build/bin/meccyradar.exe
```

> 默认管理端是真实 RentForge（`43.138.163.188:10003`）。
> 连到 mock 会把任何真实卡密判成无效，报错长得像卡密本身有问题 —— 别搞混。

---

## 12. 待确认清单

| # | 问题 | 影响 |
|---|---|---|
| 1 | 截图"本地端口：已断开"是哪个版本 / 哪一行？ | 9.3，决定是否要加新文案 |
| 2 | 截图"本地端口：已断开"是哪个版本 / 哪一行？ | 9.3，决定是否要加新文案 |
| 3 | 端口状态应在退出登录后立即复位，还是等待下一轮探测？ | 目前会立即复位 |
| 4 | 端口探测周期 2 秒是否合适？ | 体验权衡 |
| 5 | `findRadarProxy` 的 `card_*_tcp` 兜底是否保留？ | 与 SPEC「禁止兜底」冲突 |
| 6 | 到期后是否要自动显示"重新登录"按钮（而不只是置灰）？ | UI 增强 |
