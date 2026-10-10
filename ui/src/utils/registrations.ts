import type {
  RegisteredContact,
  RegisteredIdentity,
  Registration,
  SignallingPath,
} from "@/queries/registrations";

const IMEI_URN = "urn:gsma:imei:";

// imeiOf is the IMEI in the instance ID of the device that registered a contact (TS 23.003 §13.8), if it has one.
export const imeiOf = (contact: RegisteredContact): string | undefined =>
  contact.instance?.toLowerCase().startsWith(IMEI_URN)
    ? contact.instance.slice(IMEI_URN.length)
    : undefined;

export interface Device {
  id: string;
  // label is how the device shows: its IMEI, else its instance ID, else its contact.
  label: string;
  contacts: RegisteredContact[];
}

// identitiesOf is the public identities of all the implicit registration sets of a registration, each once.
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

// contactsOf is the contacts of all the implicit registration sets of a registration. A contact bound to several sets
// is listed once, with its latest expiry.
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

// devicesOf groups contacts by the device that registered them, its instance ID (RFC 5626 §4.1); a contact without
// one is a device of its own.
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

// numberOfURI is the E.164 number of a tel URI or of a SIP URI with user=phone (TS 23.003 §13.4).
export const numberOfURI = (uri: string): string | undefined => {
  const tel = /^tel:(\+\d+)/i.exec(uri);
  if (tel) return tel[1];

  const sip = /^sips?:(\+\d+)[@;].*;user=phone/i.exec(uri);
  return sip?.[1];
};

export const numberOf = (identity: RegisteredIdentity): string | undefined =>
  numberOfURI(identity.uri);

// An IMPI derived from the IMSI (TS 23.003 §13.3).
const IMSI_IMPI = /^(\d{6,15})@ims\.mnc\d{3}\.mcc\d{3}\.3gppnetwork\.org$/i;

// subscriberOf is how a registration shows: the IMSI of an IMPI derived from one, else the IMPI.
export const subscriberOf = (impi: string): string =>
  IMSI_IMPI.exec(impi)?.[1] ?? impi;

// othersOf is the other private identities registered with a registration's numbers: other SIMs on the same
// number, such as a watch's (TS 24.229 §5.4.3.3), which calls to it reach too.
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

// signallingPathOf is the worst signalling path of the contacts of a registration.
export const signallingPathOf = (
  registration: Registration,
): SignallingPath => {
  const paths = contactsOf(registration).map((c) => c.signalling_path);
  if (paths.includes("lost")) return "lost";
  if (paths.includes("monitored")) return "monitored";
  return "unmonitored";
};
