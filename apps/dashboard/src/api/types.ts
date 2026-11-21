// Shapes of the Go API's JSON (services/backend/internal/*). Times are RFC 3339 strings.

export type Role = "ADMIN" | "DISPATCHER" | "VIEWER";

export interface Organization {
  id: string;
  name: string;
  slug: string;
  timezone: string;
  currency: string;
  created_at: string;
}

export interface Me {
  organization: Organization;
  user: { id: string; role: Role };
}

export interface Page<T> {
  items: T[];
  limit: number;
  offset: number;
}

export interface Depot {
  id: string;
  name: string;
  lat: number;
  lon: number;
  is_active: boolean;
  created_at: string;
}

export type VehicleStatus = "AVAILABLE" | "UNAVAILABLE" | "MAINTENANCE";

export interface Vehicle {
  id: string;
  depot_id: string;
  registration_number: string;
  vehicle_type: string;
  capacity_weight_kg: number;
  capacity_volume_m3: number;
  cost_per_km: number;
  fixed_cost: number;
  status: VehicleStatus;
  created_at: string;
}

export interface Driver {
  id: string;
  name: string;
  phone: string | null;
  shift_start: string; // "HH:MM"
  shift_end: string;
  break_duration_minutes: number;
  max_overtime_minutes: number;
  status: "ACTIVE" | "INACTIVE";
  created_at: string;
}

export interface Assignment {
  vehicle_id: string;
  registration_number?: string;
  driver_id: string;
  driver_name?: string;
}

export type OrderStatus = "PENDING" | "PLANNED" | "DISPATCHED" | "DELIVERED" | "FAILED" | "CANCELLED";

export interface Order {
  id: string;
  external_order_id: string;
  depot_id: string | null;
  planning_date: string;
  batch_id: string | null;
  customer_name: string;
  customer_phone: string | null;
  lat: number;
  lon: number;
  demand_weight_kg: number;
  demand_volume_m3: number;
  time_window_start: string;
  time_window_end: string;
  service_duration_minutes: number;
  priority: number;
  status: OrderStatus;
  created_at: string;
}

export interface ImportResult {
  imported: number;
  skipped_existing: number;
  rows: number;
}

export interface RowError {
  row: number;
  field: string;
  message: string;
}

export type JobStatus =
  | "QUEUED" | "PREPROCESSING" | "SOLVING" | "VALIDATING"
  | "COMPLETED" | "FAILED" | "CANCELLED" | "INFEASIBLE" | "TIME_LIMIT";

export const ACTIVE_JOB: JobStatus[] = ["QUEUED", "PREPROCESSING", "SOLVING", "VALIDATING"];

export type Algorithm = "NEAREST_NEIGHBOUR" | "CLARKE_WRIGHT" | "ORTOOLS_GLS";

export interface Job {
  id: string;
  kind: string;
  status: JobStatus;
  planning_date: string;
  algorithm: string;
  order_count: number;
  vehicle_count: number;
  objective_value: number | null;
  total_distance_km: number | null;
  execution_time_ms: number | null;
  error_message: string | null;
  cancel_requested: boolean;
  attempts: number;
  created_at: string;
  started_at: string | null;
  completed_at: string | null;
}

export type PlanStatus = "DRAFT" | "APPROVED" | "ACTIVE" | "REJECTED" | "SUPERSEDED";

export interface Objective {
  total: number;
  travel: number;
  lateness: number;
  fleet: number;
  overtime: number;
  unserved: number;
  stability: number;
}

export interface PlanSummary {
  id: string;
  optimization_job_id: string;
  planning_date: string;
  version: number;
  status: PlanStatus;
  algorithm: string;
  total_distance_km: number;
  total_cost: number;
  objective: Objective;
  unassigned_count: number;
  route_count: number;
  created_at: string;
  approved_at: string | null;
  activated_at: string | null;
}

export interface Stop {
  id: string;
  order_id: string;
  external_order_id: string;
  customer_name: string;
  lat: number;
  lon: number;
  sequence: number;
  planned_arrival: string;
  planned_departure: string;
  status: string;
  time_window_start: string;
  time_window_end: string;
}

export interface Route {
  id: string;
  route_plan_id: string;
  vehicle_id: string;
  registration_number: string;
  driver_id: string | null;
  driver_name: string | null;
  status: string;
  total_distance_km: number;
  estimated_duration_minutes: number;
  stops: Stop[];
}

export interface Plan extends PlanSummary {
  approved_by: string | null;
  unassigned_order_ids: string[];
  routes: Route[];
}

export interface RouteLine {
  route_id: string;
  source: "OSRM" | "STRAIGHT";
  coordinates: [number, number][];
}
