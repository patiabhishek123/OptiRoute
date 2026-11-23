# OptiRoute demo data

A fictional courier in Bhubaneswar and Cuttack (Odisha): 2 depots, 10 vehicles, 10 drivers, 140 orders.
Everything can be uploaded from the dashboard; no command line is needed.

| File | Upload on | Contents |
|---|---|---|
| `depots.csv` | Fleet → Depots | 2 hubs |
| `vehicles.csv` | Fleet → Vehicles | 6 vans, 2 trucks, 2 bikes (capacity, cost/km, fixed cost) |
| `drivers.csv` | Fleet → Drivers | shifts, breaks, overtime limits |
| `assignments.csv` | Fleet → Daily pairing | vehicle ↔ driver (fills the form; then click **Save pairing**) |
| `orders-invalid.csv` | Orders | 6 broken rows, to show validation (nothing is imported) |
| `orders-day.csv` | Orders | 140 orders, mixed windows and priorities, both depots |
| `orders-rush.csv` | Orders | 12 urgent afternoon orders, for re-optimization |
| `orders-minimal.csv` | Orders | 20 orders using only the required columns |

## Demo steps

Use the **same date on every page** (the default, today, is simplest).

1. **Sign up** a new organization (a new slug each time).
2. **Fleet**: import `depots.csv`, `vehicles.csv`, `drivers.csv` on their tabs.
3. **Fleet → Daily pairing**: import `assignments.csv`, then click **Save pairing**
   ("10 of 10 available vehicles have a driver"). Skipping Save gives
   "no AVAILABLE vehicle is paired with an ACTIVE driver" when optimizing.
4. **Orders**: import `orders-invalid.csv` (error table), then `orders-day.csv` (140 orders).
5. **Plans**: run **Baseline (nearest neighbour)**, then **Optimized (OR-Tools)** with a 15 s limit and soft windows.
6. Tick both plans → **Compare** (cost saving, distance saved, vehicles).
7. Open the OR-Tools plan: map, cost breakdown, route sheet, CSV export. **Approve** → **Activate**.
8. **Orders**: import `orders-rush.csv`; **Plans**: run OR-Tools again; approve and activate (**Replace active plan**).
9. Optional: **Fleet → Vehicles → Mark unavailable** on a van and re-optimize.

## Loading by script instead

`load_demo.py` creates an organization and loads the fleet, pairing and `orders-day.csv` in one go:

```bash
python3 optiroute-demo/load_demo.py --org kalinga-demo-1 --date 2026-11-24             # local (make api)
python3 optiroute-demo/load_demo.py --org kalinga-demo-1 --api https://<host>/api/v1   # deployed
```

It prints the login (organization, `dispatcher@kalinga.demo`, `kalinga-demo-pass`).
