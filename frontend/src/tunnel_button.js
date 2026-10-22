// 隧道按钮的可用性判定。
//
// 抽成纯函数是为了能脱离浏览器直接单测：这是"到期后还能不能点连接"
// 这类业务规则的唯一出处，散在 JSX 里就没法验，也容易被后续改动改坏。
//
// 规则（四条都有测试钉住）：
//   1. 已连接 / 连接中 → 永远可点（断开不该被授权状态影响）
//   2. 未登录 → 不可点
//   3. 授权已过期（剩余 <= 0）且空闲 → 不可点，提示"授权已过期，无法连接"
//   4. 授权未过期且空闲 → 可点

/**
 * 计算剩余秒数。
 *
 * 优先用后端算好的 expiresIn；没有就按 expireAtUnix 现算。
 * 不能只认 expiresIn：手动登录后 setInit 里只有 expireAtUnix，
 * expiresIn 要等 1 秒的 ticker 才补上，那 1 秒内按钮会闪一下禁用。
 */
export function remainingSeconds(state) {
  if (typeof state?.expiresIn === 'number') return state.expiresIn
  if (state?.expireAtUnix) return Math.max(0, state.expireAtUnix - Math.floor(Date.now() / 1000))
  return 0
}

/** 隧道是否处于"已经在跑"的状态。 */
function isRunning(status) {
  return status === 'connected' || status === 'connecting'
}

/** 授权是否已过期。未登录也算过期（没授权就谈不上可连接）。 */
export function isExpired(state) {
  if (!state?.authenticated) return true
  return remainingSeconds(state) <= 0
}

/**
 * 按钮是否可点。
 *
 * 注意：已连接时必须是 true，否则用户点不了"停止隧道"，
 * 一个过期账号会被永久卡在连接状态里。
 */
export function canToggleTunnel(state) {
  if (isRunning(state?.tunnelStatus)) return true
  if (!state?.authenticated) return false
  return remainingSeconds(state) > 0
}

/** 按钮文字。 */
export function tunnelButtonLabel(state) {
  if (state?.tunnelStatus === 'connecting') return '连接中…'
  if (state?.tunnelStatus === 'connected') return '停止隧道'
  return '启动隧道'
}

/**
 * hover 提示。可点时返回 undefined（不显示 tooltip）。
 */
export function tunnelButtonHint(state) {
  if (canToggleTunnel(state)) return undefined
  if (!state?.authenticated) return '请先登录'
  return '授权已过期，无法连接'
}
