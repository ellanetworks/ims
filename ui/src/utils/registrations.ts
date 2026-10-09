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

// devicesOf groups the contacts of a registration by the device that registered them, its instance ID (RFC 5626
// §4.1); a contact without one is a device of its own.
export const devicesOf = (registration: Registration): Device[] => {
  const devices: Device[] = [];

  for (const c of registration.contacts) {
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

// deviceSummary is a device's label, with its number of registration flows when it has more than one.
export const deviceSummary = (device: Device): string => {
  const flows = device.contacts.filter((c) => c.reg_id !== undefined).length;
  return flows > 1 ? `${device.label} · ${flows} flows` : device.label;
};

// priorityOf is a contact's q-value, with one decimal at least: "1.0", "0.5", "0.25".
export const priorityOf = (contact: RegisteredContact): string =>
  Number.isInteger(contact.q * 10) ? contact.q.toFixed(1) : String(contact.q);

// numberOfURI is the E.164 number of a tel URI or of a SIP URI with user=phone (TS 23.003 §13.4).
export const numberOfURI = (uri: string): string | undefined => {
  const tel = /^tel:(\+\d+)/i.exec(uri);
  if (tel) return tel[1];

  const sip = /^sips?:(\+\d+)[@;].*;user=phone/i.exec(uri);
  return sip?.[1];
};

export const numberOf = (identity: RegisteredIdentity): string | undefined =>
  numberOfURI(identity.uri);

export const numbersOf = (registration: Registration): string[] => [
  ...new Set(
    registration.identities
      .filter((identity) => !identity.barred)
      .map(numberOf)
      .filter((n): n is string => n !== undefined),
  ),
];

export const lastExpiry = (registration: Registration): string | undefined =>
  registration.contacts
    .map((c) => c.expires_at)
    .sort()
    .at(-1);

// signallingPathOf is the worst signalling path of the contacts of a registration.
export const signallingPathOf = (
  registration: Registration,
): SignallingPath => {
  const paths = registration.contacts.map((c) => c.signalling_path);
  if (paths.includes("lost")) return "lost";
  if (paths.includes("monitored")) return "monitored";
  return "unmonitored";
};
