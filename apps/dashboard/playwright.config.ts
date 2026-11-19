import { defineConfig, devices } from "@playwright/test";

// Browser tests drive the real dashboard against the real Go API.
// Prerequisites: `make up` and `make api` (and `make worker` + `make osrm-up` for planning tests).
// The Vite dev server is started here (or reused if `make dashboard` is already running).
export default defineConfig({
  testDir: "e2e",
  timeout: 120_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  reporter: [["list"]],
  use: {
    baseURL: "http://localhost:5173",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } } }],
  webServer: {
    command: "npm run dev -- --port 5173 --strictPort",
    url: "http://localhost:5173",
    reuseExistingServer: true,
  },
});
