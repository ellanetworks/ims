import type {
  RegisteredContact,
  RegisteredIdentity,
  Registration,
  SignallingPath,
} from "@/queries/registrations";

const IMEI_URN = "urn:gsma:imei:";

// TS 23.003 clause 13.8
export const imeiOf = (contact: RegisteredContact): string | undefined =>
  contact.instance?.toLowerCase().startsWith(IMEI_URN)
    ? contact.instance.slice(IMEI_URN.length)
    : undefined;

export interface Device {
  id: string;
  label: string;
  contacts: RegisteredContact[];
}

export const identitiesOf = (
  registration: Registration,
): RegisteredIdentity[] => {
  const identities: RegisteredIdentity[] = [];

  for (const set of registration.implicit_registration_sets) {
    for (const identity of set.identities) {
      if (!identities.some((i) => i.uri === identity.uri)) {
        identities.push(identity);
      }
    }
  }

  return identities;
};

export const contactsOf = (registration: Registration): RegisteredContact[] => {
  const contacts: RegisteredContact[] = [];

  for (const set of registration.implicit_registration_sets) {
    const earlier = contacts.length;

    for (const contact of set.contacts) {
      const i = contacts
        .slice(0, earlier)
        .findIndex(
          (c) => c.contact === contact.contact && c.reg_id === contact.reg_id,
        );

      if (i < 0) {
        contacts.push(contact);
      } else if (contact.expires_at > contacts[i].expires_at) {
        contacts[i] = contact;
      }
    }
  }

  return contacts;
};

// RFC 5626 section 4.1
export const devicesOf = (contacts: RegisteredContact[]): Device[] => {
  const devices: Device[] = [];

  for (const c of contacts) {
    const id = c.instance ?? c.contact;
    let device = devices.find((d) => d.id === id);

    if (!device) {
      device = {
        id,
        label: imeiOf(c) ?? c.instance ?? c.contact,
        contacts: [],
      };
      devices.push(device);
    }

    device.contacts.push(c);
  }

  return devices;
};

// TS 23.003 clause 13.4
export const numberOfURI = (uri: string): string | undefined => {
  const tel = /^tel:(\+\d+)/i.exec(uri);
  if (tel) return tel[1];

  const sip = /^sips?:(\+\d+)[@;].*;user=phone/i.exec(uri);
  return sip?.[1];
};

export const numberOf = (identity: RegisteredIdentity): string | undefined =>
  numberOfURI(identity.uri);

// TS 23.003 clause 13.3
const IMSI_IMPI = /^(\d{6,15})@ims\.mnc\d{3}\.mcc\d{3}\.3gppnetwork\.org$/i;

export const subscriberOf = (impi: string): string =>
  IMSI_IMPI.exec(impi)?.[1] ?? impi;

export const othersOf = (registration: Registration): string[] => [
  ...new Set(
    identitiesOf(registration)
      .filter((identity) => !identity.barred)
      .flatMap((identity) => identity.registered_with),
  ),
];

export const numbersOf = (registration: Registration): string[] => [
  ...new Set(
    identitiesOf(registration)
      .filter((identity) => !identity.barred)
      .map(numberOf)
      .filter((n): n is string => n !== undefined),
  ),
];

export const signallingPathOf = (
  registration: Registration,
): SignallingPath => {
  const paths = contactsOf(registration).map((c) => c.signalling_path);
  if (paths.includes("lost")) return "lost";
  if (paths.includes("monitored")) return "monitored";
  return "unmonitored";
};
