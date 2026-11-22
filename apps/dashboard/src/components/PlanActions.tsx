import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, ApiError } from "../api/client";
import type { Plan } from "../api/types";
import { useAuth } from "../auth/AuthContext";
import { Button, ErrorBox } from "./ui";

interface Activation {
  plan: Plan;
  superseded_plan_id: string | null;
  unplanned_order_ids: string[];
}

/** Approve / reject / activate (spec §11.1): activation explains replacements and stale plans. */
export function PlanActions({ plan }: { plan: Plan }) {
  const { canWrite } = useAuth();
  const qc = useQueryClient();
  const [confirmReplace, setConfirmReplace] = useState(false);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["plan", plan.id] });
    void qc.invalidateQueries({ queryKey: ["plans", plan.planning_date] });
    void qc.invalidateQueries({ queryKey: ["orders"] });
  };
  const act = useMutation({
    mutationFn: ({ action, body }: { action: "approve" | "reject" | "activate"; body?: unknown }) =>
      api<Plan | Activation>(`/route-plans/${plan.id}/${action}`, { method: "POST", body }),
    onSuccess: (res) => {
      setConfirmReplace(false);
      refresh();
      setLastActivation("plan" in res ? res : null);
    },
    onError: (err) => {
      if (err instanceof ApiError && err.code === "ACTIVE_PLAN_EXISTS") setConfirmReplace(true);
    },
  });
  const [lastActivation, setLastActivation] = useState<Activation | null>(null);
  if (!canWrite) return null;

  const stale = act.error instanceof ApiError && act.error.code === "STALE_PLAN" ? (act.error.details as { external_order_id: string; status: string }[]) : null;
  const shownError = act.error instanceof ApiError && act.error.code === "ACTIVE_PLAN_EXISTS" ? null : act.error;

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-2">
        {plan.status === "DRAFT" && <Button onClick={() => act.mutate({ action: "approve" })} disabled={act.isPending}>Approve</Button>}
        {plan.status === "APPROVED" && <Button onClick={() => act.mutate({ action: "activate" })} disabled={act.isPending}>Activate</Button>}
        {(plan.status === "DRAFT" || plan.status === "APPROVED") && (
          <Button variant="secondary" onClick={() => act.mutate({ action: "reject" })} disabled={act.isPending}>Reject</Button>
        )}
      </div>
      {confirmReplace && (
        <div role="dialog" aria-label="Replace active plan" className="rounded-sm bg-amber-50 p-3 text-sm ring-1 ring-amber-200">
          <p className="font-medium text-amber-900">This day already has an active plan.</p>
          <p className="text-amber-800">Activating replaces it. Orders the new plan does not cover go back to pending.</p>
          <div className="mt-2 flex gap-2">
            <Button onClick={() => act.mutate({ action: "activate", body: { replace_active: true } })} disabled={act.isPending}>Replace active plan</Button>
            <Button variant="secondary" onClick={() => setConfirmReplace(false)}>Keep current plan</Button>
          </div>
        </div>
      )}
      {stale ? (
        <div role="alert" className="rounded-sm bg-rose-50 p-3 text-sm text-rose-800 ring-1 ring-rose-200">
          <p className="font-medium">Orders changed since this plan was optimized; re-optimize the day.</p>
          <ul className="list-inside list-disc">{stale.map((o) => <li key={o.external_order_id}>{o.external_order_id} is now {o.status.toLowerCase()}</li>)}</ul>
        </div>
      ) : (
        <ErrorBox error={shownError} />
      )}
      {lastActivation && lastActivation.unplanned_order_ids.length > 0 && (
        <p role="status" className="text-sm text-amber-800">
          {lastActivation.unplanned_order_ids.length} order(s) from the replaced plan are pending again.
        </p>
      )}
    </div>
  );
}
