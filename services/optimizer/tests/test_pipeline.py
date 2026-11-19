import json

import pytest
from conftest import FIXTURES
from pydantic import ValidationError

from optiroute.contracts import v1
from optiroute.matrices import HaversineMatrixProvider
from optiroute.matrices.haversine import haversine_m
from optiroute.pipeline import solve_request
from optiroute.preprocessing import UnsupportedRequestError

RAW = json.loads((FIXTURES / "solve-request.json").read_text())


def request(**changes) -> v1.SolveRequest:
    return v1.SolveRequest.model_validate({**RAW, **changes})


def test_example_request_solves_feasibly():
    result = solve_request(request(), HaversineMatrixProvider())
    assert result.status is v1.Status.FEASIBLE
    assert result.validation.passed and not result.proven_optimal
    served = {s.order_id for r in result.routes for s in r.stops}
    assert served | {u.order_id for u in result.unassigned} == {o["id"] for o in RAW["orders"]}
    assert result.meta.matrix_source == "HAVERSINE"
    assert v1.SolveResult.model_validate_json(result.model_dump_json()) == result


def test_depot_bound_orders_go_to_that_depots_vehicles():
    result = solve_request(request(), HaversineMatrixProvider())
    by_vehicle = {r.vehicle_id: {s.order_id for s in r.stops} for r in result.routes}
    assert not {"o-4", "o-5"} & by_vehicle.get("van-1", set())


def test_order_for_depot_without_vehicles_is_reported():
    depots = [*RAW["depots"], {"id": "east", "lat": 52.5, "lon": 13.5}]
    orders = [*RAW["orders"], {**RAW["orders"][0], "id": "o-x", "depot_id": "east"}]
    result = solve_request(request(depots=depots, orders=orders), HaversineMatrixProvider())
    assert v1.Unassigned(order_id="o-x", reason="NO_ELIGIBLE_VEHICLE") in result.unassigned


def test_contract_rejects_unknown_depot_and_bad_windows():
    with pytest.raises(ValidationError, match="unknown depot"):
        request(orders=[{**RAW["orders"][0], "depot_id": "nowhere"}])
    with pytest.raises(ValidationError, match="tw_end_s"):
        request(orders=[{**RAW["orders"][0], "tw_start_s": 5, "tw_end_s": 4}])


def test_reoptimize_and_unimplemented_algorithms_fail_fast():
    with pytest.raises(UnsupportedRequestError):
        solve_request(request(kind="REOPTIMIZE"), HaversineMatrixProvider())
    with pytest.raises(NotImplementedError):
        solve_request(request(), HaversineMatrixProvider(), algorithm="ORTOOLS_GLS")


def test_haversine_distance_is_sane():
    # 1 degree of latitude is ~111.2 km
    assert haversine_m((0.0, 0.0), (1.0, 0.0)) == pytest.approx(111_195, rel=1e-3)
