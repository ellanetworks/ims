const pad = (n: number) => String(n).padStart(2, "0");

export const formatTimestamp = (iso: string): string => {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;

  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  );
};

const MONTHS = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
];

// formatDate is the local date of a timestamp, as YYYY-MM-DD.
export const formatDate = (iso: string): string =>
  formatTimestamp(iso).split(" ")[0];

// formatClock is the local time of day of a timestamp, as HH:MM:SS.
export const formatClock = (iso: string): string =>
  formatTimestamp(iso).split(" ")[1] ?? iso;

// formatRecentTimestamp is a timestamp as short as it can be: its time of day today, its date without the year
// this year, else in full.
export const formatRecentTimestamp = (
  iso: string,
  now: Date = new Date(),
): string => {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;

  const sameYear = d.getFullYear() === now.getFullYear();
  if (
    sameYear &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate()
  ) {
    return formatClock(iso);
  }

  return sameYear
    ? `${MONTHS[d.getMonth()]} ${d.getDate()} ${formatClock(iso)}`
    : formatTimestamp(iso);
};

export const startOfLocalDay = (daysBack = 0, now: Date = new Date()): Date => {
  const at = new Date(now);
  at.setDate(at.getDate() - Math.round(daysBack));
  at.setHours(0, 0, 0, 0);
  return at;
};

export const formatDateTime = (
  value: string,
  opts?: { seconds?: boolean },
): string => {
  if (!value) return "";
  const d = new Date(value);
  if (isNaN(d.getTime())) {
    return value.replace(/\s*[+-]\d{4}$/, "");
  }
  const now = new Date();
  const includeYear = d.getFullYear() !== now.getFullYear();
  return d.toLocaleString("en-US", {
    month: "short",
    day: "numeric",
    ...(includeYear && { year: "numeric" }),
    hour: "2-digit",
    minute: "2-digit",
    ...(opts?.seconds && { second: "2-digit" }),
    hour12: false,
  });
};
