export declare const alreadyAuthenticatedMessage: string
export declare function isSameActiveSessionConflict(errorMessage: string, requestedCard: string, state: { authenticated?: boolean } | undefined, savedCard: string): boolean
export declare function confirmSameActiveSessionConflict<T extends { authenticated?: boolean }>(errorMessage: string, requestedCard: string, getState: () => Promise<T | undefined>, getSavedCard: () => Promise<string>): Promise<T | undefined>
