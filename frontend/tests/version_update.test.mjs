import test from 'node:test'
import assert from 'node:assert/strict'
import { compareNumericVersions, isVersionSkipped, skipVersion, shouldOfferVersion } from '../src/version_update.js'

test('numeric version comparison rejects malformed values and orders numeric parts', () => {
  assert.equal(compareNumericVersions('1.10.0', '1.9.0'), 1)
  assert.equal(compareNumericVersions('1.0.0', '1.0.0'), 0)
  assert.equal(compareNumericVersions('1.0', '1.0.0'), null)
})

test('optional update skip persists, suppresses startup prompt, but manual check shows again', () => {
  const values = new Map()
  const storage = { getItem: (key) => values.get(key) ?? null, setItem: (key, value) => values.set(key, value) }
  assert.equal(skipVersion(storage, '1.2.0'), true)
  assert.equal(isVersionSkipped(storage, '1.2.0'), true)
  assert.equal(shouldOfferVersion({ currentVersion:'1.0.0', version:'1.2.0', force:false, skipped:true, manual:false }), false)
  assert.equal(shouldOfferVersion({ currentVersion:'1.0.0', version:'1.2.0', force:false, skipped:true, manual:true }), true)
  assert.equal(shouldOfferVersion({ currentVersion:'1.0.0', version:'1.2.0', force:true, skipped:true, manual:false }), true)
})

test('storage failure does not claim that optional update was skipped', () => {
  const storage = { setItem: () => { throw new Error('quota') }, getItem: () => null }
  assert.equal(skipVersion(storage, '1.2.0'), false)
})
