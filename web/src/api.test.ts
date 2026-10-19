import { afterEach, describe, expect, it, vi } from "vitest";
afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
});
describe("single-use local session bootstrap", () => {
  it("removes the token before exchange and deduplicates callers outside React lifecycles", async () => {
    const location = { hash: "#token=secret-one", pathname: "/", search: "" };
    const calls: string[] = [];
    const replaceState = vi.fn(() => {
      calls.push("remove");
      location.hash = "";
    });
    const fetch = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) => {
        calls.push("request");
        return new Response(
          JSON.stringify({
            ok: true,
            data: { csrf_token: "csrf-one", version: "0.3.0" },
            error: null,
          }),
          { status: 200 },
        );
      },
    );
    vi.stubGlobal("location", location);
    vi.stubGlobal("history", { replaceState });
    vi.stubGlobal("fetch", fetch);
    const { session, post } = await import("./api");
    const first = session(),
      second = session();
    expect(first).toBe(second);
    await first;
    expect(calls).toEqual(["remove", "request"]);
    expect(replaceState).toHaveBeenCalledWith(null, "", "/");
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls[0][0]).toBe("/api/v1/session");
    await post("/test", { action: "add" });
    const request = fetch.mock.calls.at(-1)![1] as RequestInit;
    expect(new Headers(request.headers).get("X-CSRF-Token")).toBe("csrf-one");
    expect(request.credentials).toBe("same-origin");
  });
  it("uses cookie session read on reload without placing secrets in a URL", async () => {
    vi.stubGlobal("location", { hash: "", pathname: "/", search: "" });
    vi.stubGlobal("history", { replaceState: vi.fn() });
    const fetch = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(
          JSON.stringify({
            ok: true,
            data: { csrf_token: "csrf", version: "0.3.0" },
          }),
        ),
    );
    vi.stubGlobal("fetch", fetch);
    const { session } = await import("./api");
    await session();
    expect(fetch.mock.calls[0][0]).toBe("/api/v1/session");
    expect((fetch.mock.calls[0][1] as RequestInit).method).toBeUndefined();
  });
});
describe("typed envelope client", () => {
  it("preserves the backend code for SVN authentication rather than treating any401 as local session expiration", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async (_input: RequestInfo | URL, _init?: RequestInit) =>
          new Response(
            JSON.stringify({
              ok: false,
              error: {
                code: "authentication",
                message: "SVN credentials required",
              },
              exit_code: 1,
            }),
            { status: 401 },
          ),
      ),
    );
    const { api, ApiError } = await import("./api");
    await expect(api("/projects")).rejects.toMatchObject({
      status: 401,
      code: "authentication",
      message: "SVN credentials required",
    });
    try {
      await api("/projects");
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError);
    }
  });
  it("passes cancellation signals through superseded reads", async () => {
    const fetch = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(JSON.stringify({ ok: true, data: [] })),
    );
    vi.stubGlobal("fetch", fetch);
    const { api } = await import("./api");
    const controller = new AbortController();
    await api("/jobs", { signal: controller.signal });
    expect((fetch.mock.calls[0][1] as RequestInit).signal).toBe(
      controller.signal,
    );
  });
  it("reports invalid server responses explicitly", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async (_input: RequestInfo | URL, _init?: RequestInit) =>
          new Response("not JSON", { status: 502 }),
      ),
    );
    const { api } = await import("./api");
    await expect(api("/jobs")).rejects.toMatchObject({
      status: 502,
      code: "invalid_response",
    });
  });
});
