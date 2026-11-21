import { describe, expect, it } from "vitest";
import type { Plan } from "../api/types";
import { change, planCsv, planMetrics } from "./planMetrics";

const stop = (seq: number, arrival: string, windowEnd: string, name = `Customer ${seq}`) => ({
  id: `s${seq}`, order_id: `o${seq}`, external_order_id: `O-${seq}`, customer_name: name, lat: 20.3, lon: 85.8, sequence: seq,
  planned_arrival: arrival, planned_departure: arrival, status: "PENDING", time_window_start: "2026-11-02T03:30:00Z", time_window_end: windowEnd,
});
const plan = {
  total_distance_km: 42.5, total_cost: 1000, unassigned_order_ids: ["x"],
  objective: { total: 1000, travel: 700, fleet: 300, lateness: 0, overtime: 0, unserved: 0, stability: 0 },
  routes: [
    { registration_number: "OD-1", driver_name: "Asha", estimated_duration_minutes: 90,
      stops: [stop(1, "2026-11-02T04:00:00Z", "2026-11-02T07:30:00Z"), stop(2, "2026-11-02T08:00:00Z", "2026-11-02T07:30:00Z", 'Das, "Pharmacy"')] },
  ],
} as unknown as Plan;

describe("plan metrics", () => {
  it("counts stops, late stops and hours", () => {
    expect(planMetrics(plan)).toMatchObject({ routes: 1, stops: 2, lateStops: 1, unassigned: 1, drivingHours: 1.5, travel: 700 });
  });

  it("computes relative change, guarding zero", () => {
    expect(change(200, 150)).toBe(-25);
    expect(change(0, 5)).toBeNull();
  });

  it("exports CSV with quoting", () => {
    const csv = planCsv(plan, (iso) => iso.slice(11, 16));
    const lines = csv.trim().split("\n");
    expect(lines[0]).toBe("vehicle,driver,sequence,order_id,customer,lat,lon,eta,departure,window_start,window_end");
    expect(lines[2]).toContain('"Das, ""Pharmacy"""');
    expect(lines).toHaveLength(3);
  });
});
