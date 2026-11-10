import { apiFetch } from "@/queries/utils";

export interface Formation {
  state: "forming" | "joining" | "member" | "removed";
  expected?: number;
  found?: number;
  detail?: string;
}

export interface Status {
  version: string;
  revision: string;
  initialized: boolean;
  clustered: boolean;
  node_id: string;
  display_name: string;
  diameter_label: string;
  diameter_host: string;
  label_generated: boolean;
  formation?: Formation;
}

export const getStatus = (): Promise<Status> =>
  apiFetch<Status>("/api/v1/status");
