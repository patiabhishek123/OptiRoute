"""The checked-in JSON Schemas (used by the Go backend) must match the pydantic models."""

from pathlib import Path

from optiroute.contracts import export_schemas

COMMITTED = Path(__file__).resolve().parents[3] / "packages/api-contracts/optimizer/v1"


def test_committed_schemas_are_up_to_date(tmp_path):
    for generated in export_schemas(tmp_path):
        committed = COMMITTED / generated.name
        assert committed.exists(), f"missing {committed}; run `make schemas`"
        assert committed.read_text() == generated.read_text(), "schema drift; run `make schemas`"
