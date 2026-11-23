import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import type { Assignment, Depot, Driver, Page, RowError, Vehicle } from "../api/types";
import { useAuth } from "../auth/AuthContext";
import { CsvFileCard, CsvImportCard } from "../components/CsvImport";
import { Badge, Button, Card, ErrorBox, Field, Select, Table, Tabs } from "../components/ui";
import { todayIn } from "../lib/format";

type Tab = "depots" | "vehicles" | "drivers" | "pairing";

export function Fleet() {
  const [tab, setTab] = useState<Tab>("depots");
  return (
    <div className="space-y-4">
        <h1 className="text-3xl font-normal tracking-tight">Fleet</h1>
        <Tabs<Tab>
          value={tab}
          onChange={setTab}
          tabs={[
            { id: "depots", label: "Depots" },
            { id: "vehicles", label: "Vehicles" },
            { id: "drivers", label: "Drivers" },
            { id: "pairing", label: "Daily pairing" },
          ]}
        />
        {tab === "depots" && <Depots />}
        {tab === "vehicles" && <Vehicles />}
        {tab === "drivers" && <Drivers />}
        {tab === "pairing" && <Pairing />}
    </div>
  );
}

const useDepots = () => useQuery({ queryKey: ["depots"], queryFn: () => api<Page<Depot>>("/depots", { query: { limit: 500 } }) });
const useVehicles = () => useQuery({ queryKey: ["vehicles"], queryFn: () => api<Page<Vehicle>>("/vehicles", { query: { limit: 500 } }) });
const useDrivers = () => useQuery({ queryKey: ["drivers"], queryFn: () => api<Page<Driver>>("/drivers", { query: { limit: 500 } }) });

