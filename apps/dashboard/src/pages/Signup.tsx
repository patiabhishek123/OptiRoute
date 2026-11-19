import { useMutation } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, Navigate } from "react-router";
import { useAuth } from "../auth/AuthContext";
import { Button, ErrorBox, Field } from "../components/ui";
import { AuthShell } from "./Login";

export function Signup() {
  const { me, signup } = useAuth();
  const [form, setForm] = useState({ name: "", slug: "", admin_email: "", admin_password: "" });
  const m = useMutation({ mutationFn: () => signup(form) });
  if (me) return <Navigate to="/orders" replace />;
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => {
    const value = e.target.value;
    // Suggest a slug from the name until the user edits the slug themselves.
    setForm((f) => ({
      ...f,
      [k]: value,
      ...(k === "name" && f.slug === slugify(f.name) ? { slug: slugify(value) } : {}),
    }));
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    m.mutate();
  };
  return (
    <AuthShell title="Create your organization">
      <form onSubmit={submit} className="space-y-4">
        <Field label="Organization name" value={form.name} onChange={set("name")} required autoFocus />
        <Field label="Slug" value={form.slug} onChange={set("slug")} required hint="Used to sign in, e.g. kalinga-couriers" />
        <Field label="Admin email" type="email" value={form.admin_email} onChange={set("admin_email")} required />
        <Field label="Password" type="password" value={form.admin_password} onChange={set("admin_password")} required minLength={10} hint="At least 10 characters" />
        <ErrorBox error={m.error} />
        <Button type="submit" className="w-full" disabled={m.isPending}>
          {m.isPending ? "Creating…" : "Create organization"}
        </Button>
      </form>
      <p className="mt-4 text-center text-sm text-slate-500">
        Have an account? <Link to="/login" className="font-medium text-indigo-600 hover:underline">Sign in</Link>
      </p>
    </AuthShell>
  );
}

export function slugify(s: string) {
  return s.toLowerCase().trim().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 63);
}
