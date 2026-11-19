-- Plan comparison data (spec §22: compare baseline and optimized results): which algorithm
-- produced the plan, its cost breakdown, and the orders it could not route.

-- +goose Up
ALTER TABLE routing.route_plans
    ADD COLUMN algorithm VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN objective JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN unassigned_order_ids UUID[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE routing.route_plans
    DROP COLUMN unassigned_order_ids,
    DROP COLUMN objective,
    DROP COLUMN algorithm;
