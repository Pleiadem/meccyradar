# MeccyRadar

## 本地登录测试

启动独立 Mock 管理端：

```bash
go run ./cmd/mockserver
```

默认配置：

- 管理端：`http://127.0.0.1:8766`（真实 RentForge）
- 未设置 `MECCYRADAR_MANAGEMENT_URL` 时一律走这个默认值

> 默认值**不是** mock。连到 mock 时任何真实卡密都会被判无效，
> 报错会显示成"卡密无效"，很容易误判成卡密本身有问题。

用真实服务时直接启动客户端即可：

```bash
wails dev
```

需要跑自测（mock）时，显式覆盖地址：

```bash
export MECCYRADAR_MANAGEMENT_URL=http://127.0.0.1:18080
wails dev
```

Mock 的默认配置：

- 测试卡密：`CARD-1`
- Mock 返回的 FRP 节点：`127.0.0.1:7000`

然后在登录页输入：

```text
CARD-1
```

Mock 管理端只负责鉴权、心跳和租约；没有启动 FRPS 时，登录可以测试，但启动隧道会因节点不可用而失败。

自定义地址或卡密：

```bash
go run ./cmd/mockserver -addr 127.0.0.1:18080 -card CARD-TEST
```

## About

This is the official Wails React-TS template.

You can configure the project by editing `wails.json`. More information about the project settings can be found
here: https://wails.io/docs/reference/project-config

## Live Development

To run in live development mode, run `wails dev` in the project directory. This will run a Vite development
server that will provide very fast hot reload of your frontend changes. If you want to develop in a browser
and have access to your Go methods, there is also a dev server that runs on http://localhost:34115. Connect
to this in your browser, and you can call your Go code from devtools.

## Building

To build a redistributable, production mode package, use `wails build`.
