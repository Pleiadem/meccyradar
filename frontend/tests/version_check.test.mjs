import test from 'node:test'
import assert from 'node:assert/strict'
import { createVersionCheckRunner, versionCheckButtonState } from '../src/version_check.js'

const snapshot = (overrides = {}) => ({ info: { configured: false, version: '', downloadUrl: '', notes: '', force: false }, currentVersion: '1.0.0', requiredVersion: '', downloadUrl: '', blocked: false, checking: false, checkError: '', ...overrides })
const deferred = () => {
  let resolve
  let reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

test('button state transitions from idle to checking and shows no-update only after success', () => {
  assert.equal(versionCheckButtonState({ checking: false, error: '', upToDate: false }), 'idle')
  assert.equal(versionCheckButtonState({ checking: true, error: '', upToDate: false }), 'checking')
  assert.equal(versionCheckButtonState({ checking: false, error: '', upToDate: true }), 'up-to-date')
})

test('failure remains retryable and checking takes precedence while a retry is active', () => {
  assert.equal(versionCheckButtonState({ checking: false, error: '网络不可用', upToDate: false }), 'error')
  assert.equal(versionCheckButtonState({ checking: true, error: '网络不可用', upToDate: false }), 'checking')
})

test('manual request stays pending until result arrives and ignores duplicate clicks', async () => {
  const check = deferred()
  let checks = 0
  const seen = []
  const runner = createVersionCheckRunner({ backend: { VersionState: async () => snapshot(), CheckVersion: () => { checks += 1; return check.promise } }, onSnapshot: (value) => seen.push(value), onError: assert.fail })
  const first = runner.run(true)
  assert.equal(runner.pending, true)
  assert.equal(await runner.run(true), false)
  assert.equal(checks, 1)
  check.resolve()
  assert.equal((await first).info.configured, false)
  assert.equal(runner.pending, false)
  assert.equal(seen.length, 1)
})

test('successful unconfigured result is a successful no-update check, not an error', async () => {
  const errors = []
  const delivered = []
  const runner = createVersionCheckRunner({ backend: { CheckVersion: async () => {}, VersionState: async () => snapshot() }, onSnapshot: (value) => delivered.push(value), onError: (message) => errors.push(message) })
  assert.equal((await runner.run(true)).info.configured, false)
  assert.equal(delivered.at(-1).info.configured, false)
  assert.deepEqual(errors, [])
})

test('update result is delivered immediately with manual intent while settings can remain open', async () => {
  const delivered = []
  const update = snapshot({ info: { configured: true, version: '2.0.0', downloadUrl: 'https://example.invalid', notes: '', force: false } })
  const runner = createVersionCheckRunner({ backend: { CheckVersion: async () => {}, VersionState: async () => update }, onSnapshot: (value, manual) => delivered.push({ value, manual }), onError: assert.fail })
  assert.equal((await runner.run(true)).info.version, '2.0.0')
  assert.equal(delivered.at(-1).value.info.version, '2.0.0')
  assert.equal(delivered.at(-1).manual, true)
})

test('pre-check snapshot only synchronizes gate and cannot trigger a stale manual update prompt', async () => {
  const prompts = []
  const knownUpdate = snapshot({ info: { configured: true, version: '9.0.0', downloadUrl: 'https://example.invalid', notes: '', force: false } })
  const authoritative = snapshot({ info: { configured: false, version: '', downloadUrl: '', notes: '', force: false } })
  let reads = 0
  const backendRunner = createVersionCheckRunner({ backend: { VersionState: async () => ++reads === 1 ? knownUpdate : authoritative, CheckVersion: async () => {} }, onKnown: (value) => prompts.push({ type: 'gate', value }), onSnapshot: (value, manual) => prompts.push({ type: 'prompt', value, manual }), onError: assert.fail })
  const result = await backendRunner.run(true)
  assert.equal(result.info.configured, false)
  assert.equal(prompts.filter((item) => item.type === 'prompt').length, 1)
  assert.equal(prompts.find((item) => item.type === 'prompt').value.info.configured, false)
})

test('failed authoritative state read releases pending runner and allows retry', async () => {
  let reads = 0
  const errors = []
  const runner = createVersionCheckRunner({ backend: { VersionState: async () => { reads += 1; if (reads === 2) throw new Error('结果读取失败'); return snapshot() }, CheckVersion: async () => {} }, onSnapshot: () => {}, onError: (message) => errors.push(message) })
  assert.equal(await runner.run(true), undefined)
  assert.equal(runner.pending, false)
  assert.deepEqual(errors, ['结果读取失败'])
  assert.equal((await runner.run(true)).info.configured, false)
  assert.equal(runner.pending, false)
})

test('failed check reports error and never delivers stale no-update as success', async () => {
  const delivered = []
  const errors = []
  const runner = createVersionCheckRunner({ backend: { VersionState: async () => snapshot(), CheckVersion: async () => { throw new Error('网络不可用') } }, onSnapshot: (value) => delivered.push(value), onError: (message) => errors.push(message) })
  assert.equal(await runner.run(true), undefined)
  assert.deepEqual(errors, ['网络不可用'])
  assert.equal(delivered.length, 0)
})
