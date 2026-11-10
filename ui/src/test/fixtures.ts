import type { CallRecord } from "@/queries/callRecords";
import type { ClusterMember, ClusterStatus } from "@/queries/cluster";
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
  domain: "",
  numbering: {
    country_code: "1",
    national_prefix: "",
    international_prefix: "",
    min_national_digits: 7,
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

export const page = <T>(
  items: T[],
  extra: { page?: number; per_page?: number; total_count?: number } = {},
) => ({
  items,
  page: extra.page ?? 1,
  per_page: extra.per_page ?? 25,
  total_count: extra.total_count ?? items.length,
});

export const clusterMember = (
  overrides: Partial<ClusterMember> = {},
): ClusterMember => ({
  node_id: "0b5c6a0e-1111-4c1e-9a55-1f0c2d3e4f50",
  address: "10.0.0.11:7000",
  api_address: "https://10.0.0.11:5020",
  diameter_label: "ims-1f0c2d3e4f50",
  diameter_host: "ims-1f0c2d3e4f50.ims.mnc001.mcc001.3gppnetwork.org",
  label_generated: true,
  display_name: "",
  version: "v0.1.0",
  joined_at: "2026-09-30T12:00:00.000Z",
  drain_state: "active",
  suffrage: "voter",
  leader: false,
  healthy: true,
  last_contact: "12ms",
  ...overrides,
});

export const clusterStatus = (
  overrides: Partial<ClusterStatus> = {},
): ClusterStatus => ({
  node_id: "0b5c6a0e-1111-4c1e-9a55-1f0c2d3e4f50",
  cluster_id: "9f1e2d3c-2222-4b5a-8c7d-6e5f4a3b2c1d",
  clustered: true,
  leader_id: "0b5c6a0e-1111-4c1e-9a55-1f0c2d3e4f50",
  term: 3,
  applied_index: 42,
  commit_index: 42,
  schema: 1,
  healthy: true,
  failure_tolerance: 1,
  members: [
    clusterMember({ leader: true, last_contact: "0s" }),
    clusterMember({
      node_id: "1c6d7b1f-3333-4d2f-8b66-2a1d3e4f5a61",
      address: "10.0.0.12:7000",
      api_address: "https://10.0.0.12:5020",
      diameter_label: "ims-b",
      diameter_host: "ims-b.ims.mnc001.mcc001.3gppnetwork.org",
      label_generated: false,
      display_name: "paris-2",
      last_contact: "35ms",
    }),
    clusterMember({
      node_id: "2d7e8c2a-4444-4e3a-9c77-3b2e4f5a6b72",
      address: "10.0.0.13:7000",
      api_address: "",
      diameter_label: "ims-3b2e4f5a6b72",
      diameter_host: "ims-3b2e4f5a6b72.ims.mnc001.mcc001.3gppnetwork.org",
      label_generated: true,
      suffrage: "nonvoter",
      drain_state: "drained",
      healthy: false,
      last_contact: "9.5s",
    }),
  ],
  ...overrides,
});
