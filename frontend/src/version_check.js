export function versionCheckButtonState({ checking, error, upToDate }) {
  if (checking) return 'checking'
  if (error) return 'error'
  if (upToDate) return 'up-to-date'
  return 'idle'
}

export function createVersionCheckRunner({ backend, onKnown, onSnapshot, onError }) {
  let pending = false
  return {
    get pending() { return pending },
    async run(manual) {
      if (pending) return false
      pending = true
      try {
        const known = await backend.VersionState?.()
        if (known) onKnown?.(known)
        if (!backend.CheckVersion) throw new Error('版本检查服务暂不可用')
        await backend.CheckVersion()
        const result = await backend.VersionState?.()
        if (!result) throw new Error('版本检查结果暂不可用')
        if (result.checkError) throw new Error(result.checkError)
        onSnapshot(result, manual)
        return result
      } catch (cause) {
        onError(cause instanceof Error ? cause.message : String(cause))
        return undefined
      } finally {
        pending = false
      }
    },
  }
}
