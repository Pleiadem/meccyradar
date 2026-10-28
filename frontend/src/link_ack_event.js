export function applyLinkAckEvent(state, event) {
  if (!state || !event?.accepted || !state.authenticated) return state
  if (state.leaseId !== event.leaseId || state.linkVersion !== event.linkVersion) return state
  if (!state.copyPending) return state
  return { ...state, copyPending: false }
}
