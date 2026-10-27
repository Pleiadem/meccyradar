export const alreadyAuthenticatedMessage = '当前已登录，请先退出当前会话'

// The message alone is not enough to classify a login failure: only suppress it
// when the backend confirms the active session is the same requested card.
export function isSameActiveSessionConflict(errorMessage, requestedCard, state, savedCard) {
  return errorMessage === alreadyAuthenticatedMessage
    && state?.authenticated === true
    && typeof requestedCard === 'string'
    && requestedCard.trim() !== ''
    && requestedCard.trim() === savedCard
}

export async function confirmSameActiveSessionConflict(errorMessage, requestedCard, getState, getSavedCard) {
  if (errorMessage !== alreadyAuthenticatedMessage) return undefined
  try {
    const [state, savedCard] = await Promise.all([getState(), getSavedCard()])
    return isSameActiveSessionConflict(errorMessage, requestedCard, state, savedCard) ? state : undefined
  } catch {
    return undefined
  }
}
