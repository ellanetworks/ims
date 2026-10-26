import { apiFetch } from "@/queries/utils";

// IPSMGW is the built-in IP-SM-GW: its E.164 number, empty until set, and its name, the SIP URI the iFCs of the HSS
// route to it by.
export interface IPSMGW {
  number: string;
  name: string;
}

export const getIPSMGW = (): Promise<IPSMGW> =>
  apiFetch<IPSMGW>("/api/v1/ip-sm-gw");

export const updateIPSMGW = (number: string): Promise<IPSMGW> =>
  apiFetch<IPSMGW>("/api/v1/ip-sm-gw", { method: "PUT", body: { number } });
