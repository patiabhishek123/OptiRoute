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
