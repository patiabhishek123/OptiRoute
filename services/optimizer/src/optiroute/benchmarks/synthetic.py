"""Seeded synthetic regional instances for Odisha (spec §19.1, "synthetic regional orders").

Orders are scattered around each depot (normal radius in km, clipped to ``max_radius_km``), with:

* time windows: 40% morning (09-13), 35% afternoon (13-18), 25% all day (09-18)
* weights: mostly small parcels (lognormal around 3 kg), some bulky 20-60 kg drops
* service time: 3 min plus 1 min per 10 kg
* fleet: an even mix of light vans (500 kg) and medium trucks (1.5 t) per depot,
  each vehicle paired with a driver on a 08:00-18:00 shift with a 30 min break and 1 h max overtime

Points can land off-road; OSRM snaps them to the nearest road. Same arguments, same instance.
"""

from __future__ import annotations

import datetime as dt
import math
import random

from optiroute.contracts import v1

# Inland Odisha distribution hubs (lat, lon).
ODISHA_DEPOTS: dict[str, tuple[float, float]] = {
    "bhubaneswar": (20.2961, 85.8245),
    "cuttack": (20.4625, 85.8830),
    "sambalpur": (21.4669, 83.9812),
    "rourkela": (22.2604, 84.8536),
    "berhampur": (19.3150, 84.7941),
}

_VEHICLES = (
    # kind, capacity kg, volume m3, cost per km, fixed cost
    ("van", 500, 4.0, 12.0, 800.0),
    ("truck", 1500, 12.0, 22.0, 1500.0),
)
_WINDOWS = ((9, 13, 0.40), (13, 18, 0.35), (9, 18, 0.25))


def generate(
    orders: int,
    vehicles: int,
    *,
    depots: int = 2,
    seed: int = 0,
    radius_km: float = 12.0,
    max_radius_km: float = 35.0,
    planning_date: dt.date = dt.date(2026, 11, 2),
) -> v1.SolveRequest:
    if not 1 <= depots <= len(ODISHA_DEPOTS):
        raise ValueError(f"depots must be 1..{len(ODISHA_DEPOTS)}")
    if vehicles < depots:
        raise ValueError("need at least one vehicle per depot")
    rng = random.Random(seed)
    hubs = list(ODISHA_DEPOTS.items())[:depots]

    depot_models = [v1.Depot(id=name, lat=lat, lon=lon) for name, (lat, lon) in hubs]
    fleet = []
    for k in range(vehicles):
        depot = hubs[k % depots][0]
        kind, cap_kg, vol_m3, per_km, fixed = _VEHICLES[(k // depots) % len(_VEHICLES)]
        fleet.append(
            v1.Vehicle(
                id=f"{kind}-{k + 1:03d}",
                driver_id=f"drv-{k + 1:03d}",
                start_depot_id=depot,
                cap_weight_g=cap_kg * 1000,
                cap_volume_cm3=round(vol_m3 * 1_000_000),
                cost_per_km=per_km,
                fixed_cost=fixed,
                shift_start_s=8 * 3600,
                shift_end_s=18 * 3600,
                max_overtime_s=3600,
                break_s=1800,
            )
        )

    order_models = []
    for i in range(orders):
        depot, (lat0, lon0) = hubs[i % depots]
        lat, lon = _scatter(rng, lat0, lon0, radius_km, max_radius_km)
        start_h, end_h, _ = rng.choices(_WINDOWS, weights=[w[2] for w in _WINDOWS])[0]
        bulky = rng.random() < 0.08
        kg = rng.uniform(20, 60) if bulky else min(15.0, rng.lognormvariate(math.log(3), 0.6))
        order_models.append(
            v1.Order(
                id=f"ord-{i + 1:05d}",
                depot_id=depot,
                lat=round(lat, 6),
                lon=round(lon, 6),
                weight_g=round(kg * 1000),
                volume_cm3=round(kg * 4000),  # ~250 kg/m3 parcel density
                tw_start_s=start_h * 3600,
                tw_end_s=end_h * 3600,
                service_s=180 + round(kg / 10 * 60),
                priority=2 if rng.random() < 0.1 else 1,
            )
        )

    return v1.SolveRequest(
        job_id=f"synthetic-odisha-{orders}o-{vehicles}v-{depots}d-s{seed}",
        planning_date=planning_date,
        timezone="Asia/Kolkata",
        solver=v1.SolverConfig(algorithm=v1.Algorithm.ORTOOLS_GLS, seed=seed),
        depots=depot_models,
        vehicles=fleet,
        orders=order_models,
    )


def _scatter(
    rng: random.Random, lat0: float, lon0: float, radius_km: float, max_km: float
) -> tuple[float, float]:
    r = min(abs(rng.gauss(0, radius_km)), max_km)
    theta = rng.uniform(0, 2 * math.pi)
    dlat = r * math.cos(theta) / 111.32
    dlon = r * math.sin(theta) / (111.32 * math.cos(math.radians(lat0)))
    return lat0 + dlat, lon0 + dlon
