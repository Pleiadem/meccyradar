import test from 'node:test'
import assert from 'node:assert/strict'
import { applyLinkAckEvent } from '../src/link_ack_event.js'

test('accepted ack event clears only the matching displayed lease and version', () => {
  const state = { authenticated: true, leaseId: 'lease-b', linkVersion: 2, copyPending: true }
  const next = applyLinkAckEvent(state, { accepted: true, leaseId: 'lease-b', linkVersion: 2 })
  assert.equal(next.copyPending, false)
  assert.notEqual(next, state)
})

test('late ack from another lease cannot clear a same-version prompt', () => {
  const state = { authenticated: true, leaseId: 'lease-b', linkVersion: 2, copyPending: true }
  const next = applyLinkAckEvent(state, { accepted: true, leaseId: 'lease-a', linkVersion: 2 })
  assert.equal(next, state)
  assert.equal(next.copyPending, true)
})

test('rejected or stale-version ack does not clear the prompt', () => {
  const state = { authenticated: true, leaseId: 'lease-b', linkVersion: 3, copyPending: true }
  assert.equal(applyLinkAckEvent(state, { accepted: false, leaseId: 'lease-b', linkVersion: 3 }), state)
  assert.equal(applyLinkAckEvent(state, { accepted: true, leaseId: 'lease-b', linkVersion: 2 }), state)
})
