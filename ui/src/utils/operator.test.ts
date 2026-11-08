import { describe, expect, it } from "vitest";
import { derivedDomain, domainError, homeDomain } from "@/utils/operator";

describe("derivedDomain", () => {
  it("pads a 2-digit MNC", () => {
    expect(derivedDomain("001", "01")).toBe(
      "ims.mnc001.mcc001.3gppnetwork.org",
    );
  });

  it("keeps a 3-digit MNC", () => {
    expect(derivedDomain("310", "410")).toBe(
      "ims.mnc410.mcc310.3gppnetwork.org",
    );
  });
});

describe("homeDomain", () => {
  it("derives the domain when none is set", () => {
    expect(homeDomain({ mcc: "001", mnc: "01", domain: "" })).toBe(
      "ims.mnc001.mcc001.3gppnetwork.org",
    );
  });

  it("uses the domain set", () => {
    expect(
      homeDomain({ mcc: "001", mnc: "01", domain: "ims.example.com" }),
    ).toBe("ims.example.com");
  });
});

describe("domainError", () => {
  it.each([
    "",
    "ims.example.com",
    "ims-1.example.com",
    "a.b.c.example.co",
    "ims.mnc001.mcc001.3gppnetwork.org",
  ])("accepts %s", (domain) => {
    expect(
      domainError(domain, "ims.mnc001.mcc001.3gppnetwork.org"),
    ).toBeUndefined();
  });

  it.each([
    "localhost",
    "ims..example.com",
    "-ims.example.com",
    "ims_1.example.com",
    "IMS.example.com",
    "192.0.2.1",
    `${"a".repeat(64)}.com`,
    `${"a.".repeat(127)}com`,
    "3gppnetwork.org",
    "mcc001.3gppnetwork.org",
    "ims.mnc002.mcc001.3gppnetwork.org",
  ])("rejects %s", (domain) => {
    expect(
      domainError(domain, "ims.mnc001.mcc001.3gppnetwork.org"),
    ).toBeDefined();
  });
});
