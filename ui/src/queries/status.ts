import { apiFetch } from "@/queries/utils";

export interface Status {
  version: string;
  revision: string;
  initialized: boolean;
}

export const getStatus = (): Promise<Status> =>
  apiFetch<Status>("/api/v1/status");
