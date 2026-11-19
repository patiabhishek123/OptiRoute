import { expect, test } from "@playwright/test";
import { PLANNING_DATE, requireApi, signUp } from "./helpers";

const goodCsv = [
  "order_id,customer,lat,lon,weight_kg,from,to",
  "A-1,Sahoo Stores,20.31,85.82,12,09:00,13:00",
  "A-2,Das Pharmacy,20.27,85.84,3.5,13:00,17:00",
  "A-3,Mishra Traders,20.35,85.80,40,09:00,18:00",
].join("\n");
const badCsv = goodCsv.replace("20.27,85.84", "95,85.84").replace("09:00,18:00", "18:00,09:00");

test("set up a fleet and import orders", async ({ page }) => {
  await requireApi(page);
  const slug = await signUp(page, "fleet");

  // Depot
  await page.getByRole("link", { name: "Fleet" }).click();
  await page.getByLabel("Name").fill("Bhubaneswar Hub");
  await page.getByLabel("Latitude").fill("20.2961");
  await page.getByLabel("Longitude").fill("85.8245");
  await page.getByRole("button", { name: "Add depot" }).click();
  await expect(page.getByRole("cell", { name: "Bhubaneswar Hub" })).toBeVisible();

  // Vehicle
  await page.getByRole("tab", { name: "Vehicles" }).click();
  await page.getByLabel("Depot").selectOption({ label: "Bhubaneswar Hub" });
  await page.getByLabel("Registration number").fill("OD-02-AB-1234");
  await page.getByLabel("Capacity (kg)").fill("500");
  await page.getByLabel("Capacity (m³)").fill("4");
  await page.getByLabel("Cost per km").fill("12");
  await page.getByRole("button", { name: "Add vehicle" }).click();
  await expect(page.getByRole("cell", { name: "OD-02-AB-1234" })).toBeVisible();

  // Driver
  await page.getByRole("tab", { name: "Drivers" }).click();
  await page.getByLabel("Name").fill("Asha Pradhan");
  await page.getByRole("button", { name: "Add driver" }).click();
  await expect(page.getByRole("cell", { name: "Asha Pradhan" })).toBeVisible();

  // Pairing for the planning date
  await page.getByRole("tab", { name: "Daily pairing" }).click();
  await page.getByLabel("Pairing date").fill(PLANNING_DATE);
  await page.getByLabel("Driver for OD-02-AB-1234").selectOption({ label: "Asha Pradhan (08:00–18:00)" });
  await page.getByRole("button", { name: "Save pairing" }).click();
  await expect(page.getByText("Saved.")).toBeVisible();
  await expect(page.getByText("1 of 1 available vehicles have a driver")).toBeVisible();
  await page.screenshot({ path: "test-results/screens/fleet-pairing.png", fullPage: true });

  // Orders: a bad file is rejected with line/column errors and imports nothing...
  await page.getByRole("link", { name: "Orders" }).click();
  await page.getByLabel("Planning date").fill(PLANNING_DATE);
  await page.getByLabel("CSV file").setInputFiles({ name: "orders.csv", mimeType: "text/csv", buffer: Buffer.from(badCsv) });
  await page.getByRole("button", { name: "Import" }).click();
  const alert = page.getByRole("alert");
  await expect(alert).toContainText("Nothing was imported: 2 problems found");
  await expect(alert.getByRole("row", { name: /3 lat/ })).toBeVisible();
  await expect(alert.getByRole("row", { name: /4 time_window_end/ })).toBeVisible();
  await expect(page.getByText("No orders for this date")).toBeVisible();
  await page.screenshot({ path: "test-results/screens/orders-import-errors.png", fullPage: true });

  // ...the fixed file imports, with windows shown in the org's timezone.
  await page.getByLabel("CSV file").setInputFiles({ name: "orders.csv", mimeType: "text/csv", buffer: Buffer.from(goodCsv) });
  await page.getByRole("button", { name: "Import" }).click();
  await expect(page.getByRole("status")).toHaveText("Imported 3 orders.");
  await expect(page.getByRole("row", { name: /A-1 Sahoo Stores 09:00–13:00/ })).toBeVisible();
  await expect(page.getByText(`3 orders on ${PLANNING_DATE} · 3 pending`)).toBeVisible();

  // Cancel one pending order.
  await page.getByRole("row", { name: /A-2/ }).getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByRole("row", { name: /A-2/ })).toContainText("cancelled");
  await page.screenshot({ path: "test-results/screens/orders.png", fullPage: true });

  // A reload keeps the session (refresh token) and the org context.
  await page.reload();
  await expect(page.getByRole("heading", { name: "Orders", level: 1 })).toBeVisible();
  await expect(page.getByText("Kalinga Couriers")).toBeVisible();
  expect(slug).toContain("fleet-");
});
