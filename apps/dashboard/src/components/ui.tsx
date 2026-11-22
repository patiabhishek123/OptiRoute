// Small shared UI pieces (Tailwind only, no component library).
import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode } from "react";
import { ApiError } from "../api/client";

export function Button({ variant = "primary", className = "", ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "primary" | "secondary" | "danger" }) {
  const styles = {
    primary: "bg-ink text-cream hover:bg-ink/85 disabled:bg-ink/40",
    secondary: "bg-cream text-ink ring-1 ring-ink/70 hover:bg-sand-deep disabled:text-ink-soft disabled:ring-line-strong",
    danger: "bg-rose-700 text-white hover:bg-rose-600 disabled:bg-rose-300",
  }[variant];
  return <button className={`rounded-sm px-3.5 py-2 text-sm font-medium transition-colors disabled:cursor-not-allowed ${styles} ${className}`} {...props} />;
}

export function Field({ label, hint, ...props }: InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string }) {
  return (
    <label className="block text-sm">
      <span className="text-ink-muted">{label}</span>
      <input className="mt-1 block w-full rounded-sm border-0 bg-cream px-3 py-1.5 text-ink ring-1 ring-line-strong placeholder:text-ink-soft focus:ring-2 focus:ring-plum focus:outline-none" {...props} />
      {hint && <span className="mt-1 block text-xs text-ink-muted">{hint}</span>}
    </label>
  );
}

export function Card({ title, actions, children }: { title?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="rounded-sm bg-sand">
      {(title || actions) && (
        <header className="flex items-center justify-between px-5 pt-5 pb-2">
          <h2 className="text-lg text-ink">{title}</h2>
          <div className="flex gap-2">{actions}</div>
        </header>
      )}
      <div className="px-5 pt-2 pb-5">{children}</div>
    </section>
  );
}

/** Renders an API error, including per-field validation details. */
export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  const e = error instanceof ApiError ? error : null;
  const details = e?.details && typeof e.details === "object" && !Array.isArray(e.details) ? Object.entries(e.details as Record<string, unknown>) : [];
  return (
    <div role="alert" className="rounded-sm bg-rose-50 p-3 text-sm text-rose-800 ring-1 ring-rose-200">
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

export type Tone = "neutral" | "green" | "amber" | "red" | "plum";

/** Pill in the inspiration's style: light chip, colored dot carries the tone. */
export function Badge({ children, tone = "neutral" }: { children: ReactNode; tone?: Tone }) {
  const dot = {
    neutral: "bg-ink-soft",
    green: "bg-emerald-600",
    amber: "bg-sun",
    red: "bg-rose-600",
    plum: "bg-plum",
  }[tone];
  return (
    <span className="inline-flex items-center gap-1.5 rounded-sm bg-cream px-2 py-0.5 text-xs font-medium text-ink ring-1 ring-line">
      <span aria-hidden className={`size-1.5 rounded-full ${dot}`} />
      {children}
    </span>
  );
}

export function Select({ label, children, ...props }: React.SelectHTMLAttributes<HTMLSelectElement> & { label?: string }) {
  const select = (
    <select className="mt-1 block w-full rounded-sm border-0 bg-cream px-2 py-1.5 text-sm text-ink ring-1 ring-line-strong focus:ring-2 focus:ring-plum focus:outline-none" {...props}>
      {children}
    </select>
  );
  if (!label) return select;
  return (
    <label className="block text-sm">
      <span className="text-ink-muted">{label}</span>
      {select}
    </label>
  );
}

/** Minimal data table: columns are header + cell renderer. */
export function Table<T>({ rows, columns, rowKey, empty = "Nothing here yet." }: {
  rows: T[];
  columns: { header: string; cell: (row: T) => ReactNode; className?: string }[];
  rowKey: (row: T) => string;
  empty?: string;
}) {
  if (rows.length === 0) return <p className="py-6 text-center text-sm text-ink-muted">{empty}</p>;
  return (
    <div className="-mx-5 overflow-x-auto">
      <table className="min-w-full divide-y divide-line text-sm">
        <thead>
          <tr>
            {columns.map((c) => (
              <th key={c.header} className={`px-5 py-2 text-left text-xs font-normal text-ink-muted ${c.className ?? ""}`}>
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {rows.map((r) => (
            <tr key={rowKey(r)} className="hover:bg-sand-deep/50">
              {columns.map((c) => (
                <td key={c.header} className={`px-5 py-2.5 whitespace-nowrap ${c.className ?? ""}`}>
                  {c.cell(r)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function Tabs<T extends string>({ tabs, value, onChange }: { tabs: { id: T; label: string }[]; value: T; onChange: (t: T) => void }) {
  return (
    <div className="flex gap-1 border-b border-line" role="tablist">
      {tabs.map((t) => (
        <button
          key={t.id}
          role="tab"
          aria-selected={t.id === value}
          onClick={() => onChange(t.id)}
          className={`-mb-px border-b-2 px-3 py-2 text-sm font-medium ${t.id === value ? "border-plum text-ink" : "border-transparent text-ink-muted hover:text-ink"}`}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}
