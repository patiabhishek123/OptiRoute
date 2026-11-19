// Package osrm is a minimal client for OSRM's /route service (road geometry for the map).
package osrm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Point is [lon, lat], the GeoJSON / OSRM coordinate order.
type Point [2]float64

type Client struct {
	BaseURL string
	Profile string // default "driving"
	HTTP    *http.Client
	// MaxWaypoints per request; longer routes are fetched in overlapping chunks (default 100).
	MaxWaypoints int
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		Profile:      "driving",
		HTTP:         &http.Client{Timeout: 10 * time.Second},
		MaxWaypoints: 100,
	}
}

// Route returns the road geometry through the waypoints in order.
func (c *Client) Route(ctx context.Context, waypoints []Point) ([]Point, error) {
	if len(waypoints) < 2 {
		return waypoints, nil
	}
	var line []Point
	// Chunks share their boundary waypoint so the pieces join up.
	for start := 0; start < len(waypoints)-1; start += c.MaxWaypoints - 1 {
		end := min(start+c.MaxWaypoints, len(waypoints))
		part, err := c.route(ctx, waypoints[start:end])
		if err != nil {
			return nil, err
		}
		if len(line) > 0 && len(part) > 0 {
			part = part[1:] // drop the duplicated join point
		}
		line = append(line, part...)
	}
	return line, nil
}

func (c *Client) route(ctx context.Context, waypoints []Point) ([]Point, error) {
	coords := make([]string, len(waypoints))
	for i, p := range waypoints {
		coords[i] = fmt.Sprintf("%.6f,%.6f", p[0], p[1])
	}
	url := fmt.Sprintf("%s/route/v1/%s/%s?overview=full&geometries=geojson&continue_straight=false",
		c.BaseURL, c.Profile, strings.Join(coords, ";"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Routes  []struct {
			Geometry struct {
				Coordinates []Point `json:"coordinates"`
			} `json:"geometry"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("osrm: status %d, undecodable body: %w", resp.StatusCode, err)
	}
	if body.Code != "Ok" || len(body.Routes) == 0 {
		return nil, fmt.Errorf("osrm: %s: %s", body.Code, body.Message)
	}
	return body.Routes[0].Geometry.Coordinates, nil
}
