import { vi } from "vitest";

export const push = vi.fn();
export const replace = vi.fn();
export const refresh = vi.fn();
export const back = vi.fn();

export function useRouter() {
  return { push, replace, refresh, back };
}
