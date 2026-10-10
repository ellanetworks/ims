import type { CallRecord } from "@/queries/callRecords";
import type { DiameterPeer, DiameterRoute } from "@/queries/diameter";
import type { Operator } from "@/queries/operator";
import type { PolicyWithStatus } from "@/queries/policy";
import type {
  ImplicitRegistrationSet,
  RegisteredContact,
  Registration,
} from "@/queries/registrations";
import type { SIPStatus } from "@/queries/sip";

export const operator: Operator = {
  mcc: "001",
  mnc: "01",
  numbering: {
    country_code: "1",
    national_prefix: "",
    international_prefix: "",
  },
};

export const identity = {
  host: "ims.ims.mnc001.mcc001.3gppnetwork.org",
  realm: "ims.mnc001.mcc001.3gppnetwork.org",
};

export const sip: SIPStatus = {
  home_domain: "ims.mnc001.mcc001.3gppnetwork.org",
  aliases: [],
  listeners: [
    { role: "pcscf", address: "192.0.2.20:5060", transports: ["udp", "tcp"] },
    { role: "pcscf", address: "[2001:db8::20]:5060", transports: ["udp"] },
    { role: "pcscf-protected", address: "192.0.2.20:5063", transports: [] },
    { role: "icscf", address: "192.0.2.20:5070", transports: ["udp"] },
  ],
};

export const peer = (overrides: Partial<DiameterPeer> = {}): DiameterPeer => ({
  id: "0199a1b2-0000-7000-8000-000000000001",
  host: "core.epc.mnc001.mcc001.3gppnetwork.org",
  address: "192.0.2.1",
  port: 3868,
  transport: "sctp",
  applications: ["cx", "rx"],
  priority: 10,
  status: {
    state: "open",
    since: "2026-10-08T12:00:00.000Z",
    realm: "epc.mnc001.mcc001.3gppnetwork.org",
  },
  ...overrides,
});

export const route = (
  overrides: Partial<DiameterRoute> = {},
): DiameterRoute => ({
  application: "cx",
  realm: "",
  destination_realm: "ims.mnc001.mcc001.3gppnetwork.org",
  peers: [
    {
      id: "0199a1b2-0000-7000-8000-000000000001",
      host: "core.epc.mnc001.mcc001.3gppnetwork.org",
      priority: 10,
      status: { state: "open" },
    },
  ],
  ...overrides,
});

export const policy = (
  overrides: Partial<PolicyWithStatus> = {},
): PolicyWithStatus => ({
  interface: "rx",
  status: {
    interface: "rx",
    endpoint: "core.epc.mnc001.mcc001.3gppnetwork.org",
  },
  ...overrides,
});

export const contact = (
  overrides: Partial<RegisteredContact> = {},
): RegisteredContact => ({
  contact: "sip:001010000000001@192.0.2.30:5064",
  instance: "urn:gsma:imei:35693803-564380-0",
  q: 1,
  media: ["audio", "video"],
  registered_at: "2026-10-08T12:00:00.000Z",
  expires_at: "2026-10-08T13:00:00.000Z",
  address: "192.0.2.30:5064",
  transport: "udp",
  protected: true,
  signalling_path: "monitored",
  ...overrides,
});

// registration is a registration with one implicit registration set, built from identities, contacts and hss,
// unless implicit_registration_sets gives them all.
export const registration = ({
  impi = "001010000000001@ims.mnc001.mcc001.3gppnetwork.org",
  identities = [
    {
      uri: "sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org",
      barred: true,
      registered_with: [],
    },
    {
      uri: "sip:+15551230001@ims.mnc001.mcc001.3gppnetwork.org;user=phone",
      barred: false,
      registered_with: [],
    },
    { uri: "tel:+15551230001", barred: false, registered_with: [] },
  ],
  contacts = [contact()],
  hss = {
    host: "mmec01.mmegi0001.mme.epc.mnc001.mcc001.3gppnetwork.org",
    realm: "epc.mnc001.mcc001.3gppnetwork.org",
  },
  implicit_registration_sets,
}: Partial<Registration> &
  Partial<ImplicitRegistrationSet> = {}): Registration => ({
  impi,
  implicit_registration_sets: implicit_registration_sets ?? [
    { hss, identities, contacts },
  ],
});

export const callRecord = (
  overrides: Partial<CallRecord> = {},
): CallRecord => ({
  id: 1,
  icid: "4F2A9C0E1B7D4E3A8C6F0D2B5A1E9C7F",
  session_id: "a84b4c76e66710@192.0.2.30",
  calling_party: [
    "sip:+15551230001@ims.mnc001.mcc001.3gppnetwork.org;user=phone",
    "tel:+15551230001",
  ],
  caller_impi: "001010000000001@ims.mnc001.mcc001.3gppnetwork.org",
  requested_party:
    "tel:5551230002;phone-context=ims.mnc001.mcc001.3gppnetwork.org",
  called_party: "tel:+15551230002",
  callee_impi: "001010000000002@ims.mnc001.mcc001.3gppnetwork.org",
  requested_at: "2026-10-08T12:00:00.000Z",
  delivery_start_at: "2026-10-08T12:00:04.000Z",
  delivery_end_at: "2026-10-08T12:01:09.000Z",
  sip_status: 200,
  outcome: "answered",
  ended_by: "caller",
  alerted: true,
  media: ["audio", "video"],
  in_progress: false,
  incomplete: false,
  duration_ms: 65000,
  ...overrides,
});
