import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, use, useEffect, useMemo, type ReactNode } from "react";
import { api, session, type TokenPair } from "../api/client";
import type { Me } from "../api/types";

interface AuthState {
  me: Me | undefined;
  loading: boolean;
  login: (organization: string, email: string, password: string) => Promise<void>;
  signup: (input: { name: string; slug: string; admin_email: string; admin_password: string }) => Promise<void>;
  logout: () => Promise<void>;
  canWrite: boolean;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  // With only a refresh token (after a reload), the first call 401s, refreshes and retries.
  const meQuery = useQuery({
    queryKey: ["me"],
    queryFn: () => api<Me>("/organizations/me"),
    enabled: session.hasSession,
    retry: false,
    staleTime: 5 * 60_000,
  });

  useEffect(() => session.onEnd(() => qc.clear()), [qc]);

  const value = useMemo<AuthState>(
    () => ({
      me: session.hasSession ? meQuery.data : undefined,
      loading: session.hasSession && meQuery.isPending,
      canWrite: meQuery.data?.user.role === "ADMIN" || meQuery.data?.user.role === "DISPATCHER",
      async login(organization, email, password) {
        session.setTokens(await api<TokenPair>("/auth/login", { method: "POST", body: { organization, email, password } }));
        await qc.resetQueries({ queryKey: ["me"] });
        await qc.fetchQuery({ queryKey: ["me"], queryFn: () => api<Me>("/organizations/me") });
      },
      async signup(input) {
        const res = await api<{ tokens: TokenPair }>("/organizations", { method: "POST", body: input });
        session.setTokens(res.tokens);
        await qc.fetchQuery({ queryKey: ["me"], queryFn: () => api<Me>("/organizations/me") });
      },
      async logout() {
        const token = session.refreshToken;
        if (token) await api("/auth/logout", { method: "POST", body: { refresh_token: token } }).catch(() => {});
        session.clear();
        qc.clear();
      },
    }),
    [meQuery.data, meQuery.isPending, qc],
  );
  return <AuthContext value={value}>{children}</AuthContext>;
}

export function useAuth() {
  const ctx = use(AuthContext);
  if (!ctx) throw new Error("useAuth outside AuthProvider");
  return ctx;
}
