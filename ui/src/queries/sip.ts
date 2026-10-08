import { apiFetch } from "@/queries/utils";

export interface SIPListener {
  role: string;
  address: string;
  transports: string[];
}

export interface SIPStatus {
  home_domain: string;
  aliases: string[];
  listeners: SIPListener[];
}

export const getSIPStatus = (): Promise<SIPStatus> =>
  apiFetch<SIPStatus>("/api/v1/sip");
