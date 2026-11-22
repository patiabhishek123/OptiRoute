import { useMutation } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, Navigate } from "react-router";
import { useAuth } from "../auth/AuthContext";
import { Button, ErrorBox, Field } from "../components/ui";

export function AuthShell({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center p-6">
      <div className="w-full max-w-sm">
        <h1 className="mb-1 text-3xl font-normal tracking-tight">OptiRoute</h1>
        <p className="mb-6 text-sm text-ink-muted">{title}</p>
        <div className="rounded-sm bg-sand p-6">{children}</div>
      </div>
    </div>
  );
}

export function Login() {
  const { me, login } = useAuth();
  const [form, setForm] = useState({ organization: "", email: "", password: "" });
  const m = useMutation({ mutationFn: () => login(form.organization, form.email, form.password) });
  if (me) return <Navigate to="/orders" replace />;
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, [k]: e.target.value });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    m.mutate();
  };
  return (
    <AuthShell title="Sign in to your organization">
      <form onSubmit={submit} className="space-y-4">
        <Field label="Organization" placeholder="kalinga-couriers" value={form.organization} onChange={set("organization")} required autoFocus />
        <Field label="Email" type="email" value={form.email} onChange={set("email")} required autoComplete="username" />
        <Field label="Password" type="password" value={form.password} onChange={set("password")} required autoComplete="current-password" />
        <ErrorBox error={m.error} />
        <Button type="submit" className="w-full" disabled={m.isPending}>
          {m.isPending ? "Signing in…" : "Sign in"}
        </Button>
      </form>
      <p className="mt-4 text-center text-sm text-ink-muted">
        New here? <Link to="/signup" className="font-medium text-plum-deep hover:underline">Create an organization</Link>
      </p>
    </AuthShell>
  );
}
