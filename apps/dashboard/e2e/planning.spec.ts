import { expect, test } from "@playwright/test";
import { PLANNING_DATE, requireApi, seedDay, signUp } from "./helpers";

// Needs the optimizer worker (`make worker`) and OSRM (`make osrm-up`) besides the API.
test("optimize a day and inspect the plan on the map", async ({ page }) => {
  await requireApi(page);
  const slug = await signUp(page, "plan");
  // ~240 kg of orders on 100 kg vans: the plan needs all three routes (several colors on one map).
  await seedDay(page, slug, { vans: 3, orders: 30, capacityKg: 100 });

  await page.getByRole("link", { name: "Plans" }).click();
  await expect(page.getByRole("heading", { name: "Plans", level: 1 })).toBeVisible();
  await page.getByLabel("Planning date").fill(PLANNING_DATE);
  await page.getByLabel("Time limit (seconds)").fill("3");
  await page.getByRole("button", { name: "Run optimization" }).click();

  // Live status: the job row moves to completed without a reload, then the plan appears.
  const job = page.getByRole("row", { name: /Optimized \(OR-Tools\) 30 orders · 3 vehicles/ });
  await expect(job).toContainText("completed", { timeout: 60_000 });
  const plan = page.getByRole("link", { name: "Version 1" });
  await expect(plan).toBeVisible();
  await page.screenshot({ path: "test-results/screens/plans.png", fullPage: true });

  await plan.click();
  await expect(page.getByRole("heading", { name: /Plan v1 · 2026-11-02/ })).toBeVisible();
  const routes = page.getByRole("list", { name: "Routes" }).getByRole("listitem");
  await expect(routes.first()).toBeVisible();
  const routeCount = await routes.count();
  expect(routeCount).toBeGreaterThan(1);
  expect(routeCount).toBeLessThanOrEqual(3);
  // The map must actually draw every route and all 30 numbered stops (render probe on "idle").
  const map = page.getByTestId("route-map");
  await expect(map).toHaveAttribute("data-rendered-routes", String(routeCount), { timeout: 30_000 });
  await expect(map).toHaveAttribute("data-rendered-stops", "30");
  await page.screenshot({ path: "test-results/screens/plan-map.png" });

  // Selecting a route lists its stops in sequence.
  await routes.first().getByRole("button").click();
  const stops = page.getByRole("list", { name: "Stops" }).getByRole("listitem");
  await expect(stops.first()).toContainText("ETA");
  await expect(map).toHaveAttribute("data-rendered-routes", String(routeCount)); // others dimmed, not hidden
  await page.screenshot({ path: "test-results/screens/plan-route-selected.png" });
  await page.getByRole("button", { name: "All routes" }).click();
  await expect(routes.first()).toBeVisible();
});
