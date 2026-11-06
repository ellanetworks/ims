const units: [string, number][] = [
  ["d", 86400],
  ["h", 3600],
  ["m", 60],
  ["s", 1],
];

export function formatDuration(seconds: number): string {
  for (const [unit, size] of units) {
    if (seconds >= size && seconds % size === 0)
      return `${seconds / size}${unit}`;
  }
  return `${seconds}s`;
}

export function parseDuration(value: string): number | undefined {
  const match = /^(\d+)\s*([dhms])$/.exec(value.trim());
  if (!match) return undefined;

  const size = units.find(([unit]) => unit === match[2])![1];
  const seconds = Number(match[1]) * size;
  return seconds >= 1 && Number.isSafeInteger(seconds) ? seconds : undefined;
}

const names: [string, number][] = [
  ["day", 86400],
  ["hour", 3600],
  ["minute", 60],
  ["second", 1],
];

export function describeDuration(seconds: number): string {
  const parts: string[] = [];
  let rest = seconds;
  for (const [name, size] of names) {
    const count = Math.floor(rest / size);
    rest -= count * size;
    if (count > 0) parts.push(`${count} ${name}${count === 1 ? "" : "s"}`);
  }
  return parts.join(" ") || "0 seconds";
}
