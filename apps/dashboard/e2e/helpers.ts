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
