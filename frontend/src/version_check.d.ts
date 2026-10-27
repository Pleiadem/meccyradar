export type VersionCheckSnapshot = {
  info: { configured: boolean; version: string; downloadUrl: string; notes: string; force: boolean }
  currentVersion: string
  requiredVersion: string
  downloadUrl: string
  blocked: boolean
  checking: boolean
  checkError: string
}
export function versionCheckButtonState(state: { checking: boolean; error: string; upToDate: boolean }): 'checking' | 'error' | 'up-to-date' | 'idle'
export function createVersionCheckRunner(options: {
  backend: { VersionState?: () => Promise<VersionCheckSnapshot>; CheckVersion?: () => Promise<void> }
  onKnown?: (snapshot: VersionCheckSnapshot) => void
  onSnapshot: (snapshot: VersionCheckSnapshot, manual: boolean) => void
  onError: (message: string) => void
}): {
  readonly pending: boolean
  run(manual: boolean): Promise<VersionCheckSnapshot | false | undefined>
}
