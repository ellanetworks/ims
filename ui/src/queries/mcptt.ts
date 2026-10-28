import { ApiError, apiFetch, withQuery, type Page } from "@/queries/utils";

const API = "/api/v1/mcptt";

// MCPTTStatus is what the built-in MCPTT AS runs: its server name and PSIs, which the HSS routes to it.
export interface MCPTTStatus {
  mode: "ims" | "direct";
  name: string;
  participating_psi: string;
  controlling_psi: string;
  sip: string[];
  media_control: string;
}

// getMCPTTStatus is the status of the MCPTT AS, or null if the IMS runs none, which the server answers with 404.
export const getMCPTTStatus = async (): Promise<MCPTTStatus | null> => {
  try {
    return await apiFetch<MCPTTStatus>(API);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null;
    throw err;
  }
};

// ServiceConfiguration is what an operator sets of the organisation's service; the server fixes the rest. The
// domain is the IMS home domain until set; the transmit time limit 30 s if absent.
export interface ServiceConfiguration {
  domain: string;
  transmit_time_limit_ms?: number;
}

export interface MCPTTUser {
  mcptt_id: string;
  alias: string;
  impus: string[];
  implicit_affiliations: string[];
}

export interface Member {
  mcptt_id: string;
  recvonly: boolean;
  // implicit_affiliation is of the user's profile: the group is one of its implicit affiliations. Absent from a
  // request, it stays as it is.
  implicit_affiliation?: boolean;
}

export interface Group {
  group_id: string;
  display_name: string;
  hang_timer_ms?: number;
  maximum_duration_ms: number;
  release_on_initiator_leave: boolean;
  members: Member[];
}

export interface ClientRegistration {
  mcptt_id: string;
  client_id: string;
  impu: string;
  impus: string[];
  since: string;
  expires_at: string;
  contacts: string[];
}

export type AffiliationStatus = "affiliating" | "affiliated" | "deaffiliating";

export interface Affiliation {
  mcptt_id: string;
  client_id: string;
  group_id: string;
  status: AffiliationStatus;
  // How the client was affiliated: by the server, from the user's implicit affiliations; false if by the client.
  implicit: boolean;
}

export interface Participant {
  mcptt_id: string;
  state: "invited" | "joined";
  required: boolean;
  recvonly: boolean;
}

export interface GroupCall {
  id: string;
  session_id: string;
  group_id: string;
  originator: string;
  created_at: string;
  started_at: string | null;
  ends_at: string | null;
  talker: { mcptt_id: string; since: string } | null;
  participants: Participant[];
}

const MAX_PER_PAGE = 100;

// listAll is every item of a paged list, a page of the most items after another.
export async function listAll<T>(
  path: string,
  params: Record<string, string | undefined> = {},
): Promise<T[]> {
  const items: T[] = [];

  for (let page = 1; ; page++) {
    const result = await apiFetch<Page<T>>(
      withQuery(path, { ...params, page, per_page: MAX_PER_PAGE }),
    );
    items.push(...result.items);

    if (result.items.length === 0 || items.length >= result.total_count) {
      return items;
    }
  }
}

const one = (collection: string, id: string) =>
  `${API}/${collection}/${encodeURIComponent(id)}`;

export const getServiceConfiguration = (): Promise<ServiceConfiguration> =>
  apiFetch<ServiceConfiguration>(`${API}/service-configuration`);

export const updateServiceConfiguration = (
  c: ServiceConfiguration,
): Promise<ServiceConfiguration> =>
  apiFetch<ServiceConfiguration>(`${API}/service-configuration`, {
    method: "PUT",
    body: c,
  });

export const listUsers = (): Promise<MCPTTUser[]> =>
  listAll<MCPTTUser>(`${API}/users`);

export const createUser = (u: MCPTTUser): Promise<MCPTTUser> =>
  apiFetch<MCPTTUser>(`${API}/users`, { method: "POST", body: u });

export const updateUser = (u: MCPTTUser): Promise<MCPTTUser> =>
  apiFetch<MCPTTUser>(one("users", u.mcptt_id), { method: "PUT", body: u });

export const deleteUser = (id: string): Promise<unknown> =>
  apiFetch(one("users", id), { method: "DELETE" });

export const listGroups = (): Promise<Group[]> =>
  listAll<Group>(`${API}/groups`);

export const createGroup = (g: Group): Promise<Group> =>
  apiFetch<Group>(`${API}/groups`, { method: "POST", body: g });

export const updateGroup = (g: Group): Promise<Group> =>
  apiFetch<Group>(one("groups", g.group_id), { method: "PUT", body: g });

export const deleteGroup = (id: string): Promise<unknown> =>
  apiFetch(one("groups", id), { method: "DELETE" });

export const putMember = (group: string, m: Member): Promise<Group> =>
  apiFetch<Group>(
    `${one("groups", group)}/members/${encodeURIComponent(m.mcptt_id)}`,
    { method: "PUT", body: m },
  );

export const deleteMember = (group: string, id: string): Promise<Group> =>
  apiFetch<Group>(`${one("groups", group)}/members/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });

export const listClientRegistrations = (): Promise<ClientRegistration[]> =>
  listAll<ClientRegistration>(`${API}/registrations`);

export const listAffiliations = (): Promise<Affiliation[]> =>
  listAll<Affiliation>(`${API}/affiliations`);

export const listGroupCalls = (): Promise<GroupCall[]> =>
  listAll<GroupCall>(`${API}/calls`);

// The query keys of the MCPTT settings and live state.
export const mcpttKeys = {
  status: ["mcptt", "status"],
  serviceConfiguration: ["mcptt", "service-configuration"],
  users: ["mcptt", "users"],
  groups: ["mcptt", "groups"],
  registrations: ["mcptt", "registrations"],
  affiliations: ["mcptt", "affiliations"],
  calls: ["mcptt", "calls"],
} as const;
