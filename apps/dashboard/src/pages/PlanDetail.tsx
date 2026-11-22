import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { Link, useParams } from "react-router";
import { api } from "../api/client";
import type { Depot, Page, Plan, RouteLine } from "../api/types";
import { useAuth } from "../auth/AuthContext";
import { PlanActions } from "../components/PlanActions";
import { RouteMap, type MapRoute } from "../components/RouteMap";
import { Badge, Button, Card, ErrorBox } from "../components/ui";
import { clock, km, money, timeWindow } from "../lib/format";
import { download, planCsv } from "../lib/planMetrics";
import { routeStyles } from "../lib/routeStyle";
import { algorithmLabel, planTone } from "./Plans";

export function PlanDetail() {
  const { id } = useParams();
  const { me } = useAuth();
  const tz = me!.organization.timezone;
  const [selected, setSelected] = useState<string | null>(null);

  const plan = useQuery({ queryKey: ["plan", id], queryFn: () => api<Plan>(`/route-plans/${id}`) });
  const geometry = useQuery({ queryKey: ["plan-geometry", id], queryFn: () => api<{ routes: RouteLine[] }>(`/route-plans/${id}/geometry`) });
  const depots = useQuery({ queryKey: ["depots"], queryFn: () => api<Page<Depot>>("/depots", { query: { limit: 500 } }) });

  const styles = useMemo(() => routeStyles(plan.data?.routes ?? []), [plan.data]);
  const mapRoutes = useMemo<MapRoute[]>(() => {
    const lines = new Map((geometry.data?.routes ?? []).map((l) => [l.route_id, l.coordinates]));
    return (plan.data?.routes ?? []).map((r) => ({
      id: r.id,
      label: r.registration_number,
      ...styles.get(r.vehicle_id)!,
      line: lines.get(r.id) ?? r.stops.map((s) => [s.lon, s.lat] as [number, number]),
      stops: r.stops.map((s) => ({
        lon: s.lon, lat: s.lat, seq: s.sequence,
        title: `${s.sequence}. ${s.external_order_id} · ${s.customer_name}`,
        detail: `ETA ${clock(s.planned_arrival, tz)} · window ${timeWindow(s.time_window_start, s.time_window_end, tz)}`,
      })),
    }));
  }, [plan.data, geometry.data, styles, tz]);

  if (plan.error) return <ErrorBox error={plan.error} />;
  if (!plan.data) return <p className="text-sm text-ink-muted">Loading plan…</p>;
  const p = plan.data;
  const usedDepots = (depots.data?.items ?? []).filter((d) => d.is_active);
  const straight = geometry.data?.routes.some((l) => l.source === "STRAIGHT");
  const current = p.routes.find((r) => r.id === selected);

  return (
    <div className="flex h-[calc(100vh-4rem)] flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <Link to="/plans" className="text-sm text-plum-deep hover:underline">← Plans</Link>
          <h1 className="text-3xl font-normal tracking-tight">
            Plan v{p.version} · {p.planning_date} <Badge tone={planTone[p.status]}>{p.status.toLowerCase()}</Badge>
          </h1>
          <p className="text-sm text-ink-muted">{algorithmLabel(p.algorithm)}</p>
          <div className="mt-2 flex flex-wrap items-start gap-2">
            <PlanActions plan={p} />
            <Button variant="secondary" onClick={() => download(`plan-${p.planning_date}-v${p.version}.csv`, planCsv(p, (iso) => clock(iso, tz)))}>
              Export CSV
            </Button>
          </div>
        </div>
        <dl className="flex gap-6 text-sm">
          <Kpi label="Routes" value={String(p.routes.length)} />
          <Kpi label="Stops" value={String(p.routes.reduce((n, r) => n + r.stops.length, 0))} />
          <Kpi label="Distance" value={km(p.total_distance_km)} />
          <Kpi label="Cost" value={money(p.total_cost, me!.organization.currency)} />
          <Kpi label="Unassigned" value={String(p.unassigned_order_ids.length)} alert={p.unassigned_order_ids.length > 0} />
        </dl>
      </div>
      {straight && <p className="text-xs text-amber-700">Road geometry is unavailable for some routes; they are drawn as straight lines.</p>}

      <div className="grid min-h-0 flex-1 gap-4 lg:grid-cols-[1fr_380px]">
        <RouteMap routes={mapRoutes} depots={usedDepots.map((d) => ({ id: d.id, name: d.name, lat: d.lat, lon: d.lon }))}
          selected={selected} onSelect={setSelected} />
        <div className="min-h-0 overflow-y-auto">
          <Card title={current ? `${current.registration_number} · stops` : "Routes"}
            actions={current && (
              <>
                <Link to={`/plans/${p.id}/routes/${current.id}/sheet`} className="text-sm text-plum-deep hover:underline">Route sheet</Link>
                <button className="text-sm text-plum-deep hover:underline" onClick={() => setSelected(null)}>All routes</button>
              </>
            )}>
            {!current ? (
              <ul className="divide-y divide-line" aria-label="Routes">
                {p.routes.map((r) => {
                  const s = styles.get(r.vehicle_id)!;
                  return (
                    <li key={r.id}>
                      <button onClick={() => setSelected(r.id)} className="flex w-full items-center gap-3 py-2 text-left text-sm hover:bg-sand-deep/50">
                        <Swatch color={s.color} dashed={s.dashed} />
                        <span className="flex-1">
                          <span className="font-medium">{r.registration_number}</span>
                          <span className="block text-xs text-ink-muted">{r.driver_name ?? "no driver"}</span>
                        </span>
                        <span className="text-right text-xs text-ink-muted">
                          {r.stops.length} stops<br />{km(r.total_distance_km)} · {Math.round(r.estimated_duration_minutes / 6) / 10} h
                        </span>
                      </button>
                    </li>
                  );
                })}
              </ul>
            ) : (
              <ol className="space-y-2 text-sm" aria-label="Stops">
                {current.stops.map((s) => {
                  const late = new Date(s.planned_arrival) > new Date(s.time_window_end);
                  return (
                    <li key={s.id} className="flex gap-3">
                      <span className="w-6 shrink-0 text-right font-mono text-xs text-ink-muted">{s.sequence}</span>
                      <span className="flex-1">
                        <span className="font-medium">{s.customer_name}</span> <span className="font-mono text-xs text-ink-muted">{s.external_order_id}</span>
                        <span className="block text-xs text-ink-muted">
                          ETA {clock(s.planned_arrival, tz)} · window {timeWindow(s.time_window_start, s.time_window_end, tz)}
                        </span>
                      </span>
                      {late && <Badge tone="red">late</Badge>}
                    </li>
                  );
                })}
              </ol>
            )}
          </Card>
        </div>
      </div>
    </div>
  );
}

function Kpi({ label, value, alert = false }: { label: string; value: string; alert?: boolean }) {
  return (
    <div>
      <dt className="text-xs text-ink-muted">{label}</dt>
      <dd className={`font-semibold ${alert ? "text-rose-700" : "text-ink"}`}>{value}</dd>
    </div>
  );
}

function Swatch({ color, dashed }: { color: string; dashed: boolean }) {
  return (
    <svg width="28" height="10" aria-hidden="true">
      <line x1="2" y1="5" x2="26" y2="5" stroke={color} strokeWidth="4" strokeLinecap="round" strokeDasharray={dashed ? "5 4" : undefined} />
    </svg>
  );
}
