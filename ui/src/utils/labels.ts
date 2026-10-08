import type {
  DiameterApplication,
  DiameterTransport,
} from "@/queries/diameter";
import type { PolicyInterface } from "@/queries/policy";

export const applicationLabels: Record<DiameterApplication, string> = {
  cx: "HSS",
  rx: "PCRF",
};

export const transportLabels: Record<DiameterTransport, string> = {
  tcp: "TCP",
  sctp: "SCTP",
};

export const policyLabels: Record<PolicyInterface, string> = {
  none: "None",
  rx: "PCRF",
  n5: "PCF",
};
