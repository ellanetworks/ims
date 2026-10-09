import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";
import {
  ANY_RANGE,
  CUSTOM_RANGE,
  DAILY_RANGES,
  fromFirstDateInputValue,
  fromLastDateInputValue,
  isValidBound,
  resolveTimeRangeFilter,
  timeRangeFieldErrors,
  timeRangeFilter,
  timeRangeLabel,
  timeRangeParams,
  toLastInputValue,
  toNextMinute,
  toInputValue,
  toLastDateInputValue,
} from "./TimeRangePicker";

const originalTz = process.env.TZ;

beforeAll(() => {
  process.env.TZ = "America/Toronto";
});

afterAll(() => {
  process.env.TZ = originalTz;
});

const local = (value: string) => new Date(value).toISOString();

describe("timeRangeLabel", () => {
  it("names a custom range by its bounds", () => {
    expect(
      timeRangeLabel({
        preset: CUSTOM_RANGE,
        from: local("2026-09-04T00:00"),
        to: local("2026-10-23T14:30"),
      }),
    ).toBe("Sep 4, 00:00 → Oct 23, 14:30");
  });

  it("names an open-ended range by its bound", () => {
    expect(
      timeRangeLabel({
        preset: CUSTOM_RANGE,
        from: "",
        to: local("2026-10-23T00:00"),
      }),
    ).toBe("Before Oct 23, 00:00");
  });

  it("leaves out a bound it cannot parse", () => {
    expect(
      timeRangeLabel({ preset: CUSTOM_RANGE, from: "not-a-date", to: "" }),
    ).toBe("Custom range");
  });

  it("names a preset by its label", () => {
    expect(
      timeRangeLabel(
        { preset: "7d", from: "", to: "" },
        {
          ranges: DAILY_RANGES,
        },
      ),
    ).toBe("Last 7 days");
  });

  it("names a preset the page does not offer", () => {
    expect(
      timeRangeLabel(
        { preset: "15m", from: "", to: "" },
        {
          ranges: DAILY_RANGES,
        },
      ),
    ).toBe("Last 15 minutes");
  });
});

describe("timeRangeFilter", () => {
  it("carries custom bounds as instants", () => {
    const from = local("2026-09-04T00:00");
    const to = local("2026-10-23T00:00");
    expect(timeRangeFilter({ preset: CUSTOM_RANGE, from, to })).toEqual({
      from,
      to,
    });
  });

  it("carries a preset as a relative token", () => {
    expect(timeRangeFilter({ preset: "7d", from: "", to: "" })).toEqual({
      relative: "7d",
    });
  });

  it("filters on nothing for any time", () => {
    expect(timeRangeFilter({ preset: ANY_RANGE, from: "", to: "" })).toEqual(
      {},
    );
  });
});

