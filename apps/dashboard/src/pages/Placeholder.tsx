import { Card } from "../components/ui";

export function Placeholder({ title }: { title: string }) {
  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
      <Card>
        <p className="text-sm text-slate-500">Coming in the next Phase 4 step.</p>
      </Card>
    </div>
  );
}
