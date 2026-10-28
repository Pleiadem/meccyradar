import test from 'node:test'
import assert from 'node:assert/strict'
import { sessionInvalidatedState } from '../src/session_state.js'

test('authoritative session invalidation returns the login-page state', () => {
  const next = sessionInvalidatedState()
  assert.deepEqual(next, { authenticated: false, tunnelStatus: 'idle' })
})
