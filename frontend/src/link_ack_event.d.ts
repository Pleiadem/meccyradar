export type LinkAckState = {
  authenticated: boolean
  leaseId?: string
  linkVersion?: number
  copyPending?: boolean
}

export type LinkAckEvent = {
  accepted: boolean
  leaseId: string
  linkVersion: number
}

export function applyLinkAckEvent<T extends LinkAckState>(state: T, event: LinkAckEvent): T
