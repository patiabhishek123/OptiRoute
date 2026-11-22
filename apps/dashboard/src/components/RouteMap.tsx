import * as maplibregl from "maplibre-gl";
import type { GeoJSONSource, LngLatBoundsLike, MapLayerMouseEvent } from "maplibre-gl";
import maplibreWorkerUrl from "maplibre-gl/dist/maplibre-gl-worker.mjs?url";
import "maplibre-gl/dist/maplibre-gl.css";
import { useEffect, useRef } from "react";

// MapLibre does tile and GeoJSON work in a web worker it loads from next to its own module. Vite's
// dependency pre-bundling moves the module but not the worker (the map silently stays blank), so
// hand MapLibre the worker as a bundled asset URL: works in the dev server and the production build.
maplibregl.setWorkerUrl(maplibreWorkerUrl);

// Light vector basemap without an API key (https://openfreemap.org); override with VITE_MAP_STYLE_URL.
const STYLE_URL = import.meta.env.VITE_MAP_STYLE_URL ?? "https://tiles.openfreemap.org/styles/positron";

export interface MapRoute {
  id: string;
  label: string; // vehicle registration
  color: string;
  dashed: boolean;
  line: [number, number][]; // [lon, lat]
  stops: { lon: number; lat: number; seq: number; title: string; detail: string }[];
}

export interface MapDepot {
  id: string;
  name: string;
  lon: number;
  lat: number;
}

interface Props {
  routes: MapRoute[];
  depots: MapDepot[];
  selected: string | null;
  onSelect: (routeId: string | null) => void;
}

