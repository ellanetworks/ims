import type {
  RegisteredDevice,
  RegisteredIdentity,
  Registration,
  SignallingPath,
} from "@/queries/registrations";

const IMEI_URN = "urn:gsma:imei:";

// imeiOf is the IMEI in a device's instance ID (TS 23.003 §13.8), if it has one.
export const imeiOf = (device: RegisteredDevice): string | undefined =>
  device.instance?.toLowerCase().startsWith(IMEI_URN)
    ? device.instance.slice(IMEI_URN.length)
    : undefined;

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
  registration.devices
    .map((d) => d.expires_at)
    .sort()
    .at(-1);

// signallingPathOf is the worst signalling path of the devices of a registration.
export const signallingPathOf = (
  registration: Registration,
): SignallingPath => {
  const paths = registration.devices.map((d) => d.signalling_path);
  if (paths.includes("lost")) return "lost";
  if (paths.includes("monitored")) return "monitored";
  return "unmonitored";
};
