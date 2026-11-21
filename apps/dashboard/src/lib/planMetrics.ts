import type { Plan } from "../api/types";

export interface PlanMetrics {
  routes: number;
  stops: number;
  distanceKm: number;
  drivingHours: number;
  lateStops: number;
  unassigned: number;
  cost: number;
  travel: number;
  fleet: number;
  lateness: number;
  overtime: number;
}

/** Comparable numbers for one plan (late = planned arrival after the window closes). */
export function planMetrics(p: Plan): PlanMetrics {
  const stops = p.routes.flatMap((r) => r.stops);
  return {
    routes: p.routes.length,
    stops: stops.length,
    distanceKm: p.total_distance_km,
    drivingHours: p.routes.reduce((h, r) => h + r.estimated_duration_minutes, 0) / 60,
    lateStops: stops.filter((s) => new Date(s.planned_arrival) > new Date(s.time_window_end)).length,
    unassigned: p.unassigned_order_ids.length,
    cost: p.total_cost,
    travel: p.objective.travel ?? 0,
    fleet: p.objective.fleet ?? 0,
    lateness: p.objective.lateness ?? 0,
    overtime: p.objective.overtime ?? 0,
  };
}

/** Relative change from a to b in percent (null when a is zero). Lower is better for every metric here. */
export function change(a: number, b: number): number | null {
  return a === 0 ? null : ((b - a) / a) * 100;
}

const csvCell = (v: string | number) => {
  const s = String(v);
  return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
};

/** Dispatch export: one line per stop, in route order. */
export function planCsv(p: Plan, fmtTime: (iso: string) => string): string {
  const header = ["vehicle", "driver", "sequence", "order_id", "customer", "lat", "lon", "eta", "departure", "window_start", "window_end"];
  const lines = p.routes.flatMap((r) =>
    r.stops.map((s) =>
      [r.registration_number, r.driver_name ?? "", s.sequence, s.external_order_id, s.customer_name, s.lat, s.lon,
        fmtTime(s.planned_arrival), fmtTime(s.planned_departure), fmtTime(s.time_window_start), fmtTime(s.time_window_end)].map(csvCell).join(","),
    ),
  );
  return [header.join(","), ...lines].join("\n") + "\n";
}

export function download(filename: string, content: string, type = "text/csv;charset=utf-8") {
  const url = URL.createObjectURL(new Blob([content], { type }));
  const a = Object.assign(document.createElement("a"), { href: url, download: filename });
  a.click();
  URL.revokeObjectURL(url);
}
