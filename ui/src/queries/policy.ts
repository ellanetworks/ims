import { apiFetch } from "@/queries/utils";

export type PolicyInterface = "none" | "rx" | "n5";

export interface Policy {
  interface: PolicyInterface;
  n5?: {
    pcf_uri: string;
  };
}

export interface PolicyStatus {
  interface: PolicyInterface;
  endpoint?: string;
  notify?: string;
  last?: {
    at: string;
    reachable: boolean;
    result: string;
  };
}

export interface PolicyWithStatus extends Policy {
  status: PolicyStatus;
}

export const getPolicy = (): Promise<PolicyWithStatus> =>
  apiFetch<PolicyWithStatus>("/api/v1/policy");

export const updatePolicy = (policy: Policy): Promise<PolicyWithStatus> =>
  apiFetch<PolicyWithStatus>("/api/v1/policy", {
    method: "PUT",
    body: policy,
  });
