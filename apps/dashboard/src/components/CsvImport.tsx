import { useMutation } from "@tanstack/react-query";
import { useRef, useState, type ReactNode } from "react";
import { api, ApiError } from "../api/client";
import type { ImportResult, RowError } from "../api/types";
import { Button, Card, ErrorBox } from "./ui";

/** Row-level problems from an all-or-nothing import, as a compact table. */
export function RowErrors({ errors, total, message }: { errors: RowError[]; total: number; message: string }) {
  return (
    <div role="alert" className="rounded-sm bg-rose-50 p-3 ring-1 ring-rose-200">
      <p className="mb-2 font-medium text-rose-800">
        {message}: {total} problem{total === 1 ? "" : "s"} found. Fix the file and upload it again.
      </p>
      <table className="text-left text-xs text-rose-900">
        <thead>
          <tr><th className="pr-4">Line</th><th className="pr-4">Column</th><th>Problem</th></tr>
        </thead>
        <tbody>
          {errors.map((e, i) => (
            <tr key={i}><td className="pr-4 font-mono">{e.row}</td><td className="pr-4 font-mono">{e.field || "—"}</td><td>{e.message}</td></tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** A file picker that hands the chosen file to `onFile`, then clears itself. */
function FilePicker({ label, busy, onFile, children }: { label: string; busy: boolean; onFile: (f: File) => void; children?: ReactNode }) {
  const input = useRef<HTMLInputElement>(null);
  return (
    <div className="flex flex-wrap items-center gap-3">
      <input ref={input} type="file" accept=".csv,text/csv" aria-label={label}
        className="max-w-full text-sm file:mr-3 file:rounded-sm file:border-0 file:bg-ink file:px-3 file:py-1.5 file:font-medium file:text-cream hover:file:bg-ink/85" />
      {children}
      <Button
        disabled={busy}
        onClick={() => {
          const file = input.current?.files?.[0];
          if (!file) return;
          onFile(file);
          input.current!.value = "";
        }}
      >
        {busy ? "Importing…" : "Import"}
      </Button>
    </div>
  );
}

/**
 * Upload a CSV to an all-or-nothing `bulk-import` endpoint. The server validates every row and
 * either creates all of them or reports every bad cell.
 */
export function CsvImportCard({ title, path, noun, columns, onDone }: {
  title: string;
  path: string;
  noun: string;
  columns: ReactNode;
  onDone: () => void;
}) {
  const [skipExisting, setSkipExisting] = useState(false);
  const upload = useMutation({
    mutationFn: (file: File) =>
      api<ImportResult>(path, { method: "POST", body: file, contentType: "text/csv", query: { skip_existing: skipExisting || undefined } }),
    onSuccess: onDone,
  });
  const rejected =
    upload.error instanceof ApiError && upload.error.code === "IMPORT_REJECTED"
      ? (upload.error.details as { errors: RowError[]; total_errors: number })
      : null;

  return (
    <Card title={title}>
      <div className="space-y-3 text-sm">
        <p className="text-ink-muted">{columns}</p>
        <FilePicker label={`${title} file`} busy={upload.isPending} onFile={(f) => upload.mutate(f)}>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={skipExisting} onChange={(e) => setSkipExisting(e.target.checked)} />
            Skip existing
          </label>
        </FilePicker>
        {upload.isSuccess && (
          <p role="status" className="text-emerald-700">
            Imported {upload.data.imported} {noun}{upload.data.skipped_existing > 0 && `, skipped ${upload.data.skipped_existing} that already exist`}.
          </p>
        )}
        {rejected ? (
          <RowErrors errors={rejected.errors} total={rejected.total_errors} message="Nothing was imported" />
        ) : (
          <ErrorBox error={upload.error} />
        )}
      </div>
    </Card>
  );
}

/** A local-only CSV picker: the file is parsed in the browser and handed to `onRows`. */
export function CsvFileCard({ title, columns, onRows }: {
  title: string;
  columns: ReactNode;
  onRows: (rows: Record<string, string>[]) => { applied: number; errors: RowError[] };
}) {
  const [result, setResult] = useState<{ applied: number; errors: RowError[] } | null>(null);
  const [error, setError] = useState<unknown>(null);
  return (
    <Card title={title}>
      <div className="space-y-3 text-sm">
        <p className="text-ink-muted">{columns}</p>
        <FilePicker
          label={`${title} file`}
          busy={false}
          onFile={(f) => {
            setResult(null);
            setError(null);
            f.text().then((t) => setResult(onRows(parseCsv(t))), setError);
          }}
        />
        {result && result.errors.length === 0 && (
          <p role="status" className="text-emerald-700">Filled {result.applied} pairs. Review them, then save.</p>
        )}
        {result && result.errors.length > 0 && (
          <RowErrors errors={result.errors} total={result.errors.length} message="Nothing was filled in" />
        )}
        <ErrorBox error={error} />
      </div>
    </Card>
  );
}

/**
 * Minimal RFC 4180 CSV parser: quoted fields, doubled quotes, CRLF. Returns one object per data
 * row keyed by the lower-cased, trimmed header, with `__line` set to the 1-based line number.
 */
export function parseCsv(text: string): Record<string, string>[] {
  const records: { line: number; cells: string[] }[] = [];
  let cells: string[] = [];
  let cell = "";
  let quoted = false;
  let line = 1;
  let start = 1;
  const src = text.replace(/^﻿/, "");
  for (let i = 0; i < src.length; i++) {
    const c = src[i];
    if (quoted) {
      if (c === '"' && src[i + 1] === '"') {
        cell += '"';
        i++;
      } else if (c === '"') {
        quoted = false;
      } else {
        if (c === "\n") line++;
        cell += c;
      }
    } else if (c === '"') {
      quoted = true;
    } else if (c === ",") {
      cells.push(cell);
      cell = "";
    } else if (c === "\n" || c === "\r") {
      if (c === "\r" && src[i + 1] === "\n") i++;
      cells.push(cell);
      records.push({ line: start, cells });
      cells = [];
      cell = "";
      line++;
      start = line;
    } else {
      cell += c;
    }
  }
  if (cell !== "" || cells.length > 0) {
    cells.push(cell);
    records.push({ line: start, cells });
  }
  const nonEmpty = records.filter((r) => r.cells.some((c) => c.trim() !== ""));
  if (nonEmpty.length === 0) return [];
  const header = nonEmpty[0]!.cells.map((h) => h.trim().toLowerCase());
  return nonEmpty.slice(1).map((r) => {
    const row: Record<string, string> = { __line: String(r.line) };
    header.forEach((h, i) => (row[h] = (r.cells[i] ?? "").trim()));
    return row;
  });
}
