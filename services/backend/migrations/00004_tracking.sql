-- Tracking (spec §12.6, Phase 5): route start/finish times, stop outcomes, and the invariants that
-- make progress trustworthy enough to re-optimize from.

-- +goose Up
ALTER TABLE routing.routes
    ADD COLUMN started_at   TIMESTAMPTZ,
    ADD COLUMN completed_at TIMESTAMPTZ,
    ADD CONSTRAINT routes_completed_after_start CHECK (completed_at >= started_at);

-- A vehicle drives one route at a time.
CREATE UNIQUE INDEX routes_one_in_progress_idx ON routing.routes (vehicle_id) WHERE status = 'IN_PROGRESS';

ALTER TABLE routing.route_stops
    ADD COLUMN failure_reason VARCHAR(200),
    ADD CONSTRAINT route_stops_departure_after_arrival CHECK (actual_departure >= actual_arrival),
    -- Each status carries exactly the timestamps it implies.
    ADD CONSTRAINT route_stops_status_times CHECK (
        CASE stop_status
            WHEN 'PENDING' THEN actual_arrival IS NULL AND actual_departure IS NULL
            WHEN 'ARRIVED' THEN actual_arrival IS NOT NULL AND actual_departure IS NULL
            WHEN 'COMPLETED' THEN actual_arrival IS NOT NULL AND actual_departure IS NOT NULL
            WHEN 'FAILED' THEN actual_arrival IS NOT NULL AND actual_departure IS NOT NULL
            ELSE TRUE
        END),
    ADD CONSTRAINT route_stops_failure_reason CHECK (failure_reason IS NULL OR stop_status = 'FAILED');

-- A vehicle is at one stop at a time.
CREATE UNIQUE INDEX route_stops_one_arrived_idx ON routing.route_stops (route_id) WHERE stop_status = 'ARRIVED';

-- Spec §8.4: a completed delivery cannot be reassigned. Finished stops are frozen: no update may
-- change them. (Deletes are left to tenant cascades; the application never deletes plans.)
-- +goose StatementBegin
CREATE FUNCTION routing.freeze_finished_stops() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.stop_status IN ('COMPLETED', 'FAILED') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'route stop % is % and cannot change', OLD.id, OLD.stop_status
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER route_stops_frozen BEFORE UPDATE ON routing.route_stops
    FOR EACH ROW EXECUTE FUNCTION routing.freeze_finished_stops();

CREATE INDEX gps_events_route_time_idx ON operations.gps_events (route_id, recorded_at DESC)
    WHERE route_id IS NOT NULL;

-- +goose Down
DROP INDEX operations.gps_events_route_time_idx;
DROP TRIGGER route_stops_frozen ON routing.route_stops;
DROP FUNCTION routing.freeze_finished_stops();
DROP INDEX routing.route_stops_one_arrived_idx;
ALTER TABLE routing.route_stops
    DROP CONSTRAINT route_stops_failure_reason,
    DROP CONSTRAINT route_stops_status_times,
    DROP CONSTRAINT route_stops_departure_after_arrival,
    DROP COLUMN failure_reason;
DROP INDEX routing.routes_one_in_progress_idx;
ALTER TABLE routing.routes
    DROP CONSTRAINT routes_completed_after_start,
    DROP COLUMN completed_at,
    DROP COLUMN started_at;
