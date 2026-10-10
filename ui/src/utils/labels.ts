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

export const mediaLabels: Record<string, string> = {
  audio: "Voice",
  video: "Video",
};

// mediaText is media as the parties declared them, "Voice, Video", or "—" for none.
export const mediaText = (media: string[]): string =>
  media.map((m) => mediaLabels[m] ?? m).join(", ") || "—";
