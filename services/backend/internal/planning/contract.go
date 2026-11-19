package planning

// Optimizer contract v1 (docs/ARCHITECTURE.md §4). The JSON Schema in
// packages/api-contracts/optimizer/v1 is the source of truth; contract_test.go validates every
// snapshot this package builds against it, so these structs cannot silently drift.
//
// Units are integers: metres, seconds since local midnight of planning_date, grams, cm³.

const contractVersion = "1.0"

var algorithms = []string{"NEAREST_NEIGHBOUR", "CLARKE_WRIGHT", "ORTOOLS_GLS", "HYBRID_GLS"}

type SolveRequest struct {
	ContractVersion string          `json:"contract_version"`
	JobID           string          `json:"job_id"`
	Kind            string          `json:"kind"`
	PlanningDate    string          `json:"planning_date"`
	Timezone        string          `json:"timezone"`
	Objective       Objective       `json:"objective"`
	Solver          SolverConfig    `json:"solver"`
	Depots          []SnapshotDepot `json:"depots"`
	Vehicles        []Vehicle       `json:"vehicles"`
	Orders          []Order         `json:"orders"`
}

type Objective struct {
	Profile string  `json:"profile"`
	Weights Weights `json:"weights"`
}

type Weights struct {
	LatenessPerMin float64 `json:"lateness_per_min"`
	Unserved       float64 `json:"unserved"`
	OvertimePerMin float64 `json:"overtime_per_min"`
	Reassign       float64 `json:"reassign"`
	Reorder        float64 `json:"reorder"`
}

// profiles maps objective_profile names to weights (currency units).
var profiles = map[string]Weights{
	"COST_BALANCED": {LatenessPerMin: 50, Unserved: 1_000_000, OvertimePerMin: 20, Reassign: 500, Reorder: 100},
}

type SolverConfig struct {
	Algorithm       string `json:"algorithm"`
	TimeLimitS      int    `json:"time_limit_s"`
	AllowUnassigned bool   `json:"allow_unassigned"`
	Seed            int    `json:"seed"`
	TimeWindows     string `json:"time_windows"`
}

type SnapshotDepot struct {
	ID  string  `json:"id"`
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type Vehicle struct {
	ID           string  `json:"id"`
	DriverID     string  `json:"driver_id"`
	StartDepotID string  `json:"start_depot_id"`
	CapWeightG   int64   `json:"cap_weight_g"`
	CapVolumeCm3 int64   `json:"cap_volume_cm3"`
	CostPerKm    float64 `json:"cost_per_km"`
	FixedCost    float64 `json:"fixed_cost"`
	ShiftStartS  int64   `json:"shift_start_s"`
	ShiftEndS    int64   `json:"shift_end_s"`
	MaxOvertimeS int64   `json:"max_overtime_s"`
	BreakS       int64   `json:"break_s"`
}

type Order struct {
	ID        string  `json:"id"`
	DepotID   *string `json:"depot_id"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	WeightG   int64   `json:"weight_g"`
	VolumeCm3 int64   `json:"volume_cm3"`
	TwStartS  int64   `json:"tw_start_s"`
	TwEndS    int64   `json:"tw_end_s"`
	ServiceS  int64   `json:"service_s"`
	Priority  int32   `json:"priority"`
}
