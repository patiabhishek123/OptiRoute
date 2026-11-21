package dispatch

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/dispatch/store"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/osrm"
)

const geometryTTL = 7 * 24 * time.Hour

// Geometry serves road-following route lines for the map. A route's stops never change once it
// exists (plans are immutable), so lines are cached by route id. Without OSRM, or when OSRM fails
// for a route, that route falls back to straight segments and says so (source "STRAIGHT").
type Geometry struct {
	Pool  *pgxpool.Pool
	Redis *redis.Client // optional cache
	OSRM  *osrm.Client  // optional; nil means straight lines
	Log   *slog.Logger
}

type RouteLine struct {
	RouteID     uuid.UUID    `json:"route_id"`
	Source      string       `json:"source"` // OSRM | STRAIGHT
	Coordinates []osrm.Point `json:"coordinates"`
}

func (g *Geometry) Routes(mux *http.ServeMux, authn func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/route-plans/{id}/geometry", authn(httpx.Handle(g.Log, g.planGeometry)))
}

func (g *Geometry) planGeometry(w http.ResponseWriter, r *http.Request) error {
	org := auth.MustPrincipal(r).OrgID
	id, err := httpx.PathUUID(r, "id", "route plan")
	if err != nil {
		return err
	}
	q := store.New(g.Pool)
	if _, err := q.GetPlan(r.Context(), store.GetPlanParams{OrganizationID: org, ID: id}); err != nil {
		return httpx.NotFound("route plan") // also hides other organizations' plans
	}
	rows, err := q.PlanWaypoints(r.Context(), store.PlanWaypointsParams{OrganizationID: org, RoutePlanID: id})
	if err != nil {
		return err
	}

	lines := make([]RouteLine, len(rows))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4) // bounded parallelism towards OSRM
	for i, row := range rows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			lines[i] = g.line(r.Context(), row)
		}()
	}
	wg.Wait()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"plan_id": id, "routes": lines})
	return nil
}

func (g *Geometry) line(ctx context.Context, row store.PlanWaypointsRow) RouteLine {
	depot := osrm.Point{row.DepotLon, row.DepotLat}
	waypoints := []osrm.Point{depot}
	for i := range row.StopLats {
		waypoints = append(waypoints, osrm.Point{row.StopLons[i], row.StopLats[i]})
	}
	waypoints = append(waypoints, depot)
	straight := RouteLine{RouteID: row.RouteID, Source: "STRAIGHT", Coordinates: waypoints}
	if g.OSRM == nil || len(row.StopLats) == 0 {
		return straight
	}

	key := "geom:route:" + row.RouteID.String()
	if g.Redis != nil {
		if raw, err := g.Redis.Get(ctx, key).Bytes(); err == nil {
			var cached RouteLine
			if json.Unmarshal(raw, &cached) == nil {
				return cached
			}
		}
	}
	coords, err := g.OSRM.Route(ctx, waypoints)
	if err != nil {
		g.Log.WarnContext(ctx, "route geometry falls back to straight lines", "route_id", row.RouteID, "err", err)
		return straight
	}
	out := RouteLine{RouteID: row.RouteID, Source: "OSRM", Coordinates: coords}
	if g.Redis != nil {
		if raw, err := json.Marshal(out); err == nil {
			g.Redis.Set(ctx, key, raw, geometryTTL)
		}
	}
	return out
}
