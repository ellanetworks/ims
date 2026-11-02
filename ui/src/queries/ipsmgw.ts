import { apiFetch } from "@/queries/utils";

export interface IPSMGW {
  number: string;
  name: string;
}

export const getIPSMGW = (): Promise<IPSMGW> =>
  apiFetch<IPSMGW>("/api/v1/ip-sm-gw");

export const updateIPSMGW = (number: string): Promise<IPSMGW> =>
  apiFetch<IPSMGW>("/api/v1/ip-sm-gw", { method: "PUT", body: { number } });
