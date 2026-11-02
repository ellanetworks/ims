import type { CallOutcome, CallRecord } from "@/queries/callRecords";
import { numberOfURI } from "@/utils/registrations";

export const outcomeLabels: Record<CallOutcome, string> = {
  answered: "answered",
  cancelled: "cancelled",
  busy: "busy",
  rejected: "rejected",
  no_answer: "no answer",
  unavailable: "unavailable",
  failed: "failed",
};

export const endedByLabels: Record<
  NonNullable<CallRecord["ended_by"]>,
  string
> = {
  caller: "Caller",
  callee: "Callee",
  network: "Network",
};

export const partyOf = (uri: string): string => numberOfURI(uri) ?? uri;

export const dialledOf = (uri: string): string =>
  numberOfURI(uri) ?? /^tel:([^;]+)/i.exec(uri)?.[1] ?? uri;

export const callerOf = (record: CallRecord): string => {
  const number = record.calling_party
    .map(numberOfURI)
    .find((n) => n !== undefined);

  return number ?? record.calling_party[0] ?? record.caller_impi ?? "—";
};

export const calleeOf = (record: CallRecord): string =>
  partyOf(record.called_party ?? record.requested_party);

export const formatDuration = (ms: number): string => {
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = String(total % 60).padStart(2, "0");

  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
};
