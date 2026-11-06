import { describe, expect, it } from "vitest";
import { isHostname, trunkDefaults } from "@/utils/sipTrunks";

describe("isHostname", () => {
  it.each([
    ["sip.example.com", true],
    ["a-b.example", true],
    ["example", true],
    ["192.0.2.1", false],
    ["2001:db8::1", false],
    ["", false],
    ["-a.example", false],
    ["a..example", false],
    ["example.123", false],
  ])("%s is %s", (host, want) => {
    expect(isHostname(host)).toBe(want);
  });
});

describe("trunkDefaults", () => {
  it("takes the domain from the first domain name", () => {
    expect(
      trunkDefaults(["192.0.2.1", "a.example", "b.example"], "", false).domain,
    ).toBe("a.example");
  });

  it("has no domain without a domain name", () => {
    expect(trunkDefaults(["192.0.2.1"], "", false).domain).toBe("");
  });

  it("covers each IP address once", () => {
    expect(
      trunkDefaults(
        ["192.0.2.1", "a.example", "2001:db8::1", "192.0.2.1"],
        "",
        false,
      ).inboundCidrs,
    ).toEqual(["192.0.2.1/32", "2001:db8::1/128"]);
  });

  it("has no identities without trust anchors", () => {
    expect(trunkDefaults(["a.example"], "", false).identities).toEqual([]);
  });

  it("lists the domain then each domain name once", () => {
    expect(
      trunkDefaults(
        ["a.example", "B.example", "b.EXAMPLE", "192.0.2.1"],
        "b.example",
        true,
      ).identities,
    ).toEqual(["b.example", "a.example"]);
  });

  it("leaves an IP domain out of the identities", () => {
    expect(trunkDefaults(["a.example"], "192.0.2.1", true).identities).toEqual([
      "a.example",
    ]);
  });
});
