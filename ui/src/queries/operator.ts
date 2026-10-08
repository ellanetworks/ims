import { apiFetch } from "@/queries/utils";

export interface Numbering {
  country_code: string;
  national_prefix: string;
  international_prefix: string;
}

export interface Operator {
  mcc: string;
  mnc: string;
  numbering: Numbering;
}

export const getOperator = (): Promise<Operator> =>
  apiFetch<Operator>("/api/v1/operator");

export const updateOperator = (operator: Operator): Promise<Operator> =>
  apiFetch<Operator>("/api/v1/operator", {
    method: "PUT",
    body: operator,
  });
