import { expect, test, type Page } from "@playwright/test";
import { PLANNING_DATE, requireApi, signUp } from "./helpers";

const csv = (...lines: string[]) => ({ name: "fleet.csv", mimeType: "text/csv", buffer: Buffer.from(lines.join("\n")) });

async function importFile(page: Page, title: string, file: ReturnType<typeof csv>) {
  await page.getByLabel(`${title} file`).setInputFiles(file);
  await page.getByRole("button", { name: "Import", exact: true }).click();
}

test("import depots, vehicles, drivers and a pairing from CSV", async ({ page }) => {
  await requireApi(page);
  await signUp(page, "fleetcsv");
  await page.getByRole("link", { name: "Fleet" }).click();

  // Depots
  await importFile(page, "Import depots from CSV", csv("name,lat,lon", "Bhubaneswar Hub,20.2961,85.8245", "Cuttack Hub,20.4625,85.8830"));
  await expect(page.getByRole("status")).toHaveText("Imported 2 depots.");
  await expect(page.getByRole("cell", { name: "Cuttack Hub" })).toBeVisible();

  // Vehicles: a bad file is rejected with line/column errors and creates nothing...
  await page.getByRole("tab", { name: "Vehicles" }).click();
  const vehicles = ["registration_number,depot,vehicle_type,capacity_weight_kg,capacity_volume_m3,cost_per_km,fixed_cost",
    "OD-02-AB-1101,Bhubaneswar Hub,van,400,3,12,600", "OD-05-TR-5501,Cuttack Hub,truck,1200,8,20,1500"];
  await importFile(page, "Import vehicles from CSV", csv(...vehicles, "OD-09-XX-0001,Puri Hub,van,400,3,12,600"));
  const alert = page.getByRole("alert");
  await expect(alert).toContainText("Nothing was imported: 1 problem found");
  await expect(alert.getByRole("row", { name: /4 depot depot not found/ })).toBeVisible();
  await expect(page.getByText("No vehicles yet.")).toBeVisible();
  // ...the fixed file imports.
  await importFile(page, "Import vehicles from CSV", csv(...vehicles));
  await expect(page.getByRole("status")).toHaveText("Imported 2 vehicles.");
  await expect(page.getByRole("row", { name: /OD-05-TR-5501 truck Cuttack Hub/ })).toBeVisible();

  // Drivers
  await page.getByRole("tab", { name: "Drivers" }).click();
  await importFile(page, "Import drivers from CSV",
    csv("name,phone,shift_start,shift_end,break_duration_minutes,max_overtime_minutes", "Ramesh Behera,,08:00,18:00,30,60", "Hemant Tripathy,,07:00,17:00,45,90"));
  await expect(page.getByRole("status")).toHaveText("Imported 2 drivers.");
  await expect(page.getByRole("row", { name: /Hemant Tripathy .* 07:00–17:00/ })).toBeVisible();

  // Pairing: the file fills the dropdowns; nothing is stored until Save.
  await page.getByRole("tab", { name: "Daily pairing" }).click();
  await page.getByLabel("Pairing date").fill(PLANNING_DATE);
  await importFile(page, "Fill pairing from CSV", csv("registration_number,driver_name", "OD-02-AB-1101,Ramesh Behera", "OD-05-TR-5501,Nobody"));
  await expect(page.getByRole("alert")).toContainText("Nothing was filled in: 1 problem found");
  await importFile(page, "Fill pairing from CSV", csv("registration_number,driver_name", "OD-02-AB-1101,Ramesh Behera", "OD-05-TR-5501,Hemant Tripathy"));
  await expect(page.getByRole("status")).toHaveText("Filled 2 pairs. Review them, then save.");
  await expect(page.getByLabel("Driver for OD-05-TR-5501")).toHaveValue(/.+/);
  await page.getByRole("button", { name: "Save pairing" }).click();
  await expect(page.getByText("Saved.")).toBeVisible();
  await expect(page.getByText("2 of 2 available vehicles have a driver")).toBeVisible();
  await page.screenshot({ path: "test-results/screens/fleet-import.png", fullPage: true });
});
