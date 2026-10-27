import { describe, expect, it } from "vitest";
import {
  expandID,
  expandIMPU,
  isMCPTTIdentity,
  formatDuration,
  formatElapsed,
  identityKey,
  sameIdentity,
  shortID,
} from "@/utils/mcptt";

const domain = "mcptt.example.com";
const home = "ims.mnc001.mcc001.3gppnetwork.org";

describe("mcptt identities", () => {
  it("expands an MCPTT or group ID a name, user@host or URI", () => {
    expect(expandID("alice", domain)).toBe("sip:alice@mcptt.example.com");
    expect(expandID(" bob@other.org ", domain)).toBe("sip:bob@other.org");
    expect(expandID("sips:c@x.org", domain)).toBe("sips:c@x.org");
    expect(expandID("", domain)).toBeUndefined();
    expect(expandID("two words", domain)).toBeUndefined();
  });

  it("expands an IMPU of a number to its MCPTT identity, as Ella Core's HSS has it", () => {
    expect(expandIMPU("15550000001", home)).toBe(
      `sip:mcptt-15550000001@${home}`,
    );
    expect(expandIMPU("+15550000001", home)).toBe(
      `sip:mcptt-15550000001@${home}`,
    );
    expect(expandIMPU("alice", home)).toBe(
      "sip:alice@ims.mnc001.mcc001.3gppnetwork.org",
    );
    expect(expandIMPU("sip:15550000001@x.org", home)).toBe(
      "sip:15550000001@x.org",
    );
    expect(expandIMPU("not valid", home)).toBeUndefined();
  });

  it("compares identities as the server: host case-insensitive, user part as is", () => {
    expect(
      sameIdentity(
        "sip:alice@MCPTT.example.com",
        "sip:alice@mcptt.example.com",
      ),
    ).toBe(true);
    expect(
      sameIdentity(
        "sip:ALICE@mcptt.example.com",
        "sip:alice@mcptt.example.com",
      ),
    ).toBe(false);
    expect(identityKey("sips:a@B.org")).toBe("sip:a@b.org");
  });

  it("shortens an identity of the domain to its user part", () => {
    expect(shortID("sip:fire@mcptt.example.com", domain)).toBe("fire");
    expect(shortID("sip:fire@other.org", domain)).toBe("fire@other.org");
  });
});

describe("mcptt durations", () => {
  it("formats durations and elapsed times", () => {
    expect(formatDuration(600000)).toBe("10 min");
    expect(formatDuration(7200000)).toBe("2 h");
    expect(formatDuration(30000)).toBe("30 s");
    expect(formatDuration(1500)).toBe("1500 ms");
    expect(formatDuration(0)).toBe("0 s");
    const at = Date.parse("2026-10-25T12:00:00Z");
    expect(formatElapsed("2026-10-25T12:00:00Z", at + 65_000)).toBe("1:05");
    expect(formatElapsed("2026-10-25T12:00:00Z", at + 3_725_000)).toBe(
      "1:02:05",
    );
  });
});

describe("isMCPTTIdentity", () => {
  it("tells the MCPTT identity from the voice and SMS identities", () => {
    expect(isMCPTTIdentity("sip:mcptt-15550000001@ims.example.org")).toBe(true);
    expect(isMCPTTIdentity("sip:+15550000001@ims.example.org")).toBe(false);
    expect(isMCPTTIdentity("tel:+15550000001")).toBe(false);
  });
});
