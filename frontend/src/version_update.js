export function compareNumericVersions(left, right) {
  const valid = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/
  const a = valid.exec(left)
  const b = valid.exec(right)
  if (!a || !b) return null
  for (let index = 1; index <= 3; index += 1) {
    if (a[index].length !== b[index].length) return a[index].length < b[index].length ? -1 : 1
    if (a[index] !== b[index]) return a[index] < b[index] ? -1 : 1
  }
  return 0
}

export function isVersionSkipped(storage, version) {
  try { return storage.getItem('meccyradar:skipped-version') === version } catch { return false }
}

export function skipVersion(storage, version) {
  if (!/^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(version)) return false
  try { storage.setItem('meccyradar:skipped-version', version); return true } catch { return false }
}

export function shouldOfferVersion({ currentVersion, version, force, skipped, manual }) {
  const comparison = compareNumericVersions(version, currentVersion)
  return comparison !== null && comparison > 0 && (Boolean(force) || Boolean(manual) || !skipped)
}
