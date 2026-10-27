export function compareNumericVersions(left: string, right: string): number | null
export function isVersionSkipped(storage: Pick<Storage, 'getItem'>, version: string): boolean
export function skipVersion(storage: Pick<Storage, 'setItem'>, version: string): boolean
export function shouldOfferVersion(input: { currentVersion: string; version: string; force: boolean; skipped: boolean; manual: boolean }): boolean
