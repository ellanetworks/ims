import { describe, expect, it } from "vitest";
import { formatTimestamp } from "@/utils/dates";

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
