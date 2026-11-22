import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { api } from "../api/client";
import { ACTIVE_JOB, type Algorithm, type Job, type Page, type PlanStatus, type PlanSummary } from "../api/types";
import { useAuth } from "../auth/AuthContext";
import { Badge, Button, Card, ErrorBox, Field, Select, Table, type Tone } from "../components/ui";
import { clock, km, money, todayIn } from "../lib/format";

export const ALGORITHMS: { id: Algorithm; label: string }[] = [
  { id: "ORTOOLS_GLS", label: "Optimized (OR-Tools)" },
  { id: "CLARKE_WRIGHT", label: "Savings heuristic (Clarke-Wright)" },
  { id: "NEAREST_NEIGHBOUR", label: "Baseline (nearest neighbour)" },
];
export const algorithmLabel = (id: string) => ALGORITHMS.find((a) => a.id === id)?.label ?? id;

export const planTone: Record<PlanStatus, Tone> = {
  DRAFT: "amber",
  APPROVED: "plum",
  ACTIVE: "green",
  REJECTED: "red",
  SUPERSEDED: "neutral",
};
const jobTone = (s: Job["status"]) => (s === "COMPLETED" ? "green" : ACTIVE_JOB.includes(s) ? "plum" : s === "CANCELLED" ? "neutral" : "red");

export function Plans() {
  const { me, canWrite } = useAuth();
  const tz = me!.organization.timezone;
  const [date, setDate] = useState(() => todayIn(tz));

  // Live status: poll every second while any job for the date is still running.
  const jobs = useQuery({
    queryKey: ["jobs", date],
    queryFn: () => api<Page<Job>>("/optimization/jobs", { query: { planning_date: date, limit: 20 } }),
    refetchInterval: (q) => (q.state.data?.items.some((j) => ACTIVE_JOB.includes(j.status)) ? 1000 : false),
  });
  const running = jobs.data?.items.some((j) => ACTIVE_JOB.includes(j.status)) ?? false;
  // A completed job becomes a plan a moment later (job event -> API consumer).
  const plans = useQuery({
    queryKey: ["plans", date],
    queryFn: () => api<Page<PlanSummary>>("/route-plans", { query: { planning_date: date, limit: 50 } }),
    refetchInterval: (q) => {
      const done = jobs.data?.items.filter((j) => j.status === "COMPLETED").length ?? 0;
      return running || (q.state.data?.items.length ?? 0) < done ? 1000 : false;
    },
  });

  const navigate = useNavigate();
  const [picked, setPicked] = useState<string[]>([]);
  const toggle = (id: string) => setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p.slice(-1), id]));

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <h1 className="text-3xl font-normal tracking-tight">Plans</h1>
        <label className="text-sm">
          <span className="block font-medium text-ink">Planning date</span>
          <input type="date" value={date} onChange={(e) => setDate(e.target.value)} className="mt-1 rounded-sm bg-cream px-2 py-1.5 text-sm ring-1 ring-line-strong focus:ring-2 focus:ring-plum focus:outline-none" />
        </label>
      </div>
      <div className="grid gap-4 lg:grid-cols-3">
        {canWrite && <Optimize date={date} />}
        <div className={canWrite ? "lg:col-span-2" : "lg:col-span-3"}>
          <Card title="Optimization jobs">
            <ErrorBox error={jobs.error} />
            <Table
              rows={jobs.data?.items ?? []}
              rowKey={(j) => j.id}
              empty="No optimization runs for this date yet."
              columns={[
                { header: "Started", cell: (j) => clock(j.created_at, tz) },
                { header: "Algorithm", cell: (j) => algorithmLabel(j.algorithm) },
                { header: "Problem", cell: (j) => `${j.order_count} orders · ${j.vehicle_count} vehicles` },
                { header: "Status", cell: (j) => <Badge tone={jobTone(j.status)}>{j.status.toLowerCase().replace("_", " ")}</Badge> },
                { header: "Result", cell: (j) => (j.total_distance_km !== null ? km(j.total_distance_km) : (j.error_message ?? "—")) },
                { header: "Time", cell: (j) => (j.execution_time_ms !== null ? `${(j.execution_time_ms / 1000).toFixed(1)} s` : "—") },
              ]}
            />
          </Card>
        </div>
      </div>
      <Card
        title="Route plans"
        actions={
          <Button variant="secondary" disabled={picked.length !== 2} onClick={() => navigate(`/plans/compare?a=${picked[0]}&b=${picked[1]}`)}>
            Compare selected ({picked.length}/2)
          </Button>
        }
      >
        <ErrorBox error={plans.error} />
        <Table
          rows={plans.data?.items ?? []}
          rowKey={(p) => p.id}
          empty="No plans for this date. Run an optimization."
          columns={[
            {
              header: "Compare",
              cell: (p) => <input type="checkbox" aria-label={`Compare version ${p.version}`} checked={picked.includes(p.id)} onChange={() => toggle(p.id)} />,
            },
            { header: "Plan", cell: (p) => <Link to={`/plans/${p.id}`} className="font-medium text-plum-deep hover:underline">Version {p.version}</Link> },
            { header: "Status", cell: (p) => <Badge tone={planTone[p.status]}>{p.status.toLowerCase()}</Badge> },
            { header: "Algorithm", cell: (p) => algorithmLabel(p.algorithm) },
            { header: "Routes", cell: (p) => p.route_count, className: "text-right" },
            { header: "Distance", cell: (p) => km(p.total_distance_km), className: "text-right" },
            { header: "Cost", cell: (p) => money(p.total_cost, me!.organization.currency), className: "text-right" },
            { header: "Unassigned", cell: (p) => (p.unassigned_count > 0 ? <Badge tone="red">{p.unassigned_count}</Badge> : "0"), className: "text-right" },
          ]}
        />
      </Card>
    </div>
  );
}

function Optimize({ date }: { date: string }) {
  const qc = useQueryClient();
  const [form, setForm] = useState({ algorithm: "ORTOOLS_GLS" as Algorithm, timeLimit: "10", windows: "SOFT" });
  const run = useMutation({
    mutationFn: () =>
      api<Job>("/optimization/jobs", {
        method: "POST",
        // One key per click: a network retry of this request cannot create a second job.
        headers: { "Idempotency-Key": crypto.randomUUID() },
        body: {
          planning_date: date,
          solver: { algorithm: form.algorithm, time_limit_seconds: Number(form.timeLimit), time_windows: form.windows },
        },
      }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["jobs", date] }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    run.mutate();
  };
  return (
    <Card title={`Optimize ${date}`}>
      <form onSubmit={submit} className="space-y-3">
        <Select label="Algorithm" value={form.algorithm} onChange={(e) => setForm({ ...form, algorithm: e.target.value as Algorithm })}>
          {ALGORITHMS.map((a) => <option key={a.id} value={a.id}>{a.label}</option>)}
        </Select>
        <Field label="Time limit (seconds)" type="number" min="1" max="3600" value={form.timeLimit}
          onChange={(e) => setForm({ ...form, timeLimit: e.target.value })} hint="Only the OR-Tools search uses the full limit." />
        <Select label="Delivery windows" value={form.windows} onChange={(e) => setForm({ ...form, windows: e.target.value })}>
          <option value="SOFT">Soft: late deliveries allowed, penalised</option>
          <option value="HARD">Hard: never late (may leave orders unassigned)</option>
        </Select>
        <ErrorBox error={run.error} />
        <Button type="submit" disabled={run.isPending}>{run.isPending ? "Submitting…" : "Run optimization"}</Button>
      </form>
    </Card>
  );
}
