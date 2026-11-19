import { readFile } from "node:fs/promises";
import { expect, test, type Page } from "@playwright/test";
import { PLANNING_DATE, requireApi, seedDay, signUp } from "./helpers";

async function run(page: Page, algorithm: string, seconds: string) {
  await page.getByLabel("Algorithm").selectOption({ label: algorithm });
  await page.getByLabel("Time limit (seconds)").fill(seconds);
  await page.getByRole("button", { name: "Run optimization" }).click();
}

// Spec §22 steps 30-35 at MVP scale (5-10 vehicles, 50-100 orders): baseline vs optimized, review,
// approve, activate, export. Needs the optimizer worker (OSRM optional).
const VANS = 6;
const ORDERS = 80; // ~640 kg on 6 x 150 kg vans
test("compare baseline and optimized, then dispatch", async ({ page }) => {
  await requireApi(page);
  const slug = await signUp(page, "dispatch");
  await seedDay(page, slug, { vans: VANS, orders: ORDERS, capacityKg: 150 });

  await page.getByRole("link", { name: "Plans" }).click();
  await expect(page.getByRole("heading", { name: "Plans", level: 1 })).toBeVisible();
  await page.getByLabel("Planning date").fill(PLANNING_DATE);
  await run(page, "Baseline (nearest neighbour)", "1");
  await run(page, "Optimized (OR-Tools)", "3");
  await expect(page.getByRole("link", { name: "Version 2" })).toBeVisible({ timeout: 60_000 });
  await expect(page.getByRole("link", { name: "Version 1" })).toBeVisible();

  // Compare the two plans.
  await page.getByLabel("Compare version 1").check();
  await page.getByLabel("Compare version 2").check();
  await page.getByRole("button", { name: "Compare selected (2/2)" }).click();
  const table = page.getByRole("table", { name: "Plan comparison" });
  await expect(table.getByRole("row", { name: /Total cost/ })).toContainText(/better|same/);
  await expect(page.getByText("Cost saving")).toBeVisible();
  await page.screenshot({ path: "test-results/screens/compare.png", fullPage: true });

  // Open the optimized (cheaper, right-hand) plan and dispatch it.
  await table.getByRole("columnheader").nth(2).getByRole("link").click();
  await expect(page.getByRole("heading", { name: /Plan v\d/ })).toContainText("draft");
  await page.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByRole("heading", { name: /Plan v\d/ })).toContainText("approved");
  await page.getByRole("button", { name: "Activate" }).click();
  await expect(page.getByRole("heading", { name: /Plan v\d/ })).toContainText("active");
  await expect(page.getByRole("button", { name: "Activate" })).toHaveCount(0);

  // Export: CSV with one line per stop.
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export CSV" }).click();
  const csv = await readFile((await (await download).path())!, "utf8");
  const lines = csv.trim().split("\n");
  expect(lines[0]).toBe("vehicle,driver,sequence,order_id,customer,lat,lon,eta,departure,window_start,window_end");
  expect(lines).toHaveLength(ORDERS + 1); // every order routed

  // A driver's printable route sheet.
  const routes = page.getByRole("list", { name: "Routes" }).getByRole("listitem");
  await routes.first().getByRole("button").click();
  const stopCount = await page.getByRole("list", { name: "Stops" }).getByRole("listitem").count();
  await page.getByRole("link", { name: "Route sheet" }).click();
  const sheet = page.getByRole("table", { name: "Route sheet" });
  await expect(sheet.getByRole("row")).toHaveCount(stopCount + 1);
  await page.screenshot({ path: "test-results/screens/route-sheet.png", fullPage: true });
  await page.goBack();

  // Activation moved the orders to PLANNED.
  await page.getByRole("link", { name: "Orders" }).click();
  await expect(page.getByRole("heading", { name: "Orders", level: 1 })).toBeVisible();
  await page.getByLabel("Planning date").fill(PLANNING_DATE);
  await expect(page.getByText(`${ORDERS} orders on ${PLANNING_DATE} · ${ORDERS} planned`)).toBeVisible();

  // Activating the baseline now needs explicit confirmation to replace the active plan.
  await page.getByRole("link", { name: "Plans" }).click();
  await expect(page.getByRole("heading", { name: "Plans", level: 1 })).toBeVisible();
  await page.getByLabel("Planning date").fill(PLANNING_DATE);
  const draft = page.getByRole("row").filter({ hasText: "draft" }).getByRole("link");
  await draft.click();
  await page.getByRole("button", { name: "Approve" }).click();
  await page.getByRole("button", { name: "Activate" }).click();
  const dialog = page.getByRole("dialog", { name: "Replace active plan" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "Replace active plan" }).click();
  await expect(page.getByRole("heading", { name: /Plan v\d/ })).toContainText("active");
  await page.getByRole("link", { name: "← Plans" }).click();
  await page.getByLabel("Planning date").fill(PLANNING_DATE);
  await expect(page.getByRole("row").filter({ hasText: "superseded" })).toHaveCount(1);
});
