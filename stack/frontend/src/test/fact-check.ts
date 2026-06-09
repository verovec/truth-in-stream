import { vi } from "vitest";

export type BackendRoute = {
  match: (url: string, init?: RequestInit) => boolean;
  responses: (() => Response | Promise<Response>)[];
};

export const json = (status: number, body: unknown) => () =>
  new Response(JSON.stringify(body), { status });

export function stubBackend(routes: BackendRoute[]) {
  return vi
    .spyOn(globalThis, "fetch")
    .mockImplementation((input, init?: RequestInit) => {
      const url = String(input);
      const route = routes.find((r) => r.match(url, init));
      if (!route) {
        throw new Error(`unexpected fetch: ${url}`);
      }
      const next =
        route.responses.length > 1
          ? route.responses.shift()
          : route.responses[0];
      if (!next) {
        throw new Error(`no response scripted for: ${url}`);
      }
      return Promise.resolve(next());
    });
}

export const submitRoute = (
  ...responses: BackendRoute["responses"]
): BackendRoute => ({
  match: (url, init) => url.endsWith("/api/videos") && init?.method === "POST",
  responses,
});

export const statusRoute = (
  ...responses: BackendRoute["responses"]
): BackendRoute => ({
  match: (url) => url.endsWith("/status"),
  responses,
});

export const resultsRoute = (
  ...responses: BackendRoute["responses"]
): BackendRoute => ({
  match: (url) => url.endsWith("/results"),
  responses,
});
