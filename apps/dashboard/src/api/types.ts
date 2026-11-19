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
