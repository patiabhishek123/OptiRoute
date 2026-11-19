// Route colors for the map, following the dataviz method (validated, not eyeballed):
//
// The 8-hue categorical palette below is the validated reference order. On a map every pair of
// routes can be adjacent ("all pairs"), and with all pairs in play no 8-hue palette clears the
// color-vision-deficiency floor (validator: worst ΔE 3.2). So color is never the only cue:
//   - hues are assigned in fixed order by vehicle registration (an entity keeps its color),
//   - routes 9-16 reuse the hues as dashed lines (composite encoding), 17+ fold to neutral gray,
//   - the route list (registration + driver + swatch), selection highlighting, numbered stops
//     and tooltips carry identity.
export const CATEGORICAL = ["#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4", "#008300", "#6250d6", "#e34948"] as const;
export const OTHER = "#8a8f98";

export interface RouteStyle {
  color: string;
  dashed: boolean;
}

/** Style for every vehicle, keyed by vehicle id; stable for a given set of registrations. */
export function routeStyles(vehicles: { vehicle_id: string; registration_number: string }[]): Map<string, RouteStyle> {
  const ordered = [...vehicles].sort((a, b) => a.registration_number.localeCompare(b.registration_number, "en", { numeric: true }));
  const styles = new Map<string, RouteStyle>();
  ordered.forEach((v, i) => {
    const slot = CATEGORICAL[i % CATEGORICAL.length]!;
    styles.set(v.vehicle_id, i < 2 * CATEGORICAL.length ? { color: slot, dashed: i >= CATEGORICAL.length } : { color: OTHER, dashed: false });
  });
  return styles;
}