describe("resolveTimeRangeFilter", () => {
  it("resolves a day preset to whole local days", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 8, 15, 13, 45));
    try {
      expect(
        resolveTimeRangeFilter({ relative: "7d" }, { ranges: DAILY_RANGES }),
      ).toEqual({
        from: new Date(2026, 8, 9).toISOString(),
        to: new Date(2026, 8, 16).toISOString(),
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("resolves yesterday to a single local day", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 8, 15, 13, 45));
    try {
      expect(
        resolveTimeRangeFilter(
          { relative: "yesterday" },
          { ranges: DAILY_RANGES },
        ),
      ).toEqual({
        from: new Date(2026, 8, 14).toISOString(),
        to: new Date(2026, 8, 15).toISOString(),
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("counts calendar days across a daylight saving change", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 2, 9, 0, 30));
    try {
      expect(
        resolveTimeRangeFilter(
          { relative: "yesterday" },
          { ranges: DAILY_RANGES },
        ),
      ).toEqual({
        from: new Date(2026, 2, 8).toISOString(),
        to: new Date(2026, 2, 9).toISOString(),
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("resolves a relative instant to the past and leaves the end open", () => {
    const resolved = resolveTimeRangeFilter({ relative: "15m" });
    const elapsed = Date.now() - Date.parse(resolved.from!);
    expect(elapsed).toBeGreaterThan(14 * 60_000);
    expect(elapsed).toBeLessThan(16 * 60_000);
    expect(resolved.to).toBeUndefined();
  });
});

describe("timeRangeParams", () => {
  it("names the bounds as the API expects them", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 8, 15, 13, 45));
    try {
      expect(
        timeRangeParams({ relative: "yesterday" }, { ranges: DAILY_RANGES }),
      ).toEqual({
        start: new Date(2026, 8, 14).toISOString(),
        end: new Date(2026, 8, 15).toISOString(),
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("leaves an open bound out rather than sending it empty", () => {
    expect(timeRangeParams({ relative: "15m" })).not.toHaveProperty("end");
    expect(timeRangeParams({})).toEqual({});
  });
});

describe("toInputValue", () => {
  it("renders an instant in local time", () => {
    expect(toInputValue("2026-10-23T00:30:00.000Z")).toBe("2026-10-22T20:30");
  });

  it("renders nothing for an unparseable value", () => {
    expect(toInputValue("not-a-date")).toBe("");
  });
});

describe("isValidBound", () => {
  it("rejects a bound that is not a date", () => {
    expect(isValidBound("not-a-date")).toBe(false);
  });

  it("accepts a local wall-clock value from the input", () => {
    expect(isValidBound("2026-08-10T10:00")).toBe(true);
  });
});

describe("timeRangeFieldErrors", () => {
  it("marks both bounds when the range is inverted", () => {
    const errors = timeRangeFieldErrors({
      preset: CUSTOM_RANGE,
      from: local("2026-10-23T00:00"),
      to: local("2026-09-04T00:00"),
    });
    expect(errors.from).toMatch(/on or after/i);
    expect(errors.to).toBe(errors.from);
  });

  it("marks only the bound it cannot parse", () => {
    const errors = timeRangeFieldErrors({
      preset: CUSTOM_RANGE,
      from: "not-a-date",
      to: local("2026-09-04T00:00"),
    });
    expect(errors.from).toMatch(/valid date and time/i);
    expect(errors.to).toBeUndefined();
  });

  it("leaves an open-ended range alone unless both bounds are required", () => {
    const value = {
      preset: CUSTOM_RANGE,
      from: local("2026-09-04T00:00"),
      to: "",
    };
    expect(timeRangeFieldErrors(value)).toEqual({});
    expect(timeRangeFieldErrors(value, true).to).toMatch(
      /enter a date and time/i,
    );
  });
});

describe("day bounds", () => {
  it("shows the last day of a range, not its exclusive end", () => {
    expect(toLastDateInputValue(new Date(2026, 8, 17).toISOString())).toBe(
      "2026-09-16",
    );
  });

  it("ends a range at the local midnight after its last day", () => {
    expect(fromLastDateInputValue("2026-09-16")).toBe(
      new Date(2026, 8, 17).toISOString(),
    );
  });

  it("starts a day at its local midnight", () => {
    expect(fromFirstDateInputValue("2026-09-16")).toBe(
      new Date(2026, 8, 16).toISOString(),
    );
  });

  it("rejects what is not a date", () => {
    expect(fromFirstDateInputValue("")).toBe("");
    expect(fromLastDateInputValue("16/09/2026")).toBe("");
  });
});

describe("minute bounds", () => {
  it("ends a range at the start of the minute after its last", () => {
    expect(toNextMinute("2026-10-08T12:30")).toBe(local("2026-10-08T12:31"));
  });

  it("shows the last minute of a range, not its exclusive end", () => {
    expect(toLastInputValue(local("2026-10-08T12:31"))).toBe(
      "2026-10-08T12:30",
    );
  });

  it("is empty for no time", () => {
    expect(toNextMinute("")).toBe("");
    expect(toNextMinute("not a time")).toBe("");
    expect(toLastInputValue("")).toBe("");
  });
});

describe("timeRangeFieldErrors order", () => {
  it("rejects an empty range", () => {
    const at = local("2026-10-08T12:30");
    expect(
      timeRangeFieldErrors({ preset: CUSTOM_RANGE, from: at, to: at }).to,
    ).toMatch(/on or after/i);
  });
});
