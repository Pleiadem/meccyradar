# Definition of Done

一个任务只有同时满足以下条件，才可以标记为完成。

## 功能

- 用户需求中的每个功能都有对应实现。
- 组件 API、状态和视觉契约与 `Design.md` 一致。
- 表单有真实的输入、校验、提交反馈和错误可见性。
- 交互状态包含初始、悬停、聚焦、按下、成功和失败状态。
- 移动端、桌面端布局均符合需求。

## 代码

- `frontend/src/App.tsx` 保持 `export default function App()`。
- TypeScript 无错误，不用 `any` 绕过类型系统。
- 不包含密钥、Token、服务器凭据或敏感业务数据。
- 不引入未记录的依赖。
- 无旧架构残留和无关临时代码。

## 视觉

- 使用 `Design.md` 中的颜色、圆角、字体和间距规则。
- 视频地址准确，视频没有遮罩或 tint。
- Tailwind v4 token 正常生效。
- 文字和控件满足可读性与键盘 focus 要求。
- `prefers-reduced-motion` 下不会产生强制动画。

## 验证

以下命令必须执行并通过：

```bash
make test
make lint
make build
make smoke
```

另外，Wails 项目需要执行目标平台构建。Windows 行为必须在 Windows 10/11 真机或虚拟机验证，不能用 macOS 交叉编译替代。

## 交付

最终报告必须明确区分：

- 已实现内容
- 已通过的检查
- 尚未验证或被环境阻塞的内容
