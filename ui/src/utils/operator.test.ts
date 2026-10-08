import { describe, expect, it } from "vitest";
import { homeDomain } from "@/utils/operator";

describe("homeDomain", () => {
  it("pads a 2-digit MNC", () => {
    expect(homeDomain("001", "01")).toBe("ims.mnc001.mcc001.3gppnetwork.org");
  });

  it("keeps a 3-digit MNC", () => {
    expect(homeDomain("310", "410")).toBe("ims.mnc410.mcc310.3gppnetwork.org");
  });
});
