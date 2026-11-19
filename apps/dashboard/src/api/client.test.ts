import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, session } from "./client";

const tokens = (n: number) => ({ access_token: `access-${n}`, refresh_token: `refresh-${n}`, token_type: "Bearer", expires_in: 900 });

function reply(status: number, body?: unknown) {
  return new Response(body === undefined ? null : JSON.stringify(body), { status });
}

describe("api client", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    session.clear();
  });
  afterEach(() => vi.unstubAllGlobals());

  it("sends the access token and decodes JSON", async () => {
    session.setTokens(tokens(1));
    fetchMock.mockResolvedValueOnce(reply(200, { ok: true }));
    expect(await api("/x", { query: { a: 1, skip: undefined } })).toEqual({ ok: true });
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(String(url)).toMatch(/\/api\/v1\/x\?a=1$/);
    expect(init.headers.Authorization).toBe("Bearer access-1");
  });

  it("refreshes once on 401 and retries with the new token", async () => {
    session.setTokens(tokens(1));
    fetchMock
      .mockResolvedValueOnce(reply(401, { error: { code: "UNAUTHORIZED", message: "expired" } }))
      .mockResolvedValueOnce(reply(200, tokens(2)))
      .mockResolvedValueOnce(reply(200, { ok: true }));
    expect(await api("/x")).toEqual({ ok: true });
    expect(fetchMock.mock.calls[2]![1].headers.Authorization).toBe("Bearer access-2");
    expect(session.refreshToken).toBe("refresh-2");
  });

  it("shares one refresh between concurrent requests", async () => {
    session.setTokens(tokens(1));
    let refreshCalls = 0;
    fetchMock.mockImplementation(async (url: URL | string, init: RequestInit) => {
      if (String(url).endsWith("/auth/refresh")) {
        refreshCalls++;
        await new Promise((r) => setTimeout(r, 10));
        return reply(200, tokens(2));
      }
      const auth = (init.headers as Record<string, string>).Authorization;
      return auth === "Bearer access-2" ? reply(200, { ok: true }) : reply(401);
    });
    await Promise.all([api("/a"), api("/b"), api("/c")]);
    expect(refreshCalls).toBe(1); // a second refresh would replay a rotated token
  });

  it("ends the session when refresh is rejected", async () => {
    session.setTokens(tokens(1));
    const ended = vi.fn();
    const off = session.onEnd(ended);
    fetchMock.mockResolvedValueOnce(reply(401)).mockResolvedValueOnce(reply(401));
    await expect(api("/x")).rejects.toMatchObject({ status: 401 });
    expect(ended).toHaveBeenCalledOnce();
    expect(session.hasSession).toBe(false);
    off();
  });

  it("raises ApiError with the envelope's code and details", async () => {
    fetchMock.mockResolvedValueOnce(
      reply(422, { error: { code: "VALIDATION_FAILED", message: "invalid", details: { name: "required" } } }),
    );
    const err = await api("/x", { method: "POST", body: {} }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 422, code: "VALIDATION_FAILED", details: { name: "required" } });
  });
});
