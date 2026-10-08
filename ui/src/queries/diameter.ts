import { apiFetch } from "@/queries/utils";

export interface DiameterIdentity {
  host: string;
  realm: string;
}

export type DiameterApplication = "cx" | "rx";

export type DiameterTransport = "tcp" | "sctp";

export type DiameterPeerState =
  "down" | "connecting" | "open" | "suspect" | "reopen" | "closing";

export interface DiameterPeerParams {
  host: string;
  realm: string;
  address: string;
  port?: number;
  transport?: DiameterTransport;
  applications: DiameterApplication[];
}

export interface DiameterPeerStatus {
  state: DiameterPeerState;
  since?: string;
  remote_address?: string;
}

export interface DiameterPeer extends DiameterPeerParams {
  id: string;
  status: DiameterPeerStatus;
}

const peerUrl = (id: string) =>
  `/api/v1/diameter/peers/${encodeURIComponent(id)}`;

export const getDiameterIdentity = (): Promise<DiameterIdentity> =>
  apiFetch<DiameterIdentity>("/api/v1/diameter");

export const listDiameterPeers = async (): Promise<DiameterPeer[]> => {
  const { items } = await apiFetch<{ items: DiameterPeer[] }>(
    "/api/v1/diameter/peers",
  );
  return items;
};

export const getDiameterPeer = (id: string): Promise<DiameterPeer> =>
  apiFetch<DiameterPeer>(peerUrl(id));

export const createDiameterPeer = (
  params: DiameterPeerParams,
): Promise<DiameterPeer> =>
  apiFetch<DiameterPeer>("/api/v1/diameter/peers", {
    method: "POST",
    body: params,
  });

export const updateDiameterPeer = (
  id: string,
  params: DiameterPeerParams,
): Promise<DiameterPeer> =>
  apiFetch<DiameterPeer>(peerUrl(id), { method: "PUT", body: params });

export const deleteDiameterPeer = async (id: string): Promise<void> => {
  await apiFetch(peerUrl(id), { method: "DELETE" });
};
