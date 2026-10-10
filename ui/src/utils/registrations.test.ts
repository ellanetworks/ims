import { describe, expect, it } from "vitest";
import { contact, registration } from "@/test/fixtures";
import {
  contactsOf,
  devicesOf,
  identitiesOf,
  imeiOf,
  numberOf,
  numbersOf,
  othersOf,
  signallingPathOf,
  subscriberOf,
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

describe("devicesOf", () => {
  it("groups the contacts by instance ID, a contact without one apart", () => {
    const phone = contact();
    const flow = contact({ contact: "sip:other@192.0.2.30:5064", reg_id: 2 });
    const bare = contact({
      contact: "sip:soft@192.0.2.50:5060",
      instance: undefined,
    });
    const uuid = "urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6";
    const tablet = contact({ instance: uuid });

    const devices = devicesOf([phone, bare, flow, tablet]);

    expect(devices.map((d) => [d.label, d.contacts])).toEqual([
      ["35693803-564380-0", [phone, flow]],
      ["sip:soft@192.0.2.50:5060", [bare]],
      [uuid, [tablet]],
    ]);
  });
});

describe("subscriberOf", () => {
  it("is the IMSI of an IMPI derived from one", () => {
    expect(
      subscriberOf("001010000000001@ims.mnc001.mcc001.3gppnetwork.org"),
    ).toBe("001010000000001");
  });

  it("is the IMPI of an ISIM", () => {
    expect(subscriberOf("alice@ims.example.org")).toBe("alice@ims.example.org");
  });
});

describe("othersOf", () => {
  it("is the other IMPIs on the registration's numbers, not on its barred IMPU", () => {
    expect(
      othersOf(
        registration({
          identities: [
            {
              uri: "sip:001010000000001@ims",
              barred: true,
              registered_with: ["x"],
            },
            {
              uri: "tel:+15551230001",
              barred: false,
              registered_with: ["bob"],
            },
            {
              uri: "sip:+15551230001@ims;user=phone",
              barred: false,
              registered_with: ["bob", "carol"],
            },
          ],
        }),
      ),
    ).toEqual(["bob", "carol"]);
  });
});

describe("identitiesOf and contactsOf", () => {
  const tel = { uri: "tel:+15551230001", barred: false, registered_with: [] };
  const sip = {
    uri: "sip:alice@ims.mnc001.mcc001.3gppnetwork.org",
    barred: false,
    registered_with: [],
  };
  const phone = contact();
  const tablet = contact({ contact: "sip:tablet@192.0.2.31:5060" });
  const twoSets = registration({
    implicit_registration_sets: [
      { identities: [tel], contacts: [phone] },
      {
        identities: [tel, sip],
        contacts: [contact({ expires_at: "2026-10-08T14:00:00.000Z" }), tablet],
      },
    ],
  });

  it("lists the identities of all sets once", () => {
    expect(identitiesOf(twoSets)).toEqual([tel, sip]);
  });

  it("lists a contact bound to several sets once, with its latest expiry", () => {
    expect(contactsOf(twoSets)).toEqual([
      contact({ expires_at: "2026-10-08T14:00:00.000Z" }),
      tablet,
    ]);
  });
});