/** A create form backed by a POST; invalidates `key` and resets on success. */
function useCreate<T>(path: string, key: string, initial: T, toBody: (f: T) => unknown) {
  const qc = useQueryClient();
  const [form, setForm] = useState(initial);
  const m = useMutation({
    mutationFn: () => api(path, { method: "POST", body: toBody(form) }),
    onSuccess: () => {
      setForm(initial);
      void qc.invalidateQueries({ queryKey: [key] });
    },
  });
  const field = (k: keyof T) => ({
    value: String(form[k] ?? ""),
    onChange: (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setForm({ ...form, [k]: e.target.value }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    m.mutate();
  };
  return { form, field, submit, m };
}

function Depots() {
  const { canWrite } = useAuth();
  const qc = useQueryClient();
  const depots = useDepots();
  const c = useCreate("/depots", "depots", { name: "", lat: "", lon: "" }, (f) => ({ name: f.name, lat: Number(f.lat), lon: Number(f.lon) }));
  return (
    <div className="grid gap-4 lg:grid-cols-3">
      <div className="lg:col-span-2">
        <Card title="Depots">
          <ErrorBox error={depots.error} />
          <Table
            rows={depots.data?.items ?? []}
            rowKey={(d) => d.id}
            empty="No depots yet. Add your first warehouse."
            columns={[
              { header: "Name", cell: (d) => <span className="font-medium">{d.name}</span> },
              { header: "Location", cell: (d) => <span className="font-mono text-xs">{d.lat.toFixed(4)}, {d.lon.toFixed(4)}</span> },
              { header: "Status", cell: (d) => (d.is_active ? <Badge tone="green">active</Badge> : <Badge>inactive</Badge>) },
            ]}
          />
        </Card>
      </div>
      {canWrite && (
        <div className="space-y-4">
          <Card title="Add depot">
            <form onSubmit={c.submit} className="space-y-3">
              <Field label="Name" {...c.field("name")} required />
              <div className="grid grid-cols-2 gap-2">
                <Field label="Latitude" type="number" step="any" {...c.field("lat")} required />
                <Field label="Longitude" type="number" step="any" {...c.field("lon")} required />
              </div>
              <ErrorBox error={c.m.error} />
              <Button type="submit" disabled={c.m.isPending}>Add depot</Button>
            </form>
          </Card>
          <CsvImportCard
            title="Import depots from CSV"
            path="/depots/bulk-import"
            noun="depots"
            columns={<>Columns: <code>name, lat, lon</code>. Names must be unique: orders and vehicles refer to depots by name.</>}
            onDone={() => void qc.invalidateQueries({ queryKey: ["depots"] })}
          />
        </div>
      )}
    </div>
  );
}

function Vehicles() {
  const { canWrite } = useAuth();
  const qc = useQueryClient();
  const vehicles = useVehicles();
  const depots = useDepots();
  const depotName = (id: string) => depots.data?.items.find((d) => d.id === id)?.name ?? "—";
  const c = useCreate(
    "/vehicles",
    "vehicles",
    { depot_id: "", registration_number: "", vehicle_type: "van", capacity_weight_kg: "", capacity_volume_m3: "", cost_per_km: "", fixed_cost: "0" },
    (f) => ({
      ...f,
      capacity_weight_kg: Number(f.capacity_weight_kg),
      capacity_volume_m3: Number(f.capacity_volume_m3),
      cost_per_km: Number(f.cost_per_km),
      fixed_cost: Number(f.fixed_cost),
    }),
  );
  const setStatus = useMutation({
    mutationFn: ({ id, status }: { id: string; status: string }) =>
      status === "UNAVAILABLE" ? api(`/vehicles/${id}/unavailable`, { method: "POST" }) : api(`/vehicles/${id}`, { method: "PATCH", body: { status } }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["vehicles"] }),
  });
  const tone = { AVAILABLE: "green", UNAVAILABLE: "red", MAINTENANCE: "amber" } as const;
  return (
    <div className="grid gap-4 lg:grid-cols-3">
      <div className="lg:col-span-2">
        <Card title="Vehicles">
          <ErrorBox error={vehicles.error ?? setStatus.error} />
          <Table
            rows={vehicles.data?.items ?? []}
            rowKey={(v) => v.id}
            empty="No vehicles yet."
            columns={[
              { header: "Registration", cell: (v) => <span className="font-medium">{v.registration_number}</span> },
              { header: "Type", cell: (v) => v.vehicle_type },
              { header: "Depot", cell: (v) => depotName(v.depot_id) },
              { header: "Capacity", cell: (v) => `${v.capacity_weight_kg} kg · ${v.capacity_volume_m3} m³` },
              { header: "Cost", cell: (v) => `${v.cost_per_km}/km + ${v.fixed_cost}` },
              { header: "Status", cell: (v) => <Badge tone={tone[v.status]}>{v.status.toLowerCase()}</Badge> },
              ...(canWrite
                ? [{
                    header: "",
                    cell: (v: Vehicle) =>
                      v.status === "AVAILABLE" ? (
                        <Button variant="secondary" onClick={() => setStatus.mutate({ id: v.id, status: "UNAVAILABLE" })}>Mark unavailable</Button>
                      ) : (
                        <Button variant="secondary" onClick={() => setStatus.mutate({ id: v.id, status: "AVAILABLE" })}>Make available</Button>
                      ),
                  }]
                : []),
            ]}
          />
        </Card>
      </div>
      {canWrite && (
        <div className="space-y-4">
          <Card title="Add vehicle">
            <form onSubmit={c.submit} className="space-y-3">
              <Select label="Depot" {...c.field("depot_id")} required>
                <option value="">Choose a depot…</option>
                {depots.data?.items.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}
              </Select>
              <Field label="Registration number" placeholder="OD-02-AB-1234" {...c.field("registration_number")} required />
              <Field label="Type" {...c.field("vehicle_type")} required />
              <div className="grid grid-cols-2 gap-2">
                <Field label="Capacity (kg)" type="number" min="0" step="any" {...c.field("capacity_weight_kg")} required />
                <Field label="Capacity (m³)" type="number" min="0" step="any" {...c.field("capacity_volume_m3")} required />
                <Field label="Cost per km" type="number" min="0" step="any" {...c.field("cost_per_km")} required />
                <Field label="Fixed cost / day" type="number" min="0" step="any" {...c.field("fixed_cost")} />
              </div>
              <ErrorBox error={c.m.error} />
              <Button type="submit" disabled={c.m.isPending}>Add vehicle</Button>
            </form>
          </Card>
          <CsvImportCard
            title="Import vehicles from CSV"
            path="/vehicles/bulk-import"
            noun="vehicles"
            columns={<>Columns: <code>registration_number, depot, vehicle_type, capacity_weight_kg, capacity_volume_m3, cost_per_km</code> plus
              optional <code>fixed_cost</code>. <code>depot</code> is a depot name or id.</>}
            onDone={() => void qc.invalidateQueries({ queryKey: ["vehicles"] })}
          />
        </div>
      )}
    </div>
  );
}

function Drivers() {
  const { canWrite } = useAuth();
  const qc = useQueryClient();
  const drivers = useDrivers();
  const c = useCreate(
    "/drivers",
    "drivers",
    { name: "", phone: "", shift_start: "08:00", shift_end: "18:00", break_duration_minutes: "30", max_overtime_minutes: "60" },
    (f) => ({ ...f, phone: f.phone || null, break_duration_minutes: Number(f.break_duration_minutes), max_overtime_minutes: Number(f.max_overtime_minutes) }),
  );
  return (
    <div className="grid gap-4 lg:grid-cols-3">
      <div className="lg:col-span-2">
        <Card title="Drivers">
          <ErrorBox error={drivers.error} />
          <Table
            rows={drivers.data?.items ?? []}
            rowKey={(d) => d.id}
            empty="No drivers yet."
            columns={[
              { header: "Name", cell: (d) => <span className="font-medium">{d.name}</span> },
              { header: "Phone", cell: (d) => d.phone ?? "—" },
              { header: "Shift", cell: (d) => `${d.shift_start}–${d.shift_end}` },
              { header: "Break", cell: (d) => `${d.break_duration_minutes} min` },
              { header: "Max overtime", cell: (d) => `${d.max_overtime_minutes} min` },
              { header: "Status", cell: (d) => (d.status === "ACTIVE" ? <Badge tone="green">active</Badge> : <Badge>inactive</Badge>) },
            ]}
          />
        </Card>
      </div>
      {canWrite && (
        <div className="space-y-4">
          <Card title="Add driver">
            <form onSubmit={c.submit} className="space-y-3">
              <Field label="Name" {...c.field("name")} required />
              <Field label="Phone" {...c.field("phone")} />
              <div className="grid grid-cols-2 gap-2">
                <Field label="Shift start" type="time" {...c.field("shift_start")} required />
                <Field label="Shift end" type="time" {...c.field("shift_end")} required />
                <Field label="Break (min)" type="number" min="0" {...c.field("break_duration_minutes")} />
                <Field label="Max overtime (min)" type="number" min="0" {...c.field("max_overtime_minutes")} />
              </div>
              <ErrorBox error={c.m.error} />
              <Button type="submit" disabled={c.m.isPending}>Add driver</Button>
            </form>
          </Card>
          <CsvImportCard
            title="Import drivers from CSV"
            path="/drivers/bulk-import"
            noun="drivers"
            columns={<>Columns: <code>name, shift_start, shift_end</code> (HH:MM) plus optional <code>phone, break_duration_minutes,
              max_overtime_minutes</code>.</>}
            onDone={() => void qc.invalidateQueries({ queryKey: ["drivers"] })}
          />
        </div>
      )}
    </div>
  );
}

/** Which driver drives which vehicle on a given day (the optimizer routes these pairs). */
function Pairing() {
  const { me, canWrite } = useAuth();
  const qc = useQueryClient();
  const [date, setDate] = useState(() => todayIn(me!.organization.timezone));
  const vehicles = useVehicles();
  const drivers = useDrivers();
  const current = useQuery({
    queryKey: ["pairing", date],
    queryFn: () => api<{ assignments: Assignment[] }>(`/driver-assignments/${date}`),
  });
  const [draft, setDraft] = useState<Record<string, string>>({});
  useEffect(() => {
    setDraft(Object.fromEntries((current.data?.assignments ?? []).map((a) => [a.vehicle_id, a.driver_id])));
  }, [current.data]);

  const save = useMutation({
    mutationFn: () =>
      api(`/driver-assignments/${date}`, {
        method: "PUT",
        body: { assignments: Object.entries(draft).filter(([, d]) => d).map(([vehicle_id, driver_id]) => ({ vehicle_id, driver_id })) },
      }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["pairing", date] }),
  });
  const available = (vehicles.data?.items ?? []).filter((v) => v.status === "AVAILABLE");
  const activeDrivers = (drivers.data?.items ?? []).filter((d) => d.status === "ACTIVE");
  const taken = new Set(Object.values(draft).filter(Boolean));
  const paired = Object.values(draft).filter(Boolean).length;

  const fillFromCsv = (rows: Record<string, string>[]) => {
    const pick = (r: Record<string, string>, ...keys: string[]) => keys.map((k) => r[k]).find((v) => v) ?? "";
    const byReg = new Map(available.map((v) => [v.registration_number.toLowerCase(), v]));
    const allRegs = new Set((vehicles.data?.items ?? []).map((v) => v.registration_number.toLowerCase()));
    const errors: RowError[] = [];
    const next: Record<string, string> = {};
    const usedBy = new Map<string, number>();
    for (const r of rows) {
      const row = Number(r.__line);
      const reg = pick(r, "registration_number", "registration", "vehicle");
      const name = pick(r, "driver_name", "driver", "name");
      const v = byReg.get(reg.toLowerCase());
      if (!reg) errors.push({ row, field: "registration_number", message: "required" });
      else if (!v) errors.push({ row, field: "registration_number", message: allRegs.has(reg.toLowerCase()) ? "vehicle is not AVAILABLE" : "unknown vehicle" });
      else if (next[v.id]) errors.push({ row, field: "registration_number", message: "vehicle listed twice" });
      const matches = activeDrivers.filter((d) => d.name.toLowerCase() === name.toLowerCase());
      if (!name) errors.push({ row, field: "driver_name", message: "required" });
      else if (matches.length === 0) errors.push({ row, field: "driver_name", message: "unknown or inactive driver" });
      else if (matches.length > 1) errors.push({ row, field: "driver_name", message: "several active drivers share this name" });
      const d = matches.length === 1 ? matches[0] : undefined;
      if (d && usedBy.has(d.id)) errors.push({ row, field: "driver_name", message: `driver already paired on line ${usedBy.get(d.id)}` });
      if (v && d && !next[v.id] && !usedBy.has(d.id)) {
        next[v.id] = d.id;
        usedBy.set(d.id, row);
      }
    }
    if (rows.length === 0) errors.push({ row: 1, field: "", message: "the file has no rows" });
    if (errors.length > 0) return { applied: 0, errors };
    setDraft(next);
    save.reset();
    return { applied: Object.keys(next).length, errors };
  };

  return (
    <div className="space-y-4">
      <Card
        title={`Daily pairing · ${paired} of ${available.length} available vehicles have a driver`}
        actions={
          <input type="date" value={date} onChange={(e) => setDate(e.target.value)} aria-label="Pairing date"
            className="rounded-sm px-2 py-1 text-sm ring-1 ring-line-strong" />
        }
      >
        <div className="space-y-3">
          <Table
            rows={available}
            rowKey={(v) => v.id}
            empty="No AVAILABLE vehicles."
            columns={[
              { header: "Vehicle", cell: (v) => <span className="font-medium">{v.registration_number}</span> },
              {
                header: "Driver",
                cell: (v) => (
                  <Select aria-label={`Driver for ${v.registration_number}`} value={draft[v.id] ?? ""} disabled={!canWrite}
                    onChange={(e) => setDraft({ ...draft, [v.id]: e.target.value })}>
                    <option value="">— not driving —</option>
                    {activeDrivers.map((d) => (
                      <option key={d.id} value={d.id} disabled={taken.has(d.id) && draft[v.id] !== d.id}>
                        {d.name} ({d.shift_start}–{d.shift_end})
                      </option>
                    ))}
                  </Select>
                ),
              },
            ]}
          />
          <ErrorBox error={current.error ?? save.error} />
          {canWrite && (
            <div className="flex items-center gap-3">
              <Button onClick={() => save.mutate()} disabled={save.isPending}>Save pairing</Button>
              {save.isSuccess && <span className="text-sm text-emerald-700">Saved.</span>}
            </div>
          )}
        </div>
      </Card>
      {canWrite && (
        <CsvFileCard
          title="Fill pairing from CSV"
          columns={<>Columns: <code>registration_number, driver_name</code>. Replaces the pairing shown above for {date}; vehicles
            not in the file are left without a driver. Nothing is saved until you click <em>Save pairing</em>.</>}
          onRows={fillFromCsv}
        />
      )}
    </div>
  );
}
