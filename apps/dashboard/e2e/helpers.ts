import { expect, type Page } from "@playwright/test";

export const PLANNING_DATE = "2026-11-02";

/** Skip with a clear message when the Go API is not running behind the Vite proxy. */
export async function requireApi(page: Page) {
  const res = await page.request.post("/api/v1/auth/login", { data: {} }).catch(() => null);
  if (!res || res.status() >= 500 || res.status() === 404) {
    throw new Error("The Go API is not reachable through the dev proxy: run `make up` and `make api` first.");
  }
}

/** Creates a fresh organization through the UI and lands on the Orders page. */
export async function signUp(page: Page, label = "e2e") {
  const slug = `${label}-${Date.now()}`;
  await page.goto("/signup");
  await page.getByLabel("Organization name").fill("Kalinga Couriers");
  await page.getByLabel("Slug").fill(slug);
  await page.getByLabel("Admin email").fill("ops@kalinga.test");
  await page.getByLabel("Password").fill("dashboard-e2e-pass");
  await page.getByRole("button", { name: "Create organization" }).click();
  await expect(page.getByRole("heading", { name: "Orders", level: 1 })).toBeVisible();
  return slug;
}

/** Seeds a depot, paired vans and orders through the API (the fleet UI has its own test). */
export async function seedDay(page: Page, slug: string, { vans = 3, orders = 30, capacityKg = 400 } = {}) {
  const api = page.request;
  const login = await api.post("/api/v1/auth/login", { data: { organization: slug, email: "ops@kalinga.test", password: "dashboard-e2e-pass" } });
  const token = (await login.json()).access_token as string;
  const headers = { Authorization: `Bearer ${token}` };
  const post = async (path: string, data: unknown) => {
    const res = await api.post(`/api/v1${path}`, { headers, data });
    expect(res.ok(), `${path}: ${await res.text()}`).toBeTruthy();
    return res.json();
  };
  const depot = await post("/depots", { name: "Bhubaneswar Hub", lat: 20.2961, lon: 85.8245 });
  const assignments = [];
  for (let i = 0; i < vans; i++) {
    const v = await post("/vehicles", { depot_id: depot.id, registration_number: `OD-02-PW-${i + 1}`, vehicle_type: "van",
      capacity_weight_kg: capacityKg, capacity_volume_m3: 3, cost_per_km: 12, fixed_cost: 600 });
    const d = await post("/drivers", { name: `Driver ${i + 1}`, shift_start: "08:00", shift_end: "18:00", break_duration_minutes: 30, max_overtime_minutes: 60 });
    assignments.push({ vehicle_id: v.id, driver_id: d.id });
  }
  const pair = await api.put(`/api/v1/driver-assignments/${PLANNING_DATE}`, { headers, data: { assignments } });
  expect(pair.ok()).toBeTruthy();
  const rows = ["order_id,customer,lat,lon,weight_kg,from,to"];
  for (let i = 0; i < orders; i++) {
    const a = (i * 137.5 * Math.PI) / 180;
    const r = 0.01 + (i % 10) * 0.006;
    rows.push(`PW-${i},Customer ${i},${(20.2961 + r * Math.cos(a)).toFixed(5)},${(85.8245 + r * Math.sin(a)).toFixed(5)},${5 + (i % 7)},${i % 2 ? "13:00" : "09:00"},${i % 2 ? "18:00" : "13:00"}`);
  }
  const imp = await api.post(`/api/v1/orders/bulk-import?planning_date=${PLANNING_DATE}`, { headers: { ...headers, "Content-Type": "text/csv" }, data: rows.join("\n") });
  expect(imp.status(), await imp.text()).toBe(201);
}
