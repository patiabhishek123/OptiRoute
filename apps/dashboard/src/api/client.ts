// HTTP client for the OptiRoute API (/api/v1, spec §12).
//
// Tokens: the short-lived access token lives only in memory; the rotating refresh token is kept in
// sessionStorage so a reload stays logged in while closing the tab logs out. On a 401 the client
// refreshes once and retries. Concurrent requests share one refresh: the API rotates refresh tokens
// and treats a replayed one as theft, so two parallel refreshes would log the user out.

export const API_BASE = "/api/v1";
const REFRESH_KEY = "optiroute.refresh";

export interface TokenPair {
  access_token: string;
  refresh_token: string;
  token_type: string;
  expires_in: number;
}

/** Error envelope from the API: {"error": {"code", "message", "details"}}. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details: unknown;

  constructor(status: number, code: string, message: string, details?: unknown) {
    super(message);
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

type Listener = () => void;

class Session {
  private access: string | null = null;
  private refreshing: Promise<boolean> | null = null;
  private listeners = new Set<Listener>();

  get accessToken() {
    return this.access;
  }

  get refreshToken() {
    return sessionStorage.getItem(REFRESH_KEY);
  }

  get hasSession() {
    return this.access !== null || this.refreshToken !== null;
  }

  setTokens(t: TokenPair) {
    this.access = t.access_token;
    sessionStorage.setItem(REFRESH_KEY, t.refresh_token);
  }

  clear() {
    const had = this.hasSession;
    this.access = null;
    sessionStorage.removeItem(REFRESH_KEY);
    if (had) this.listeners.forEach((l) => l());
  }

  /** Called when the session ends (logout, or refresh rejected). */
  onEnd(l: Listener) {
    this.listeners.add(l);
    return () => {
      this.listeners.delete(l);
    };
  }

  /** Refresh the access token; all concurrent callers await the same request. */
  refresh(): Promise<boolean> {
    const token = this.refreshToken;
    if (!token) return Promise.resolve(false);
    this.refreshing ??= (async () => {
      try {
        const res = await fetch(`${API_BASE}/auth/refresh`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ refresh_token: token }),
        });
        if (!res.ok) {
          this.clear();
          return false;
        }
        this.setTokens((await res.json()) as TokenPair);
        return true;
      } catch {
        return false; // network error: keep the session, the next call retries
      } finally {
        this.refreshing = null;
      }
    })();
    return this.refreshing;
  }
}

export const session = new Session();

export interface RequestOptions {
  method?: string;
  body?: unknown; // JSON-encoded unless it is a string/FormData/Blob
  query?: Record<string, string | number | boolean | undefined | null>;
  headers?: Record<string, string>;
  contentType?: string;
}

/** Calls the API and returns the decoded JSON body (undefined for 204). */
export async function api<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const url = new URL(API_BASE + path, window.location.origin);
  for (const [k, v] of Object.entries(opts.query ?? {})) {
    if (v !== undefined && v !== null && v !== "") url.searchParams.set(k, String(v));
  }

  const send = () => {
    const headers: Record<string, string> = { ...opts.headers };
    let body: BodyInit | undefined;
    if (opts.body instanceof FormData || opts.body instanceof Blob || typeof opts.body === "string") {
      body = opts.body;
      if (opts.contentType) headers["Content-Type"] = opts.contentType;
    } else if (opts.body !== undefined) {
      body = JSON.stringify(opts.body);
      headers["Content-Type"] = "application/json";
    }
    if (session.accessToken) headers["Authorization"] = `Bearer ${session.accessToken}`;
    return fetch(url, { method: opts.method ?? "GET", headers, body });
  };

  let res = await send();
  if (res.status === 401 && session.refreshToken && (await session.refresh())) {
    res = await send();
  }
  if (res.status === 204) return undefined as T;

  const text = await res.text();
  const data: unknown = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string; details?: unknown } })?.error;
    throw new ApiError(res.status, err?.code ?? `HTTP_${res.status}`, err?.message ?? res.statusText, err?.details);
  }
  return data as T;
}
