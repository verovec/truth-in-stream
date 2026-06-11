// Shared HTTP helpers for the typed backend clients. ApiError carries the
// backend's status code so callers can branch on it (e.g. 409 -> pending),
// and toApiError surfaces the backend's JSON `error` message when present.

// API_BASE is empty in deployed environments (the ALB serves /api/* same-origin)
// and points at the backend container in local dev.
export const API_BASE = process.env.NEXT_PUBLIC_API_URL ?? "";

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export async function toApiError(response: Response): Promise<ApiError> {
  const fallback = `request failed with status ${response.status}`;
  try {
    const body: unknown = await response.json();
    if (
      typeof body === "object" &&
      body !== null &&
      "error" in body &&
      typeof body.error === "string"
    ) {
      return new ApiError(body.error, response.status);
    }
  } catch {
    // Non-JSON error body; fall through to the generic message.
  }
  return new ApiError(fallback, response.status);
}
