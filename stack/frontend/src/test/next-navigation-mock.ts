import { vi } from "vitest";

export const push = vi.fn();
export const refresh = vi.fn();

export function useRouter() {
  return { push, refresh };
}
