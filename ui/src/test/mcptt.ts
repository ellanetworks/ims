import type {
  Affiliation,
  ClientRegistration,
  Group,
  GroupCall,
  MCPTTUser,
  ServiceConfiguration,
} from "@/queries/mcptt";
import type { MCPTTStatus } from "@/queries/status";
import { json } from "@/test/render";

export const mcpttStatus: MCPTTStatus = {
  mode: "ims",
  name: "sip:mcptt.ims.mnc001.mcc001.3gppnetwork.org",
  participating_psi:
    "sip:mcptt-participating@mcptt.ims.mnc001.mcc001.3gppnetwork.org",
  controlling_psi:
    "sip:mcptt-controlling@mcptt.ims.mnc001.mcc001.3gppnetwork.org",
  sip: ["192.0.2.20:5090"],
  media_control: "192.0.2.20:21000",
};

export const serviceConfiguration: ServiceConfiguration = {
  domain: "mcptt.example.com",
};

export const alice: MCPTTUser = {
  mcptt_id: "sip:alice@mcptt.example.com",
  alias: "Alice",
  impus: ["sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org"],
  implicit_affiliations: ["sip:fire@mcptt.example.com"],
};

export const bob: MCPTTUser = {
  mcptt_id: "sip:bob@mcptt.example.com",
  alias: "",
  impus: ["sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org"],
  implicit_affiliations: [],
};

const member = (mcptt_id: string, implicit: boolean, recvonly = false) => ({
  mcptt_id,
  recvonly,
  implicit_affiliation: implicit,
});

export const fire: Group = {
  group_id: "sip:fire@mcptt.example.com",
  display_name: "Fire",
  maximum_duration_ms: 600000,
  release_on_initiator_leave: false,
  members: [member(alice.mcptt_id, true), member(bob.mcptt_id, false)],
};

export const registrations: ClientRegistration[] = [
  {
    mcptt_id: alice.mcptt_id,
    client_id: "urn:uuid:a1",
    impu: alice.impus[0],
    impus: alice.impus,
    since: "2026-10-25T12:00:00.000Z",
    expires_at: "2026-10-25T13:00:00.000Z",
    contacts: [],
  },
];

export const affiliations: Affiliation[] = [
  {
    mcptt_id: alice.mcptt_id,
    client_id: "urn:uuid:a1",
    group_id: fire.group_id,
    status: "affiliated",
    configured_implicit: true,
  },
];

export const call: GroupCall = {
  id: "abc",
  session_id:
    "sip:mcptt-controlling@mcptt.ims.mnc001.mcc001.3gppnetwork.org;gr=abc",
  group_id: fire.group_id,
  originator: alice.mcptt_id,
  created_at: "2026-10-25T12:00:00.000Z",
  started_at: "2026-10-25T12:00:01.000Z",
  ends_at: "2026-10-25T12:10:00.000Z",
  talker: { mcptt_id: alice.mcptt_id, since: "2026-10-25T12:00:02.000Z" },
  participants: [
    {
      mcptt_id: alice.mcptt_id,
      state: "joined",
      required: false,
      recvonly: false,
    },
    {
      mcptt_id: bob.mcptt_id,
      state: "invited",
      required: false,
      recvonly: false,
    },
  ],
};

const page = (items: unknown[]) =>
  json(200, {
    result: { items, page: 1, per_page: 100, total_count: items.length },
  });

// mcpttRoutes are the API of an IMS running MCPTT, of the fixtures; status false of one running none.
export const mcpttRoutes = (
  overrides: Record<string, (url: URL, init?: RequestInit) => Response> = {},
  enabled = true,
): Record<string, (url: URL, init?: RequestInit) => Response> => ({
  "/api/v1/status": () =>
    json(200, {
      result: {
        version: "v0.0.1",
        revision: "",
        ...(enabled && { mcptt: mcpttStatus }),
      },
    }),
  "/api/v1/operator": () =>
    json(200, {
      result: {
        mcc: "001",
        mnc: "01",
        numbering: {
          country_code: "1",
          national_prefix: "",
          international_prefix: "",
        },
      },
    }),
  "/api/v1/registrations": () => page([]),
  "/api/v1/mcptt/service-configuration": () =>
    json(200, { result: serviceConfiguration }),
  "/api/v1/mcptt/users": () => page([alice, bob]),
  "/api/v1/mcptt/groups": () => page([fire]),
  "/api/v1/mcptt/registrations": () => page(registrations),
  "/api/v1/mcptt/affiliations": () => page(affiliations),
  "/api/v1/mcptt/calls": () => page([call]),
  ...overrides,
});
