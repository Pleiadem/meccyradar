# MeccyRadar 桌面客户端 Spec

## 1. 产品定位

MeccyRadar 是 Windows 10/11 上的绿色便携式 FRP 桌面客户端。客户端不写死服务器、端口、Token 或代理配置，所有授权和 FRP 连接参数由服务端根据卡密动态返回。

## 2. 最终决策

- 技术栈：Go 1.22+、Wails v2、React 19、TypeScript、Tailwind CSS v4、motion/react、lucide-react。
- FRP：直接内嵌 `github.com/fatedier/frp/client`，不使用旧项目架构，不运行外部 frpc.exe。
- FRP 版本：初始化时锁定一个具体版本，例如 `v0.71.0`，后续升级必须显式验证。
- MVP 代理：TCP、HTTP；数据模型保留 `v1.ProxyConfigurer` 扩展能力。
- 窗口：760x480、无边框、禁止缩放、深色横版双栏。
- 发布：Windows 绿色便携版，最终在 Windows 10/11 实机验证。
- 远程端口：由服务端下发固定端口。客户端重启后继续使用相同端口。
- 单卡多开：同一卡密在线时拒绝新设备，提示“当前卡密已在其他设备运行，请先退出原设备”。
- 到期：服务端踢下线，客户端本地倒计时归零后主动停止 FRP，双端保险。

## 3. 服务端职责

服务端负责：

- 卡密验证、启用、禁用、续期和到期判断。
- 卡密类型和功能权限。
- 当前授权 FRP 服务器选择。
- FRP serverAddr、serverPort、user、token 和代理配置。
- 固定 remotePort。
- 雷达链接基础地址。
- 单卡在线租约、心跳和释放。
- 服务端到期断连和 Token 失效。

客户端只接收结构化 JSON，不接收或保存本地 `.toml` 配置文件。

## 4. 鉴权返回契约

```json
{
  "code": 200,
  "msg": "ok",
  "data": {
    "appName": "MeccyRadar",
    "shareBaseUrl": "http://radar.fastcheat.top/",
    "expireAtUnix": 1794736800,
    "expireAtStr": "2026-11-15 18:00",
    "session": {
      "leaseId": "temporary-session-id",
      "heartbeatInterval": 30,
      "leaseExpireAtUnix": 1794736830
    },
    "frpConfig": {
      "serverAddr": "example.com",
      "serverPort": 7000,
      "user": "card-user",
      "token": "server-issued-token",
      "meta": { "card_key": "card-key" },
      "proxies": [
        {
          "name": "radar_tcp",
          "type": "tcp",
          "localIP": "127.0.0.1",
          "localPort": 13133,
          "remotePort": 8000
        }
      ]
    }
  }
}
```

客户端前端 DTO 不包含真实 `serverAddr`、FRP Token 或远程端口。敏感连接配置只保留在 Go 内存中。

接口约定：

- `POST /api/client/login`
- `POST /api/client/heartbeat`
- `POST /api/client/logout`
- 重复在线返回 HTTP 409。
- 心跳失败或租约失效时停止隧道。

## 5. 客户端核心流程

### 首次启动

1. 读取本地加密会话。
2. 没有会话则显示卡密登录页。
3. 手动登录成功后保存会话，但不自动启动隧道。

### 再次启动

1. 读取本地加密卡密。
2. 自动请求服务端重新鉴权。
3. 成功后进入控制台并自动启动隧道。
4. 失败则回到登录页并显示原因。

### 启动隧道

1. 使用服务端返回的 FRP 结构化配置。
2. Go 内存组装 `v1.ClientCommonConfig` 和代理配置。
3. 通过 `client.NewService` 启动内嵌 FRP。
4. 启动节点 TCP 延迟测试、本地端口监测、到期看门狗和心跳。

### 停止和到期

1. 停止心跳和本地监测。
2. 调用 FRP Service 的 `Close()`。
3. 到期时推送 `expired` 状态和日志。
4. 退出登录时停止隧道、释放租约、删除本地会话。

## 6. 雷达链接

链接只由 Go 后端生成并复制到系统剪贴板，不能返回给 React。

```text
http://radar.fastcheat.top/?a={ServerAddr}%3A{RemotePort}&k={Pwd}
```

Go 方法建议：

```go
CopyShareLink(password string) error
```

固定查找代理名 `radar_tcp`，禁止使用第一个 TCP/HTTP 代理作为兜底。

## 7. Wails 绑定方法

```go
GetInitState() InitStateDTO
LoginByCard(cardKey string) AuthResultDTO
StartTunnel() error
StopTunnel() error
CopyShareLink(password string) error
Logout() error
```

推荐事件：

- `tunnel:status`：idle、connecting、connected、expired、conflict、error，以及 pingMs、expiresIn。
- `port:status`：本地 13133 是否可连接。
- `log:append`：时间、级别、消息。

## 8. 界面

### 标题栏

- 高度 40px。
- Wails 拖拽区域。
- 品牌名、`:13133` 徽章。
- 退出登录、最小化、关闭按钮。

### 登录页

- 左侧 42%：品牌介绍和三步引导。
- 右侧 58%：卡密输入、明文/掩码切换、错误提示、登录按钮。

### 主控台

左侧：

- 授权剩余时间：天、小时、分钟动态切换。
- 精确到期时间。
- 圆形穿透开关：未启动、连接中、运行中。
- 节点延迟，只显示毫秒数，不显示服务器地址和端口。
- 本地 13133 端口状态。

右侧：

- 雷达密码输入，允许留空。
- 一键复制雷达链接。
- 常驻实时日志终端。

## 9. 安全和存储

- Windows 使用 DPAPI 保护本地卡密会话。
- Mac 开发环境使用 Keychain 或测试替代实现。
- 不将 FRP Token、服务器地址、远程端口写入前端状态、日志或本地配置文件。
- 服务端返回的 Token 仍会存在客户端内存中，核心权限必须由服务端控制。
- 所有网络请求使用 HTTPS，生产环境不允许跳过证书验证。

## 10. 验收标准

- 首次手动登录不自动启动隧道。
- 重启后自动登录并自动启动隧道。
- 服务端返回不同服务器时，客户端连接不同服务器，不修改客户端代码。
- 固定远程端口在有效期内不变化。
- 同一卡密第二设备收到 409 并拒绝启动。
- 心跳失败或服务端到期踢下线时，客户端停止隧道。
- 客户端倒计时归零时自动停止隧道。
- 雷达链接中 `:` 编码为 `%3A`，且前端拿不到完整链接返回值。
- TCP 和 HTTP 配置能正确组装并启动；HTTP 缺少域名时明确报错。
- 绿色版可在 Windows 10/11 启动并运行。
- 重复启动只唤回已有窗口，不创建第二个实例。
