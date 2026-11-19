from optiroute.benchmarks.synthetic import ODISHA_DEPOTS, generate
from optiroute.cli import main
from optiroute.matrices import HaversineMatrixProvider
from optiroute.matrices.haversine import haversine_m
from optiroute.pipeline import solve_request


def test_same_seed_same_instance_different_seed_differs():
    assert generate(50, 4, seed=3) == generate(50, 4, seed=3)
    assert generate(50, 4, seed=3).orders != generate(50, 4, seed=4).orders


def test_orders_stay_near_their_depot_and_fit_the_fleet():
    req = generate(300, 10, depots=3, seed=1, max_radius_km=35)
    hubs = {d.id: (d.lat, d.lon) for d in req.depots}
    assert {d.id for d in req.depots} == set(list(ODISHA_DEPOTS)[:3])
    for o in req.orders:
        assert haversine_m(hubs[o.depot_id], (o.lat, o.lon)) <= 35_100
        assert 9 * 3600 <= o.tw_start_s < o.tw_end_s <= 18 * 3600
    biggest = max(v.cap_weight_g for v in req.vehicles)
    assert max(o.weight_g for o in req.orders) <= biggest
    assert {v.start_depot_id for v in req.vehicles} == set(hubs)


def test_generated_instance_solves_cleanly_with_s1():
    req = generate(60, 6, depots=2, seed=2)
    result = solve_request(req, HaversineMatrixProvider(), algorithm="S1")
    assert result.validation.passed
    assert len(result.unassigned) == 0


def test_cli_writes_json(tmp_path):
    out = tmp_path / "req.json"
    assert main(["generate", "--orders", "20", "--vehicles", "2", "-o", str(out)]) == 0
    assert '"timezone": "Asia/Kolkata"' in out.read_text()
