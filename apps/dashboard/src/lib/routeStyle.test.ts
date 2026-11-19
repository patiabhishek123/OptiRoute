import { describe, expect, it } from "vitest";
import { CATEGORICAL, OTHER, routeStyles } from "./routeStyle";

const vehicles = (n: number) => Array.from({ length: n }, (_, i) => ({ vehicle_id: `v${i}`, registration_number: `OD-02-${i + 1}` }));

describe("routeStyles", () => {
  it("assigns hues in fixed order by registration, not by input order", () => {
    const a = routeStyles(vehicles(3));
    const b = routeStyles([...vehicles(3)].reverse());
    expect([...a.entries()].sort()).toEqual([...b.entries()].sort());
    expect(a.get("v0")).toEqual({ color: CATEGORICAL[0], dashed: false });
  });

  it("sorts registrations numerically (OD-02-10 after OD-02-9)", () => {
    const s = routeStyles(vehicles(10));
    expect(s.get("v8")!.color).toBe(CATEGORICAL[0]); // OD-02-9 is 9th -> slot 1 again, dashed
    expect(s.get("v8")!.dashed).toBe(true);
    expect(s.get("v9")!.dashed).toBe(true); // OD-02-10 is 10th
  });

  it("uses dashes for routes 9-16 and folds 17+ to neutral, never inventing hues", () => {
    const s = routeStyles(vehicles(20));
    const all = [...s.values()];
    expect(all.filter((x) => x.dashed)).toHaveLength(8);
    expect(all.filter((x) => x.color === OTHER)).toHaveLength(4);
    expect(new Set(all.map((x) => x.color)).size).toBe(CATEGORICAL.length + 1);
  });
});
