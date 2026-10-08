import type { CallOutcome, CallRecord } from "@/queries/callRecords";
import { numberOfURI } from "@/utils/registrations";

export const outcomeLabels: Record<CallOutcome, string> = {
  answered: "answered",
  cancelled: "cancelled",
  busy: "busy",
  rejected: "rejected",
  no_answer: "no answer",
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

// partyOf shows a party by its number, else by its URI.
const partyOf = (uri: string): string => numberOfURI(uri) ?? uri;

// callerOf is the caller's number, else its first asserted identity, else its private identity.
export const callerOf = (record: CallRecord): string => {
  const number = record.calling_party
    .map(numberOfURI)
    .find((n) => n !== undefined);

  return number ?? record.calling_party[0] ?? record.caller_impi ?? "—";
};

// calleeOf is the party the IMS routed the call to, else the one the caller dialled.
export const calleeOf = (record: CallRecord): string =>
  partyOf(record.called_party ?? record.requested_party);

// formatDuration is a duration as m:ss, or h:mm:ss from an hour.
export const formatDuration = (ms: number): string => {
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = String(total % 60).padStart(2, "0");

  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
};
