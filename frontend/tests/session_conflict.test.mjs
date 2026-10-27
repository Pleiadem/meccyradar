import test from 'node:test'
import assert from 'node:assert/strict'
import { alreadyAuthenticatedMessage, confirmSameActiveSessionConflict, isSameActiveSessionConflict } from '../src/session_conflict.js'

test('同一卡密恢复先提交时，已登录冲突可确认是已登录而非错误', () => {
  const restoredState = { authenticated: true, tunnelStatus: 'connected' }
  assert.equal(isSameActiveSessionConflict(alreadyAuthenticatedMessage, 'CARD-A', restoredState, 'CARD-A'), true)
  // The check is independent of whether the session:restored event rendered
  // before or after this asynchronous login rejection is handled.
  assert.equal(isSameActiveSessionConflict(alreadyAuthenticatedMessage, ' CARD-A ', restoredState, 'CARD-A'), true)
})

test('错误先于 session:restored 事件处理时，后端确认后仍恢复为成功状态', async () => {
  let restored = false
  let resolveState
  const state = new Promise((resolve) => { resolveState = resolve })
  const confirmation = confirmSameActiveSessionConflict(
    alreadyAuthenticatedMessage,
    'CARD-RESTORED',
    () => state,
    async () => 'CARD-RESTORED',
  )
  // Simulate the restored event arriving while the frontend handles the
  // rejected manual call and awaits the authoritative backend snapshot.
  restored = true
  resolveState({ authenticated: restored, tunnelStatus: 'connected' })
  assert.deepEqual(await confirmation, { authenticated: true, tunnelStatus: 'connected' })
})

test('不同卡密、未认证状态与其他真实错误都不能被当作成功', () => {
  const restoredState = { authenticated: true, tunnelStatus: 'connected' }
  assert.equal(isSameActiveSessionConflict(alreadyAuthenticatedMessage, 'CARD-B', restoredState, 'CARD-A'), false)
  assert.equal(isSameActiveSessionConflict(alreadyAuthenticatedMessage, 'CARD-A', { authenticated: false }, 'CARD-A'), false)
  assert.equal(isSameActiveSessionConflict('网络连接失败', 'CARD-A', restoredState, 'CARD-A'), false)
  assert.equal(isSameActiveSessionConflict(alreadyAuthenticatedMessage, '', restoredState, 'CARD-A'), false)
})
