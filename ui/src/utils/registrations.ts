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

// priorityOf is a contact's q-value, with one decimal at least: "1.0", "0.5", "0.25".
export const priorityOf = (contact: RegisteredContact): string =>
  Number.isInteger(contact.q * 10) ? contact.q.toFixed(1) : String(contact.q);

// numberOf is the E.164 number of a tel URI or of a SIP URI with user=phone (TS 23.003 §13.4).
export const numberOf = (identity: RegisteredIdentity): string | undefined => {
  const tel = /^tel:(\+\d+)/i.exec(identity.uri);
  if (tel) return tel[1];

  const sip = /^sips?:(\+\d+)[@;].*;user=phone/i.exec(identity.uri);
  return sip?.[1];
};

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
