import { describe, expect, it } from "vitest";
import { callRecord } from "@/test/fixtures";
import {
  calleeOf,
  callerOf,
  dialledOf,
  formatDuration,
} from "@/utils/callRecords";

describe("formatDuration", () => {
  it("shows minutes and seconds, then hours", () => {
    expect(formatDuration(0)).toBe("0:00");
    expect(formatDuration(65_400)).toBe("1:05");
    expect(formatDuration(3_723_000)).toBe("1:02:03");
  });
});

describe("callerOf", () => {
  it("is the caller's number", () => {
    expect(callerOf(callRecord())).toBe("+15551230001");
  });

  it("falls back to the asserted identity, then the private identity", () => {
    expect(
      callerOf(callRecord({ calling_party: ["sip:alice@example.org"] })),
    ).toBe("sip:alice@example.org");
    expect(callerOf(callRecord({ calling_party: [] }))).toBe(
      "001010000000001@ims.mnc001.mcc001.3gppnetwork.org",
    );
  });
});

describe("calleeOf", () => {
  it("is the number the call was routed to, else the one dialled", () => {
    expect(calleeOf(callRecord())).toBe("+15551230002");
    expect(
      calleeOf(
        callRecord({ called_party: undefined, requested_party: "tel:999" }),
      ),
    ).toBe("tel:999");
  });
});

describe("dialledOf", () => {
  it("is the number, the digits of a local number, else the URI", () => {
    expect(dialledOf("tel:+15551230002")).toBe("+15551230002");
    expect(
      dialledOf(
        "tel:5551230002;phone-context=ims.mnc001.mcc001.3gppnetwork.org",
      ),
    ).toBe("5551230002");
    expect(dialledOf("sip:alice@example.org")).toBe("sip:alice@example.org");
  });
});
