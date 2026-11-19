import { useQuery } from "@tanstack/react-query";
import { useParams } from "react-router";
import { api } from "../api/client";
import type { Plan } from "../api/types";
import { useAuth } from "../auth/AuthContext";
import { ErrorBox } from "../components/ui";
import { clock, km, timeWindow } from "../lib/format";

/** Printable route sheet for one driver ("Print" -> "Save as PDF" gives the PDF). */
export function RouteSheet() {
  const { id, routeId } = useParams();
  const { me } = useAuth();
  const tz = me!.organization.timezone;
  const plan = useQuery({ queryKey: ["plan", id], queryFn: () => api<Plan>(`/route-plans/${id}`) });
  if (plan.error) return <ErrorBox error={plan.error} />;
  const route = plan.data?.routes.find((r) => r.id === routeId);
  if (!plan.data || !route) return <p className="p-6 text-sm text-slate-500">Loading…</p>;
  return (
    <div className="mx-auto max-w-3xl bg-white p-8 text-sm print:p-0">
      <div className="mb-4 flex items-start justify-between">
        <div>
          <h1 className="text-xl font-semibold">{route.registration_number} · {route.driver_name ?? "no driver"}</h1>
          <p className="text-slate-600">
            {me!.organization.name} · {plan.data.planning_date} · plan v{plan.data.version} ({plan.data.status.toLowerCase()})
          </p>
          <p className="text-slate-600">{route.stops.length} stops · {km(route.total_distance_km)} · about {(route.estimated_duration_minutes / 60).toFixed(1)} h</p>
        </div>
        <button onClick={() => window.print()} className="rounded-md bg-indigo-600 px-3 py-1.5 font-medium text-white print:hidden">Print / save PDF</button>
      </div>
      <table className="w-full border-collapse" aria-label="Route sheet">
        <thead>
          <tr className="border-b-2 border-slate-800 text-left">
            <th className="py-1 pr-2">#</th><th className="py-1 pr-2">ETA</th><th className="py-1 pr-2">Window</th>
            <th className="py-1 pr-2">Order</th><th className="py-1 pr-2">Customer</th><th className="py-1 pr-2">Location</th><th className="py-1">Done</th>
          </tr>
        </thead>
        <tbody>
          {route.stops.map((s) => (
            <tr key={s.id} className="border-b border-slate-200 align-top">
              <td className="py-1.5 pr-2 font-semibold">{s.sequence}</td>
              <td className="py-1.5 pr-2 tabular-nums">{clock(s.planned_arrival, tz)}</td>
              <td className="py-1.5 pr-2 tabular-nums">{timeWindow(s.time_window_start, s.time_window_end, tz)}</td>
              <td className="py-1.5 pr-2 font-mono text-xs">{s.external_order_id}</td>
              <td className="py-1.5 pr-2">{s.customer_name}</td>
              <td className="py-1.5 pr-2 font-mono text-xs">{s.lat.toFixed(5)}, {s.lon.toFixed(5)}</td>
              <td className="py-1.5">☐</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
