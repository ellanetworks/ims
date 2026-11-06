import { describe, expect, it } from "vitest";
import { parseMetrics, sumOf } from "@/queries/metrics";

const TEXT = `# HELP ellaims_peer_requests_total Requests to peers.
# TYPE ellaims_peer_requests_total counter
ellaims_peer_requests_total{interface="cx",result="success"} 10
ellaims_peer_requests_total{interface="cx",result="timeout"} 2
ellaims_peer_requests_total{interface="rx",result="success"} 5
ellaims_build_info{version="v1.2.3",note="a \\"quoted\\" value"} 1
process_resident_memory_bytes 6.7108864e+07
go_gc_duration_seconds{quantile="0.5"} NaN
malformed line
`;

describe("parseMetrics", () => {
  it("parses the samples and skips comments, NaN and malformed lines", () => {
    expect(parseMetrics(TEXT)).toEqual([
      {
        name: "ellaims_peer_requests_total",
        labels: { interface: "cx", result: "success" },
        value: 10,
      },
      {
        name: "ellaims_peer_requests_total",
        labels: { interface: "cx", result: "timeout" },
        value: 2,
      },
      {
        name: "ellaims_peer_requests_total",
        labels: { interface: "rx", result: "success" },
        value: 5,
      },
      {
        name: "ellaims_build_info",
        labels: { version: "v1.2.3", note: 'a \\"quoted\\" value' },
        value: 1,
      },
      {
        name: "process_resident_memory_bytes",
        labels: {},
        value: 67108864,
      },
    ]);
  });
});

describe("sumOf", () => {
  const samples = parseMetrics(TEXT);

  it("sums the samples of a metric", () => {
    expect(sumOf(samples, "ellaims_peer_requests_total")).toBe(17);
  });

  it("sums the samples that have the labels", () => {
    expect(
      sumOf(samples, "ellaims_peer_requests_total", { interface: "cx" }),
    ).toBe(12);
  });

  it("returns undefined for a metric absent", () => {
    expect(sumOf(samples, "ellaims_active_calls")).toBeUndefined();
  });
});
