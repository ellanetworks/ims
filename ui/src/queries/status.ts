import { apiFetch } from "@/queries/utils";

// MCPTTStatus is what the built-in MCPTT AS runs: its server name and PSIs, which the HSS routes to it.
export interface MCPTTStatus {
  mode: "ims" | "direct";
  name: string;
  participating_psi: string;
  controlling_psi: string;
  sip: string[];
  media_control: string;
}

export interface Status {
  version: string;
  revision: string;
  // mcptt is absent when the IMS runs no MCPTT AS.
  mcptt?: MCPTTStatus;
}

export const getStatus = (): Promise<Status> =>
  apiFetch<Status>("/api/v1/status");
