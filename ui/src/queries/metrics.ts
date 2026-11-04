import { ApiError } from "@/queries/utils";

export interface Sample {
  name: string;
  labels: Record<string, string>;
  value: number;
}

const LINE = /^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{(.*)\})?\s+(\S+)/;
const LABEL = /([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"/g;

export const parseMetrics = (text: string): Sample[] =>
  text.split("\n").flatMap((line) => {
    if (line.startsWith("#")) return [];
    const match = LINE.exec(line);
    if (!match) return [];
    const value = Number(match[3]);
    if (!Number.isFinite(value)) return [];
    const labels: Record<string, string> = {};
    for (const [, key, val] of (match[2] ?? "").matchAll(LABEL)) {
      labels[key] = val;
    }
    return [{ name: match[1], labels, value }];
  });

export const sumOf = (
  samples: Sample[],
  name: string,
  labels: Record<string, string> = {},
): number | undefined => {
  const matching = samples.filter(
    (s) =>
      s.name === name &&
      Object.entries(labels).every(([k, v]) => s.labels[k] === v),
  );
  if (matching.length === 0) return undefined;
  return matching.reduce((total, s) => total + s.value, 0);
};

export const getMetrics = async (): Promise<Sample[]> => {
  let response: Response;
  try {
    response = await fetch("/api/v1/metrics");
  } catch {
    throw new ApiError("network", "Cannot reach the server.");
  }
  if (!response.ok) {
    throw new ApiError("http", `HTTP ${response.status}`, response.status);
  }
  return parseMetrics(await response.text());
};
