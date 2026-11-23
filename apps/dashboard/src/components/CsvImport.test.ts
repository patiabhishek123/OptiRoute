import { describe, expect, it } from "vitest";
import { parseCsv } from "./CsvImport";

describe("parseCsv", () => {
  it("keys rows by lower-cased header and records line numbers", () => {
    const rows = parseCsv("﻿Registration_Number, Driver_Name\r\nOD-1, Ramesh Behera\r\n\r\nOD-2,Sunita Das\n");
    expect(rows).toEqual([
      { __line: "2", registration_number: "OD-1", driver_name: "Ramesh Behera" },
      { __line: "4", registration_number: "OD-2", driver_name: "Sunita Das" },
    ]);
  });

  it("handles quoted fields with commas, doubled quotes and newlines", () => {
    const rows = parseCsv('name,note\n"Das, Sunita","said ""hi""\nthere"\nLast,x');
    expect(rows[0]).toEqual({ __line: "2", name: "Das, Sunita", note: 'said "hi"\nthere' });
    expect(rows[1]).toEqual({ __line: "4", name: "Last", note: "x" });
  });

  it("fills missing trailing cells with empty strings and ignores an empty file", () => {
    expect(parseCsv("a,b\n1\n")).toEqual([{ __line: "2", a: "1", b: "" }]);
    expect(parseCsv("")).toEqual([]);
    expect(parseCsv("a,b\n")).toEqual([]);
  });
});
