/** A failed store request's message. */
export const errorText = (error: unknown): string =>
  error instanceof Error && error.message ? error.message : String(error ?? '');

/** The store routes answer 503 while the server runs without its store. */
export const isStoreUnavailable = (error: unknown): boolean =>
  (error as { status?: number } | null)?.status === 503;
