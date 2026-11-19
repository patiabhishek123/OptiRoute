// Small shared UI pieces (Tailwind only, no component library).
import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode } from "react";
import { ApiError } from "../api/client";

export function Button({ variant = "primary", className = "", ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "primary" | "secondary" | "danger" }) {
  const styles = {
    primary: "bg-indigo-600 text-white hover:bg-indigo-500 disabled:bg-indigo-300",
    secondary: "bg-white text-slate-700 ring-1 ring-slate-300 hover:bg-slate-50 disabled:text-slate-400",
    danger: "bg-rose-600 text-white hover:bg-rose-500 disabled:bg-rose-300",
  }[variant];
  return <button className={`rounded-md px-3 py-1.5 text-sm font-medium shadow-sm transition-colors disabled:cursor-not-allowed ${styles} ${className}`} {...props} />;
}

export function Field({ label, hint, ...props }: InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string }) {
  return (
    <label className="block text-sm">
      <span className="font-medium text-slate-700">{label}</span>
      <input className="mt-1 block w-full rounded-md border-0 px-3 py-1.5 ring-1 ring-slate-300 focus:ring-2 focus:ring-indigo-500 focus:outline-none" {...props} />
      {hint && <span className="mt-1 block text-xs text-slate-500">{hint}</span>}
    </label>
  );
}

export function Card({ title, actions, children }: { title?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="rounded-lg bg-white shadow-sm ring-1 ring-slate-200">
      {(title || actions) && (
        <header className="flex items-center justify-between border-b border-slate-200 px-4 py-3">
          <h2 className="text-sm font-semibold text-slate-800">{title}</h2>
          <div className="flex gap-2">{actions}</div>
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}

/** Renders an API error, including per-field validation details. */
export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  const e = error instanceof ApiError ? error : null;
  const details = e?.details && typeof e.details === "object" && !Array.isArray(e.details) ? Object.entries(e.details as Record<string, unknown>) : [];
  return (
    <div role="alert" className="rounded-md bg-rose-50 p-3 text-sm text-rose-800 ring-1 ring-rose-200">
      <p className="font-medium">{e ? e.message : String(error)}</p>
      {details.length > 0 && (
        <ul className="mt-1 list-inside list-disc">
          {details.map(([k, v]) => (
            <li key={k}>
              <span className="font-mono">{k}</span>: {typeof v === "string" ? v : JSON.stringify(v)}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function Badge({ children, tone = "slate" }: { children: ReactNode; tone?: "slate" | "green" | "amber" | "red" | "indigo" }) {
  const tones = {
    slate: "bg-slate-100 text-slate-700",
    green: "bg-emerald-100 text-emerald-800",
    amber: "bg-amber-100 text-amber-800",
    red: "bg-rose-100 text-rose-800",
    indigo: "bg-indigo-100 text-indigo-800",
  }[tone];
  return <span className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${tones}`}>{children}</span>;
}
