// tunnel_button.js 的类型声明。
//
// 项目开了 noImplicitAny，纯 .js 模块没有声明会让 tsc 报错；
// 而用 .js 而不是 .ts 是为了让 Node 能直接 import 做单测（不经过编译）。

export type TunnelButtonState = {
  authenticated?: boolean
  tunnelStatus?: string
  expiresIn?: number
  expireAtUnix?: number
}

export function remainingSeconds(state: TunnelButtonState): number
export function remainingTimeLabel(state: TunnelButtonState): string
export function isExpired(state: TunnelButtonState): boolean
export function canToggleTunnel(state: TunnelButtonState): boolean
export function tunnelButtonLabel(state: TunnelButtonState): string
export function tunnelButtonHint(state: TunnelButtonState): string | undefined
