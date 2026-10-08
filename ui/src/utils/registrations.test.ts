import { describe, expect, it } from "vitest";
import { contact, registration } from "@/test/fixtures";
import {
  imeiOf,
  lastExpiry,
  numberOf,
  numbersOf,
  priorityOf,
  signallingPathOf,
} from "@/utils/registrations";

describe("imeiOf", () => {
  it("reads the IMEI of an IMEI URN", () => {
    expect(
      imeiOf(contact({ instance: "urn:gsma:imei:35693803-564380-0" })),
    ).toBe("35693803-564380-0");
  });

  it("has none for a UUID URN or no instance", () => {
    expect(
      imeiOf(
        contact({ instance: "urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6" }),
      ),
    ).toBeUndefined();
    expect(imeiOf(contact({ instance: undefined }))).toBeUndefined();
  });
});

describe("priorityOf", () => {
  it("shows the q-value with one decimal at least", () => {
    expect(priorityOf(contact({ q: 1 }))).toBe("1.0");
    expect(priorityOf(contact({ q: 0.5 }))).toBe("0.5");
    expect(priorityOf(contact({ q: 0.25 }))).toBe("0.25");
    expect(priorityOf(contact({ q: 0 }))).toBe("0.0");
  });
});

describe("numberOf", () => {
  it("reads tel URIs and SIP URIs with user=phone", () => {
    expect(
      numberOf({ uri: "tel:+15551230001", barred: false, registered_with: [] }),
    ).toBe("+15551230001");
    expect(
      numberOf({
        uri: "sip:+15551230001@ims.mnc001.mcc001.3gppnetwork.org;user=phone",
        barred: false,
        registered_with: [],
      }),
    ).toBe("+15551230001");
  });

  it("has none for other identities", () => {
    expect(
      numberOf({
        uri: "sip:alice@ims.example.org",
        barred: false,
        registered_with: [],
      }),
    ).toBeUndefined();
    expect(
      numberOf({
        uri: "sip:+15551230001@ims.example.org",
        barred: false,
        registered_with: [],
      }),
    ).toBeUndefined();
  });
});

describe("numbersOf", () => {
  it("lists each unbarred number once", () => {
    expect(
      numbersOf(
        registration({
          identities: [
            {
              uri: "sip:001010000000001@ims.example.org",
              barred: true,
              registered_with: [],
            },
            {
              uri: "sip:+15551230001@ims.example.org;user=phone",
              barred: false,
              registered_with: [],
            },
            { uri: "tel:+15551230001", barred: false, registered_with: [] },
            { uri: "tel:+15551230009", barred: true, registered_with: [] },
          ],
        }),
      ),
    ).toEqual(["+15551230001"]);
  });
});

describe("lastExpiry", () => {
  it("is the latest expiry of the devices", () => {
    expect(
      lastExpiry(
        registration({
          contacts: [
            contact({ expires_at: "2026-10-08T13:00:00.000Z" }),
            contact({ expires_at: "2026-10-08T14:00:00.000Z" }),
          ],
        }),
      ),
    ).toBe("2026-10-08T14:00:00.000Z");
  });
});

describe("signallingPathOf", () => {
  it("is the worst of the devices", () => {
    const of = (...paths: ("unmonitored" | "monitored" | "lost")[]) =>
      signallingPathOf(
        registration({
          contacts: paths.map((p) => contact({ signalling_path: p })),
        }),
      );

    expect(of("monitored", "lost")).toBe("lost");
    expect(of("unmonitored", "monitored")).toBe("monitored");
    expect(of("unmonitored")).toBe("unmonitored");
  });
});
