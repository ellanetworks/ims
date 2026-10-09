import {
  apiFetch,
  withQuery,
  type Page,
  type PageParams,
} from "@/queries/utils";

export type CallOutcome =
  | "answered"
  | "cancelled"
  | "busy"
  | "rejected"
  | "no_answer"
  | "unavailable"
  | "failed";

export const CALL_OUTCOMES: CallOutcome[] = [
  "answered",
  "cancelled",
  "busy",
  "rejected",
  "no_answer",
  "unavailable",
  "failed",
];

// CallRecord is a call a registered UE made (TS 32.298 §5.1.3.1). A value the IMS did not observe is absent.
export interface CallRecord {
  id: number;
  icid: string;
  session_id: string;
  // The identities asserted for the caller.
  calling_party: string[];
  caller_impi?: string;
  // The Request-URI the caller sent, as dialled.
  requested_party: string;
  // The Request-URI the IMS routed the call to, normalised.
  called_party?: string;
  callee_impi?: string;
  requested_at: string;
  // When the caller got the final response, a 2xx or an error.
  delivery_start_at?: string;
  delivery_end_at?: string;
  sip_status?: number;
  outcome?: CallOutcome;
  ended_by?: "caller" | "callee" | "network";
  alerted: boolean;
  media: string[];
  in_progress: boolean;
  // Closed without the end of the call.
  incomplete: boolean;
  duration_ms?: number;
}

export interface ListCallRecordsParams extends PageParams {
  search?: string;
  // from and to bound when the calls were requested, as RFC 3339 times: from included, to excluded.
  from?: string;
  to?: string;
  outcomes?: CallOutcome[];
}

export interface CallRecordRetention {
  days: number;
}

export const listCallRecords = (
  params: ListCallRecordsParams = {},
): Promise<Page<CallRecord>> =>
  apiFetch<Page<CallRecord>>(
    withQuery("/api/v1/call-records", {
      page: params.page,
      per_page: params.perPage,
      search: params.search,
      from: params.from,
      to: params.to,
      outcome: params.outcomes,
    }),
  );

export const getCallRecord = (id: number): Promise<CallRecord> =>
  apiFetch<CallRecord>(`/api/v1/call-records/${id}`);

export const getCallRecordRetention = (): Promise<CallRecordRetention> =>
  apiFetch<CallRecordRetention>("/api/v1/call-records/retention");

export const updateCallRecordRetention = (
  retention: CallRecordRetention,
): Promise<CallRecordRetention> =>
  apiFetch<CallRecordRetention>("/api/v1/call-records/retention", {
    method: "PUT",
    body: retention,
  });
