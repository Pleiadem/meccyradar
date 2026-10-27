# AGENTS.md

## 项目规则

- 项目是 Go + Wails v2 + React + TypeScript + Tailwind CSS v4。
- 前端入口保持 `frontend/src/App.tsx`，全局样式保持 `frontend/src/index.css`。
- 主组件必须使用 `export default function App()`。
- 可复用 UI 组件放在 `App.tsx` 底部，除非后续明确拆分文件。
- React 动画只使用 `motion/react`。
- 图标只使用 `lucide-react`，禁止手写 SVG 图标。
- 样式优先使用 Tailwind utility；项目级 token 写入 `index.css` 的 `@theme`。
- 重要交互必须有 hover、focus、active 和 reduced-motion 状态。
- 用户明确给出的远程视频 URL 必须原样保留。

## 常用命令

在项目根目录执行：

```bash
make test
make lint
make build
make smoke
```

前端单独执行：

```bash
npm run build --prefix frontend
```

Windows 默认构建（必须使用 Wails build tags）：

```bash
make build
# 或
make build-windows
```

输出：`./build/meccyradar.exe`。不要直接使用普通 `GOOS=windows go build`，否则 Wails 会因缺少 `production` 构建标签弹出错误。

## 目录说明

```text
.
├── App.tsx / frontend/src/App.tsx  # React 页面和组件
├── frontend/src/index.css          # Tailwind v4 token 和全局样式
├── frontend/vite.config.ts         # Vite 和 Tailwind 插件
├── main.go                         # Wails 入口和窗口配置
├── app.go                          # Wails App 生命周期
├── Design.md                       # 视觉规范和组件契约
├── SPEC.md                         # 产品技术规格
├── TASKS.md                        # 开发任务清单
├── DEFINITION_OF_DONE.md           # 完成标准
└── Makefile                        # 统一检查入口
```

## 禁止事项

- 不得恢复或引入旧的 `frpmgr` 架构。
- 不得在前端写死真实 FRP 服务器、Token、远程端口或生产凭据。
- 不得把完整敏感链接、Token 或卡密写入日志、截图或提交内容。
- 不得给视频增加遮罩、渐变、暗色 tint 或其他覆盖层。
- 不得删除移动端布局或把 Hero 强制显示在移动端。
- 不得用 `any` 逃避 TypeScript 类型检查。
- 不得提交 `node_modules`、`dist`、临时 EXE 或本地密钥。
- 不得把仅在 macOS 验证的结果描述为 Windows 真机验证。
- 不得只运行单个局部构建就声称项目完成。
