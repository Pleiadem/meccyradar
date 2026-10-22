// 隧道按钮可用性的单测。
//
// 用 node --test 直接跑源码（tunnel_button.js 是纯函数、无 DOM 依赖），
// 不需要浏览器或打包。运行：cd frontend && node --test tests/
//
// 为什么这些用例重要：卡密到期后如果还能点"启动隧道"，用户会看到
// FRPC 的英文错误（login to the server failed: <nil>...）。这里把
// "到期就不该让你点"这条规则钉死在小尺寸纯函数上。

import { test } from 'node:test'
import assert from 'node:assert/strict'

import {
  canToggleTunnel,
  isExpired,
  remainingSeconds,
  tunnelButtonHint,
  tunnelButtonLabel,
} from '../src/tunnel_button.js'

const future = () => Math.floor(Date.now() / 1000) + 3600
const past = () => Math.floor(Date.now() / 1000) - 60

test('到期前 + 空闲 → 连接按钮可点', () => {
  const state = { authenticated: true, tunnelStatus: 'idle', expiresIn: 3600 }
  assert.equal(canToggleTunnel(state), true)
  assert.equal(tunnelButtonHint(state), undefined)
  assert.equal(tunnelButtonLabel(state), '启动隧道')
})

test('到期后 + 空闲 → 连接按钮禁用，提示授权已过期', () => {
  for (const expiresIn of [0, -1, -86400]) {
    const state = { authenticated: true, tunnelStatus: 'idle', expiresIn }
    assert.equal(canToggleTunnel(state), false, `expiresIn=${expiresIn} 不该可点`)
    assert.equal(tunnelButtonHint(state), '授权已过期，无法连接')
    assert.equal(isExpired(state), true)
  }
})

test('已连接 → 断开按钮可点，不受到期影响', () => {
  // 这条最关键：过期账号如果按不到"停止隧道"，就会被永久卡在连接状态。
  for (const expiresIn of [3600, 0, -86400]) {
    const connected = { authenticated: true, tunnelStatus: 'connected', expiresIn }
    assert.equal(canToggleTunnel(connected), true, `已连接且 expiresIn=${expiresIn} 必须能断开`)
    assert.equal(tunnelButtonLabel(connected), '停止隧道')
  }
  const connecting = { authenticated: true, tunnelStatus: 'connecting', expiresIn: -1 }
  assert.equal(canToggleTunnel(connecting), true, '连接中也必须能中断')
  assert.equal(tunnelButtonLabel(connecting), '连接中…')
})

test('未登录 → 连接按钮禁用', () => {
  const state = { authenticated: false, tunnelStatus: 'idle' }
  assert.equal(canToggleTunnel(state), false)
  assert.equal(tunnelButtonHint(state), '请先登录')
})

test('未登录且 expiresIn 是 undefined → 禁用而不是误判为可点', () => {
  // ?? 0 兜底：undefined 绝不能当成"还有时间"。
  assert.equal(remainingSeconds({ authenticated: true }), 0)
  assert.equal(canToggleTunnel({ authenticated: true, tunnelStatus: 'idle' }), false)
})

test('只有 expireAtUnix 时也能正确判断（手动登录后的第一秒）', () => {
  // 登录响应里只有 expireAtUnix，expiresIn 要等 ticker 补上；
  // 这一秒内不能闪成"禁用"。
  const fresh = { authenticated: true, tunnelStatus: 'idle', expireAtUnix: future() }
  assert.equal(canToggleTunnel(fresh), true)
  assert.ok(remainingSeconds(fresh) > 0)

  const expired = { authenticated: true, tunnelStatus: 'idle', expireAtUnix: past() }
  assert.equal(canToggleTunnel(expired), false)
  assert.equal(remainingSeconds(expired), 0, '过期后剩余秒数不应为负')
})

test('expiresIn 优先于 expireAtUnix（后端下发的值更权威）', () => {
  // 管理员改了期限后后端推新的 expiresIn，此时 expireAtUnix 可能还没同步。
  const state = {
    authenticated: true,
    tunnelStatus: 'idle',
    expiresIn: 7200,
    expireAtUnix: past(),
  }
  assert.equal(canToggleTunnel(state), true)
  assert.equal(remainingSeconds(state), 7200)
})

test('边界：剩余正好为 0 视为已过期', () => {
  const state = { authenticated: true, tunnelStatus: 'idle', expiresIn: 0 }
  assert.equal(canToggleTunnel(state), false)
  assert.equal(isExpired(state), true)
})
