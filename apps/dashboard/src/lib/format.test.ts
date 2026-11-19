import { describe, expect, it } from "vitest";
import { clock, timeWindow, todayIn } from "./format";

describe("org-timezone formatting", () => {
  it("formats times in the organization's timezone, not the browser's", () => {
    expect(clock("2026-11-02T03:30:00Z", "Asia/Kolkata")).toBe("09:00");
    expect(timeWindow("2026-11-02T03:30:00Z", "2026-11-02T07:30:00Z", "Asia/Kolkata")).toBe("09:00–13:00");
  });

  it("computes today's date in the organization's timezone", () => {
    // 20:00 UTC on 1 Nov is already 2 Nov in India.
    expect(todayIn("Asia/Kolkata", new Date("2026-11-01T20:00:00Z"))).toBe("2026-11-02");
    expect(todayIn("UTC", new Date("2026-11-01T20:00:00Z"))).toBe("2026-11-01");
  });
});
