import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router";
import { api } from "../api/client";
import type { Plan } from "../api/types";
import { useAuth } from "../auth/AuthContext";
import { Card, ErrorBox } from "../components/ui";
import { km, money } from "../lib/format";
import { change, planMetrics, type PlanMetrics } from "../lib/planMetrics";
import { algorithmLabel } from "./Plans";

/** Baseline vs optimized (spec §22): two plans side by side; lower is better for every row. */
export function Compare() {
  const [params] = useSearchParams();
  const { me } = useAuth();
  const cur = me!.organization.currency;
  const ids = [params.get("a"), params.get("b")].filter(Boolean) as string[];
  const plans = useQuery({
    queryKey: ["compare", ...ids],
    queryFn: () => Promise.all(ids.map((id) => api<Plan>(`/route-plans/${id}`))),
    enabled: ids.length === 2,
  });
  if (ids.length !== 2) return <p className="text-sm text-slate-500">Choose two plans to compare on the Plans page.</p>;
  if (plans.error) return <ErrorBox error={plans.error} />;
  if (!plans.data) return <p className="text-sm text-slate-500">Loading…</p>;

  // Baseline = the more expensive plan, so changes read as "what optimizing bought".
  const [a, b] = [...plans.data].sort((x, y) => y.total_cost - x.total_cost) as [Plan, Plan];
  const ma = planMetrics(a);
  const mb = planMetrics(b);
  const rows: { label: string; key: keyof PlanMetrics; fmt: (v: number) => string }[] = [
    { label: "Total cost", key: "cost", fmt: (v) => money(v, cur) },
    { label: "Distance", key: "distanceKm", fmt: km },
    { label: "Vehicles used", key: "routes", fmt: String },
    { label: "Route time incl. waiting", key: "drivingHours", fmt: (v) => `${v.toFixed(1)} h` },
    { label: "Late stops", key: "lateStops", fmt: String },
    { label: "Unassigned orders", key: "unassigned", fmt: String },
    { label: "Travel cost", key: "travel", fmt: (v) => money(v, cur) },
    { label: "Vehicle fixed cost", key: "fleet", fmt: (v) => money(v, cur) },
    { label: "Lateness penalty", key: "lateness", fmt: (v) => money(v, cur) },
    { label: "Overtime cost", key: "overtime", fmt: (v) => money(v, cur) },
  ];
  const saving = ma.cost - mb.cost;

  return (
    <div className="space-y-4">
      <Link to="/plans" className="text-sm text-indigo-700 hover:underline">← Plans</Link>
      <h1 className="text-xl font-semibold tracking-tight">Compare plans · {a.planning_date}</h1>
      <div className="grid gap-4 sm:grid-cols-3">
        <Card><Headline label="Cost saving" value={money(saving, cur)} sub={`${Math.abs(change(ma.cost, mb.cost) ?? 0).toFixed(1)}% ${saving >= 0 ? "cheaper" : "more expensive"}`} /></Card>
        <Card><Headline label="Distance saved" value={km(ma.distanceKm - mb.distanceKm)} sub={`${ma.distanceKm.toFixed(1)} → ${mb.distanceKm.toFixed(1)} km`} /></Card>
        <Card><Headline label="Vehicles" value={`${ma.routes} → ${mb.routes}`} sub={`late stops ${ma.lateStops} → ${mb.lateStops}`} /></Card>
      </div>
      <Card>
        <table className="w-full text-sm" aria-label="Plan comparison">
          <thead>
            <tr className="text-left text-xs tracking-wide text-slate-500 uppercase">
              <th className="py-2">Metric</th>
              <th className="py-2 text-right"><PlanHead plan={a} /></th>
              <th className="py-2 text-right"><PlanHead plan={b} /></th>
              <th className="py-2 text-right">Change</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {rows.map((r) => {
              const pct = change(ma[r.key], mb[r.key]);
              const diff = mb[r.key] - ma[r.key];
              const verdict = Math.abs(diff) < 1e-9 ? "same" : diff < 0 ? "better" : "worse";
              return (
                <tr key={r.key}>
                  <td className="py-2 text-slate-700">{r.label}</td>
                  <td className="py-2 text-right tabular-nums">{r.fmt(ma[r.key])}</td>
                  <td className="py-2 text-right tabular-nums">{r.fmt(mb[r.key])}</td>
                  <td className="py-2 text-right tabular-nums">
                    {verdict === "same" ? (
                      <span className="text-slate-400">same</span>
                    ) : (
                      // Identity is the word, not the color.
                      <span className={verdict === "better" ? "text-emerald-700" : "text-rose-700"}>
                        {pct === null ? "" : `${pct > 0 ? "+" : ""}${pct.toFixed(1)}% `}{verdict === "better" ? "▼ better" : "▲ worse"}
                      </span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Card>
    </div>
  );
}

function PlanHead({ plan }: { plan: Plan }) {
  return (
    <Link to={`/plans/${plan.id}`} className="normal-case hover:underline">
      v{plan.version} · {algorithmLabel(plan.algorithm)}
    </Link>
  );
}

function Headline({ label, value, sub }: { label: string; value: string; sub: string }) {
  return (
    <div>
      <p className="text-xs font-medium tracking-wide text-slate-500 uppercase">{label}</p>
      <p className="mt-1 text-2xl font-semibold tabular-nums text-slate-900">{value}</p>
      <p className="text-sm text-slate-500">{sub}</p>
    </div>
  );
}
