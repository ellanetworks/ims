import { describe, expect, it } from "vitest";
import {
  describeDuration,
  formatDuration,
  parseDuration,
} from "@/utils/durations";

describe("formatDuration", () => {
  it("uses the largest whole unit", () => {
    expect(formatDuration(604800)).toBe("7d");
    expect(formatDuration(3600)).toBe("1h");
    expect(formatDuration(900)).toBe("15m");
    expect(formatDuration(90)).toBe("90s");
    expect(formatDuration(5400)).toBe("90m");
  });
});

describe("parseDuration", () => {
  it("reads a number and a unit", () => {
    expect(parseDuration("7d")).toBe(604800);
    expect(parseDuration(" 1h ")).toBe(3600);
    expect(parseDuration("15 m")).toBe(900);
    expect(parseDuration("30s")).toBe(30);
  });

  it("rejects anything else", () => {
    for (const value of ["", "7", "0s", "-1m", "1.5h", "1w", "1h30m"]) {
      expect(parseDuration(value)).toBeUndefined();
    }
  });

  it("round-trips with formatDuration", () => {
    for (const seconds of [1, 59, 60, 61, 3600, 86400, 172800]) {
      expect(parseDuration(formatDuration(seconds))).toBe(seconds);
    }
  });
});

describe("describeDuration", () => {
  it("names each unit in words", () => {
    expect(describeDuration(300)).toBe("5 minutes");
    expect(describeDuration(3600)).toBe("1 hour");
    expect(describeDuration(5400)).toBe("1 hour 30 minutes");
    expect(describeDuration(86400)).toBe("1 day");
    expect(describeDuration(2592000)).toBe("30 days");
    expect(describeDuration(90061)).toBe("1 day 1 hour 1 minute 1 second");
  });
});
