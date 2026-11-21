// Formatting in the organization's timezone (orders, windows and plans are local to the depot,
// not to the dispatcher's browser).

export function todayIn(timeZone: string, now = new Date()): string {
  // en-CA formats dates as YYYY-MM-DD.
  return new Intl.DateTimeFormat("en-CA", { timeZone, year: "numeric", month: "2-digit", day: "2-digit" }).format(now);
}

export function clock(iso: string, timeZone: string): string {
  return new Intl.DateTimeFormat("en-GB", { timeZone, hour: "2-digit", minute: "2-digit" }).format(new Date(iso));
}

export function timeWindow(start: string, end: string, timeZone: string): string {
  return `${clock(start, timeZone)}–${clock(end, timeZone)}`;
}

export const km = (v: number) => `${v.toLocaleString("en-IN", { maximumFractionDigits: 1 })} km`;

export const money = (v: number, currency: string) =>
  new Intl.NumberFormat("en-IN", { style: "currency", currency, maximumFractionDigits: 0 }).format(v);
