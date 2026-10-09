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
  address: string;
  port?: number;
  transport?: DiameterTransport;
  applications: DiameterApplication[];
  priority?: number;
}

export interface DiameterPeerStatus {
  state: DiameterPeerState;
  since?: string;
  remote_address?: string;
  realm?: string;
}

export interface DiameterPeer extends DiameterPeerParams {
  id: string;
  status: DiameterPeerStatus;
}

// DiameterRoute is where an application's requests go: to destination_realm, through its peers in the order they
// are tried. An empty realm is the home domain.
export interface DiameterRoute {
  application: DiameterApplication;
  realm: string;
  destination_realm: string;
  peers: {
    id: string;
    host: string;
    priority: number;
    status: DiameterPeerStatus;
  }[];
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

export const listDiameterRoutes = async (): Promise<DiameterRoute[]> => {
  const { items } = await apiFetch<{ items: DiameterRoute[] }>(
    "/api/v1/diameter/routes",
  );
  return items;
};

export const updateDiameterRoute = (
  application: DiameterApplication,
  realm: string,
): Promise<DiameterRoute> =>
  apiFetch<DiameterRoute>(
    `/api/v1/diameter/routes/${encodeURIComponent(application)}`,
    { method: "PUT", body: { realm } },
  );