/** Plan map: one line per route, numbered stops, depots. Selecting a route dims the others. */
export function RouteMap({ routes, depots, selected, onSelect }: Props) {
  const container = useRef<HTMLDivElement>(null);
  const map = useRef<maplibregl.Map | null>(null);
  const ready = useRef(false);
  // Work requested before the style finished loading; run once the layers exist.
  const pendingDraw = useRef<(() => void) | null>(null);
  const pendingFit = useRef<(() => void) | null>(null);
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;

  // Create the map once.
  useEffect(() => {
    if (!container.current) return;
    const m = new maplibregl.Map({ container: container.current, style: STYLE_URL, center: [85.82, 20.29], zoom: 10, attributionControl: { compact: true } });
    m.addControl(new maplibregl.NavigationControl({ showCompass: false }), "top-right");
    map.current = m;

    m.on("load", () => {
      m.addSource("routes", { type: "geojson", data: empty() });
      m.addSource("stops", { type: "geojson", data: empty() });
      m.addSource("depots", { type: "geojson", data: empty() });
      // 2px surface casing under each line keeps crossing routes separable.
      m.addLayer({ id: "route-casing", type: "line", source: "routes", layout: { "line-join": "round", "line-cap": "round" },
        paint: { "line-color": "#ffffff", "line-width": 6, "line-opacity": ["get", "opacity"] } });
      m.addLayer({ id: "route-solid", type: "line", source: "routes", filter: ["!", ["get", "dashed"]], layout: { "line-join": "round", "line-cap": "round" },
        paint: { "line-color": ["get", "color"], "line-width": ["get", "width"], "line-opacity": ["get", "opacity"] } });
      m.addLayer({ id: "route-dashed", type: "line", source: "routes", filter: ["get", "dashed"], layout: { "line-join": "round" },
        paint: { "line-color": ["get", "color"], "line-width": ["get", "width"], "line-opacity": ["get", "opacity"], "line-dasharray": [2, 1.5] } });
      m.addLayer({ id: "stop-circles", type: "circle", source: "stops",
        paint: { "circle-radius": 9, "circle-color": ["get", "color"], "circle-opacity": ["get", "opacity"],
          "circle-stroke-color": "#ffffff", "circle-stroke-width": 2, "circle-stroke-opacity": ["get", "opacity"] } });
      m.addLayer({ id: "stop-labels", type: "symbol", source: "stops",
        layout: { "text-field": ["to-string", ["get", "seq"]], "text-size": 10, "text-font": ["Noto Sans Bold"], "text-allow-overlap": true },
        paint: { "text-color": "#ffffff", "text-opacity": ["get", "opacity"] } });
      m.addLayer({ id: "depots", type: "circle", source: "depots",
        paint: { "circle-radius": 11, "circle-color": "#1e1d1a", "circle-stroke-color": "#ffffff", "circle-stroke-width": 3 } });
      m.addLayer({ id: "depot-labels", type: "symbol", source: "depots",
        layout: { "text-field": ["get", "name"], "text-size": 12, "text-font": ["Noto Sans Bold"], "text-offset": [0, 1.6], "text-anchor": "top" },
        paint: { "text-color": "#1e1d1a", "text-halo-color": "#ffffff", "text-halo-width": 2 } });

      const popup = new maplibregl.Popup({ closeButton: false, closeOnClick: false, offset: 12 });
      m.on("mouseenter", "stop-circles", (e: MapLayerMouseEvent) => {
        const f = e.features?.[0];
        if (!f) return;
        m.getCanvas().style.cursor = "pointer";
        const p = f.properties as { title: string; detail: string; route: string };
        popup.setLngLat((f.geometry as GeoJSON.Point).coordinates as [number, number])
          .setHTML(`<strong>${escapeHtml(p.title)}</strong><br>${escapeHtml(p.detail)}<br><span style="color:#625e55">${escapeHtml(p.route)}</span>`)
          .addTo(m);
      });
      m.on("mouseleave", "stop-circles", () => {
        m.getCanvas().style.cursor = "";
        popup.remove();
      });
      for (const layer of ["route-solid", "route-dashed", "stop-circles"]) {
        m.on("click", layer, (e: MapLayerMouseEvent) => {
          const id = e.features?.[0]?.properties?.route_id as string | undefined;
          if (id) onSelectRef.current(id);
        });
      }
      // Render probe: after each fully drawn frame, record what is actually on screen. Tests assert
      // on it (a canvas existing proves nothing: a blank map has one too).
      m.on("idle", () => {
        const el = container.current;
        if (!el) return;
        const ids = (layers: string[]) => new Set(m.queryRenderedFeatures({ layers }).map((f) => `${f.properties.route_id}/${f.properties.seq ?? ""}`));
        el.dataset.renderedRoutes = String(new Set([...ids(["route-solid", "route-dashed"])].map((k) => k.split("/")[0])).size);
        el.dataset.renderedStops = String(ids(["stop-circles"]).size);
      });
      ready.current = true;
      pendingDraw.current?.();
      pendingFit.current?.();
    });
    return () => {
      ready.current = false;
      m.remove();
    };
  }, []);

  // Push data whenever routes, depots or the selection change.
  useEffect(() => {
    const m = map.current;
    if (!m) return;
    const draw = () => {
      const opacity = (id: string) => (selected === null || selected === id ? 1 : 0.2);
      const width = (id: string) => (selected === id ? 5 : 3);
      setData(m, "routes", routes.map((r) => feature("LineString", r.line, {
        route_id: r.id, color: r.color, dashed: r.dashed, opacity: opacity(r.id), width: width(r.id),
      })));
      // Selected route's stops are drawn last, on top.
      const ordered = [...routes].sort((a, b) => Number(a.id === selected) - Number(b.id === selected));
      setData(m, "stops", ordered.flatMap((r) => r.stops.map((s) => feature("Point", [s.lon, s.lat], {
        route_id: r.id, route: r.label, color: r.color, seq: s.seq, title: s.title, detail: s.detail, opacity: opacity(r.id),
      }))));
      setData(m, "depots", depots.map((d) => feature("Point", [d.lon, d.lat], { name: d.name })));
    };
    if (ready.current) draw();
    else pendingDraw.current = draw;
  }, [routes, depots, selected]);

  // Fit the view when the routes or their geometry change (road lines arrive after the plan and
  // reach further than the straight placeholders), but not on selection.
  const routeKey = routes.map((r) => `${r.id}:${r.line.length}`).join();
  useEffect(() => {
    const m = map.current;
    const points = routes.flatMap((r) => r.line).concat(depots.map((d) => [d.lon, d.lat] as [number, number]));
    if (!m || points.length === 0) return;
    const lons = points.map((p) => p[0]);
    const lats = points.map((p) => p[1]);
    const bounds: LngLatBoundsLike = [[Math.min(...lons), Math.min(...lats)], [Math.max(...lons), Math.max(...lats)]];
    const fit = () => m.fitBounds(bounds, { padding: 48, duration: 0 });
    if (ready.current) fit();
    else pendingFit.current = fit;
  }, [routeKey, depots.length]);

  return <div ref={container} className="h-full min-h-[480px] w-full rounded-sm ring-1 ring-line" data-testid="route-map" />;
}

function empty(): GeoJSON.FeatureCollection {
  return { type: "FeatureCollection", features: [] };
}

function feature(type: "Point" | "LineString", coordinates: unknown, properties: Record<string, unknown>): GeoJSON.Feature {
  return { type: "Feature", geometry: { type, coordinates } as GeoJSON.Geometry, properties };
}

function setData(m: maplibregl.Map, source: string, features: GeoJSON.Feature[]) {
  (m.getSource(source) as GeoJSONSource | undefined)?.setData({ type: "FeatureCollection", features });
}

function escapeHtml(s: string) {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}
