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

// TS 32.298 clause 5.1.3.1
export interface CallRecord {
  id: number;
  icid: string;
  session_id: string;
  calling_party: string[];
  caller_impi?: string;
  requested_party: string;
  called_party?: string;
  callee_impi?: string;
  requested_at: string;
  delivery_start_at?: string;
  delivery_end_at?: string;
  sip_status?: number;
  outcome?: CallOutcome;
  ended_by?: "caller" | "callee" | "network";
  alerted: boolean;
  media: string[];
  in_progress: boolean;
  incomplete: boolean;
  duration_ms?: number;
}

export interface ListCallRecordsParams extends PageParams {
  search?: string;
  start?: string;
  end?: string;
  outcomes?: CallOutcome[];
}

export type CallRecordCounts = Record<CallOutcome, number>;

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
      start: params.start,
      end: params.end,
      outcome: params.outcomes,
    }),
  );

export const countCallRecords = (
  params: Pick<ListCallRecordsParams, "start" | "end"> = {},
): Promise<CallRecordCounts> =>
  apiFetch<CallRecordCounts>(
    withQuery("/api/v1/call-records/counts", {
      start: params.start,
      end: params.end,
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
