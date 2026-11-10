import { apiFetch, withQuery } from "@/queries/utils";

export type DrainState = "active" | "draining" | "drained";

export type Suffrage = "voter" | "nonvoter" | "none";

export interface ClusterMember {
  node_id: string;
  address: string;
  api_address?: string;
  diameter_label: string;
  diameter_host: string;
  label_generated: boolean;
  display_name: string;
  version: string;
  joined_at: string;
  drain_state: DrainState;
  suffrage: Suffrage;
  leader: boolean;
  healthy?: boolean;
  last_contact?: string;
}

export interface ClusterStatus {
  node_id: string;
  cluster_id: string;
  clustered: boolean;
  leader_id?: string;
  term: number;
  applied_index: number;
  commit_index: number;
  schema: number;
  healthy?: boolean;
  failure_tolerance?: number;
  members: ClusterMember[] | null;
}

export interface JoinToken {
  token: string;
  expires_at: string;
}

export const MAX_DISPLAY_NAME_LENGTH = 63;

const memberPath = (nodeId: string) =>
  `/api/v1/cluster/members/${encodeURIComponent(nodeId)}`;

export const getCluster = (): Promise<ClusterStatus> =>
  apiFetch<ClusterStatus>("/api/v1/cluster");

export const createJoinToken = (ttlSeconds: number): Promise<JoinToken> =>
  apiFetch<JoinToken>("/api/v1/cluster/tokens", {
    method: "POST",
    body: { ttl_seconds: ttlSeconds },
  });

export const joinCluster = (token: string) =>
  apiFetch<{ message: string }>("/api/v1/cluster/join", {
    method: "POST",
    body: { token },
    anonymous: true,
  });

export const updateDisplayName = (nodeId: string, displayName: string) =>
  apiFetch<{ message: string }>(memberPath(nodeId), {
    method: "PUT",
    body: { display_name: displayName },
  });

export const drainMember = (nodeId: string) =>
  apiFetch<{ message: string }>(`${memberPath(nodeId)}/drain`, {
    method: "POST",
  });

export const resumeMember = (nodeId: string) =>
  apiFetch<{ message: string }>(`${memberPath(nodeId)}/resume`, {
    method: "POST",
  });

export const removeMember = (nodeId: string, force: boolean) =>
  apiFetch<{ message: string }>(
    withQuery(memberPath(nodeId), { force: force ? "true" : undefined }),
    { method: "DELETE" },
  );

export const shortNodeId = (nodeId: string) =>
  nodeId.length > 12 ? nodeId.slice(-12) : nodeId;

export const nodeLabel = (
  displayName: string | undefined,
  diameterLabel: string | undefined,
  nodeId: string,
) => displayName || diameterLabel || shortNodeId(nodeId);

export const memberLabel = (member: ClusterMember) =>
  nodeLabel(member.display_name, member.diameter_label, member.node_id);

export const leaderOf = (status: ClusterStatus) =>
  status.members?.find((m) => m.node_id === status.leader_id);

export const answersAsLeader = (status: ClusterStatus) =>
  status.leader_id !== undefined && status.leader_id === status.node_id;

export const GENERATED_HOST_HINT =
  "Generated from the node ID. A reinstalled node gets a new host. Set diameter.host_label in the node's configuration file to keep it.";
