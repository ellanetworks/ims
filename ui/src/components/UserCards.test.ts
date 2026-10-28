import { describe, expect, it } from "vitest";
import { endOfDay, localDate } from "@/components/UserCards";

describe("API token expiry dates", () => {
  it("reads the date of the browser's time zone, not UTC's", () => {
    // 23:30 local time is the next day in UTC west of Greenwich, and 00:30 local the previous day east of it.
    expect(localDate(new Date(2026, 9, 27, 23, 30))).toBe("2026-10-27");
    expect(localDate(new Date(2026, 9, 27, 0, 30))).toBe("2026-10-27");
  });

  it("makes a token work through its expiry date", () => {
    expect(endOfDay("2026-10-27")).toEqual(new Date(2026, 9, 28));
    expect(endOfDay("2026-12-31")).toEqual(new Date(2027, 0, 1));
  });
});
