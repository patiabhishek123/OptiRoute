import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { api, ApiError } from "../api/client";
import type { Depot, ImportResult, Order, OrderStatus, Page, RowError } from "../api/types";
import { useAuth } from "../auth/AuthContext";
import { Badge, Button, Card, ErrorBox, Select, Table } from "../components/ui";
import { timeWindow, todayIn } from "../lib/format";

const statusTone: Record<OrderStatus, "slate" | "green" | "amber" | "red" | "indigo"> = {
  PENDING: "amber",
  PLANNED: "indigo",
  DISPATCHED: "indigo",
  DELIVERED: "green",
  FAILED: "red",
  CANCELLED: "slate",
};

export function Orders() {
  const { me, canWrite } = useAuth();
  const tz = me!.organization.timezone;
  const qc = useQueryClient();
  const [date, setDate] = useState(() => todayIn(tz));
  const [status, setStatus] = useState("");

  const orders = useQuery({
    queryKey: ["orders", date, status],
    queryFn: () => api<Page<Order>>("/orders", { query: { planning_date: date, status, limit: 500 } }),
  });
  const depots = useQuery({ queryKey: ["depots"], queryFn: () => api<Page<Depot>>("/depots", { query: { limit: 500 } }) });
  const depotName = (id: string | null) => (id ? (depots.data?.items.find((d) => d.id === id)?.name ?? "—") : "any");

  const cancel = useMutation({
    mutationFn: (id: string) => api(`/orders/${id}/cancel`, { method: "POST" }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["orders"] }),
  });

  const items = orders.data?.items ?? [];
  const counts = items.reduce<Record<string, number>>((acc, o) => ({ ...acc, [o.status]: (acc[o.status] ?? 0) + 1 }), {});

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Orders</h1>
          <p className="text-sm text-slate-500">
            {items.length} orders on {date}
            {Object.entries(counts).map(([s, n]) => ` · ${n} ${s.toLowerCase()}`)}
          </p>
        </div>
        <div className="flex items-end gap-2">
          <label className="text-sm">
            <span className="block font-medium text-slate-700">Planning date</span>
            <input type="date" value={date} onChange={(e) => setDate(e.target.value)} className="mt-1 rounded-md px-2 py-1.5 text-sm ring-1 ring-slate-300" />
          </label>
          <div className="w-40">
            <Select label="Status" value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">All</option>
              {Object.keys(statusTone).map((s) => <option key={s} value={s}>{s.toLowerCase()}</option>)}
            </Select>
          </div>
        </div>
      </div>

      {canWrite && <CsvUpload date={date} onDone={() => void qc.invalidateQueries({ queryKey: ["orders"] })} />}

      <Card title="Orders">
        <ErrorBox error={orders.error ?? cancel.error} />
        <Table
          rows={items}
          rowKey={(o) => o.id}
          empty="No orders for this date. Import a CSV to get started."
          columns={[
            { header: "Order", cell: (o) => <span className="font-mono text-xs font-medium">{o.external_order_id}</span> },
            { header: "Customer", cell: (o) => o.customer_name },
            { header: "Window", cell: (o) => timeWindow(o.time_window_start, o.time_window_end, tz) },
            { header: "Weight", cell: (o) => `${o.demand_weight_kg} kg`, className: "text-right" },
            { header: "Depot", cell: (o) => depotName(o.depot_id) },
            { header: "Priority", cell: (o) => (o.priority > 1 ? <Badge tone="amber">P{o.priority}</Badge> : "—") },
            { header: "Status", cell: (o) => <Badge tone={statusTone[o.status]}>{o.status.toLowerCase()}</Badge> },
            ...(canWrite
              ? [{
                  header: "",
                  cell: (o: Order) =>
                    o.status === "PENDING" ? (
                      <Button variant="secondary" disabled={cancel.isPending} onClick={() => cancel.mutate(o.id)}>Cancel</Button>
                    ) : null,
                }]
              : []),
          ]}
        />
      </Card>
    </div>
  );
}

/** CSV import. The API is all-or-nothing and reports every bad cell; show them as a table. */
function CsvUpload({ date, onDone }: { date: string; onDone: () => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [skipExisting, setSkipExisting] = useState(false);
  const upload = useMutation({
    mutationFn: (file: File) =>
      api<ImportResult>("/orders/bulk-import", {
        method: "POST",
        body: file,
        contentType: "text/csv",
        query: { planning_date: date, skip_existing: skipExisting || undefined },
      }),
    onSuccess: () => {
      onDone();
      if (input.current) input.current.value = "";
    },
  });
  const rowErrors =
    upload.error instanceof ApiError && upload.error.code === "IMPORT_REJECTED"
      ? (upload.error.details as { errors: RowError[]; total_errors: number })
      : null;

  return (
    <Card title="Import orders from CSV">
      <div className="space-y-3 text-sm">
        <p className="text-slate-600">
          Columns: <code>order_id, customer, lat, lon, from, to</code> (required) plus optional <code>weight_kg, volume_m3, phone,
          depot, priority, service_minutes, date</code>. Times like <code>09:00</code> are read in the organization's timezone on
          the planning date ({date}).
        </p>
        <div className="flex flex-wrap items-center gap-4">
          <input ref={input} type="file" accept=".csv,text/csv" aria-label="CSV file"
            className="text-sm file:mr-3 file:rounded-md file:border-0 file:bg-indigo-50 file:px-3 file:py-1.5 file:font-medium file:text-indigo-700" />
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={skipExisting} onChange={(e) => setSkipExisting(e.target.checked)} />
            Skip orders already imported
          </label>
          <Button
            disabled={upload.isPending}
            onClick={() => {
              const file = input.current?.files?.[0];
              if (file) upload.mutate(file);
            }}
          >
            {upload.isPending ? "Importing…" : "Import"}
          </Button>
        </div>
        {upload.isSuccess && (
          <p role="status" className="text-emerald-700">
            Imported {upload.data.imported} orders{upload.data.skipped_existing > 0 && `, skipped ${upload.data.skipped_existing} already imported`}.
          </p>
        )}
        {rowErrors ? (
          <div role="alert" className="rounded-md bg-rose-50 p-3 ring-1 ring-rose-200">
            <p className="mb-2 font-medium text-rose-800">
              Nothing was imported: {rowErrors.total_errors} problem{rowErrors.total_errors === 1 ? "" : "s"} found. Fix the file and upload it again.
            </p>
            <table className="text-left text-xs text-rose-900">
              <thead>
                <tr><th className="pr-4">Line</th><th className="pr-4">Column</th><th>Problem</th></tr>
              </thead>
              <tbody>
                {rowErrors.errors.map((e, i) => (
                  <tr key={i}><td className="pr-4 font-mono">{e.row}</td><td className="pr-4 font-mono">{e.field || "—"}</td><td>{e.message}</td></tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <ErrorBox error={upload.error} />
        )}
      </div>
    </Card>
  );
}
