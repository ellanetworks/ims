import { apiFetch } from "@/queries/utils";

export type SIPTransport = "udp" | "tcp" | "tls";

export type SIPTrunkState = "unknown" | "up" | "down";

export interface SIPTrunkAddress {
  host: string;
  port?: number;
  transport?: SIPTransport;
}

export interface SIPTrunkTLSParams {
  required: boolean;
  ca_certificates?: string;
  pins?: string[];
  identities?: string[];
}

export interface CertificateSummary {
  subject: string;
  issuer: string;
  dns_names: string[];
  uris: string[];
  ip_addresses: string[];
  identities: string[];
  ca: boolean;
  not_before: string;
  not_after: string;
  sha256_fingerprint: string;
}

export interface SIPTrunkTLS extends SIPTrunkTLSParams {
  ca_certificate_details?: CertificateSummary[];
}

export interface SIPTrunkParams {
  name: string;
  addresses: SIPTrunkAddress[];
  inbound_cidrs?: string[];
  domain?: string;
  trusted: boolean;
  keepalive_interval: number;
  enabled?: boolean;
  tls?: SIPTrunkTLSParams;
}

export interface SIPTrunk extends SIPTrunkParams {
  id: string;
  enabled: boolean;
  tls?: SIPTrunkTLS;
  status: { state: SIPTrunkState; since?: string };
}

export interface SIPRouteParams {
  prefix: string;
  trunks: string[];
}

export interface SIPRoute extends SIPRouteParams {
  id: string;
}

const trunkUrl = (id: string) => `/api/v1/sip-trunks/${encodeURIComponent(id)}`;

const routeUrl = (id: string) => `/api/v1/sip-routes/${encodeURIComponent(id)}`;

export const listSIPTrunks = async (): Promise<SIPTrunk[]> => {
  const { items } = await apiFetch<{ items: SIPTrunk[] }>("/api/v1/sip-trunks");
  return items;
};

export const createSIPTrunk = (params: SIPTrunkParams): Promise<SIPTrunk> =>
  apiFetch<SIPTrunk>("/api/v1/sip-trunks", { method: "POST", body: params });

export const updateSIPTrunk = (
  id: string,
  params: SIPTrunkParams,
): Promise<SIPTrunk> =>
  apiFetch<SIPTrunk>(trunkUrl(id), { method: "PUT", body: params });

export const deleteSIPTrunk = async (id: string): Promise<void> => {
  await apiFetch(trunkUrl(id), { method: "DELETE" });
};

export const listSIPRoutes = async (): Promise<SIPRoute[]> => {
  const { items } = await apiFetch<{ items: SIPRoute[] }>("/api/v1/sip-routes");
  return items;
};

export const createSIPRoute = (params: SIPRouteParams): Promise<SIPRoute> =>
  apiFetch<SIPRoute>("/api/v1/sip-routes", { method: "POST", body: params });

export const updateSIPRoute = (
  id: string,
  params: SIPRouteParams,
): Promise<SIPRoute> =>
  apiFetch<SIPRoute>(routeUrl(id), { method: "PUT", body: params });

export const deleteSIPRoute = async (id: string): Promise<void> => {
  await apiFetch(routeUrl(id), { method: "DELETE" });
};
