# M1: Solomon VRPTW benchmark (S0, S1, S2)

- **Data:** all 56 Solomon 100-customer instances, checksum-pinned (`make fetch-solomon`).
  Distances use the DIMACS convention (truncated to 1 decimal); hard time windows; objective is distance only.
- **Reference:** best-known solutions (BKS) from CVRPLIB via PyVRP/Instances. These minimise distance, not vehicle count.
- **Command:** `make bench ALGOS="S0 S1 S2" TIME_LIMIT=10`. Raw rows are in `solomon-20261119-195219.csv`.
- **Machine:** Intel i5-1135G7 (8 threads); single-threaded solve. OR-Tools has no seed, so S2 results with a time limit vary slightly from machine to machine.
- **Validation:** every plan passed the independent validator.

| Algorithm | Family | Feasible | Median gap | Mean gap | Mean vehicles (BKS) | Mean time |
|---|---|---|---|---|---|---|
| S0 | C1 | 9/9 | 76.7% | 69.3% | 10.4 (10.0) | 0.00 s |
| S0 | C2 | 8/8 | 67.0% | 84.2% | 3.4 (3.0) | 0.00 s |
| S0 | R1 | 12/12 | 41.4% | 39.1% | 14.5 (13.2) | 0.00 s |
| S0 | R2 | 11/11 | 95.1% | 94.6% | 3.6 (5.5) | 0.00 s |
| S0 | RC1 | 8/8 | 37.9% | 35.7% | 14.1 (12.5) | 0.00 s |
| S0 | RC2 | 8/8 | 99.3% | 101.3% | 4.0 (6.2) | 0.00 s |
| S0 | **all** | 56/56 | 69.2% | 69.7% | 8.6 (8.6) | 0.00 s |
| S1 | C1 | 9/9 | 12.3% | 15.1% | 12.1 (10.0) | 0.01 s |
| S1 | C2 | 8/8 | 25.4% | 27.8% | 5.5 (3.0) | 0.02 s |
| S1 | R1 | 12/12 | 18.7% | 19.2% | 18.4 (13.2) | 0.02 s |
| S1 | R2 | 11/11 | 19.9% | 20.4% | 10.6 (5.5) | 0.02 s |
| S1 | RC1 | 8/8 | 20.9% | 20.9% | 18.0 (12.5) | 0.01 s |
| S1 | RC2 | 8/8 | 33.0% | 29.7% | 12.2 (6.2) | 0.02 s |
| S1 | **all** | 56/56 | 20.0% | 21.7% | 13.1 (8.6) | 0.02 s |
| S2 | C1 | 9/9 | 0.0% | 0.2% | 10.0 (10.0) | 10.03 s |
| S2 | C2 | 8/8 | 0.0% | 0.4% | 3.0 (3.0) | 10.03 s |
| S2 | R1 | 12/12 | 3.9% | 4.3% | 14.2 (13.2) | 10.03 s |
| S2 | R2 | 11/11 | 2.3% | 2.6% | 5.5 (5.5) | 10.03 s |
| S2 | RC1 | 8/8 | 5.6% | 6.0% | 14.1 (12.5) | 10.03 s |
| S2 | RC2 | 8/8 | 2.0% | 2.6% | 6.5 (6.2) | 10.03 s |
| S2 | **all** | 56/56 | 2.1% | 2.7% | 9.1 (8.6) | 10.03 s |

**Summary:** S2 (OR-Tools GLS warm-started from S1) finds the BKS on most C instances and stays within about 6% on R and RC. S1 uses noticeably more vehicles than BKS; S2 recovers almost all of that. The remaining gap is largest on the tight-window R1 and RC1 families. Phase 6's custom local search (S3) is aimed there.
