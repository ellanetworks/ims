import { apiFetch } from "@/queries/utils";

export interface Status {
  version: string;
  revision: string;
  // initialized is whether the first user exists, which the UI needs before a login.
  initialized: boolean;
}

export const getStatus = (): Promise<Status> =>
  apiFetch<Status>("/api/v1/status");
