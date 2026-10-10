import { describe, expect, it } from "vitest";
import {
  formatClock,
  formatDate,
  formatRecentTimestamp,
  formatTimestamp,
} from "@/utils/dates";

describe("formatTimestamp", () => {
  it("formats an API timestamp in local time", () => {
    expect(formatTimestamp("2026-09-30T12:05:18.843Z")).toBe(
      "2026-09-30 12:05:18",
    );
  });

  it("returns unparseable input unchanged", () => {
    expect(formatTimestamp("not a date")).toBe("not a date");
  });
});

describe("formatRecentTimestamp", () => {
  const now = new Date("2026-10-10T15:00:00.000Z");

  it("is the time of day today", () => {
    expect(formatRecentTimestamp("2026-10-10T13:08:06.000Z", now)).toBe(
      "13:08:06",
    );
  });

  it("is the date without the year this year", () => {
    expect(formatRecentTimestamp("2026-10-09T13:08:06.000Z", now)).toBe(
      "Oct 9 13:08:06",
    );
  });

  it("is in full another year", () => {
    expect(formatRecentTimestamp("2025-12-31T23:59:59.000Z", now)).toBe(
      "2025-12-31 23:59:59",
    );
  });
});

describe("formatDate and formatClock", () => {
  it("split a timestamp into its local date and time of day", () => {
    expect(formatDate("2026-09-30T12:05:18.843Z")).toBe("2026-09-30");
    expect(formatClock("2026-09-30T12:05:18.843Z")).toBe("12:05:18");
  });
});
