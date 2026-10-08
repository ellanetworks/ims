import { describe, expect, it } from "vitest";
import { formatEndpoint, hostOf, portOf } from "@/utils/addresses";

describe("hostOf", () => {
  it("strips the port of an IPv4 address", () => {
    expect(hostOf("192.0.2.1:5060")).toBe("192.0.2.1");
  });

  it("strips the brackets and port of an IPv6 address", () => {
    expect(hostOf("[2001:db8::1]:5060")).toBe("2001:db8::1");
  });
});

describe("formatEndpoint", () => {
  it("joins an IPv4 address and a port", () => {
    expect(formatEndpoint("192.0.2.1", 3868)).toBe("192.0.2.1:3868");
  });

  it("brackets an IPv6 address", () => {
    expect(formatEndpoint("2001:db8::1", 3868)).toBe("[2001:db8::1]:3868");
  });
});

describe("portOf", () => {
  it("reads the port of an IPv4 or IPv6 address", () => {
    expect(portOf("192.0.2.1:5060")).toBe(5060);
    expect(portOf("[2001:db8::1]:5070")).toBe(5070);
  });
});
