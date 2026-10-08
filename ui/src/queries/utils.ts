export type ApiErrorKind = "network" | "http";

export class ApiError extends Error {
  readonly kind: ApiErrorKind;
  readonly status?: number;

  constructor(kind: ApiErrorKind, message: string, status?: number) {
    super(message);
    this.name = "ApiError";
    this.kind = kind;
    this.status = status;
  }
}

export interface Page<T> {
  items: T[];
  page: number;
  per_page: number;
  total_count: number;
}

export interface PageParams {
  page?: number;
  perPage?: number;
}

interface ApiFetchOptions {
  method?: string;
  body?: unknown;
}

export async function apiFetch<T>(
  url: string,
  options: ApiFetchOptions = {},
): Promise<T> {
  const { method = "GET", body } = options;

  const init: RequestInit = { method };
  if (body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(body);
  }

  let response: Response;
  try {
    response = await fetch(url, init);
  } catch {
    throw new ApiError("network", "Cannot reach the server.");
  }

  let data: { result?: T; error?: string } | undefined;
  try {
    data = await response.json();
  } catch {
    data = undefined;
  }

  if (!response.ok) {
    throw new ApiError(
      "http",
      data?.error || response.statusText || `HTTP ${response.status}`,
      response.status,
    );
  }

  if (data === undefined) {
    throw new ApiError(
      "http",
      "The server returned an invalid response.",
      response.status,
    );
  }

  return data.result as T;
}

export function withQuery(
  path: string,
  params: Record<string, string | number | undefined>,
): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") {
      query.set(key, String(value));
    }
  }

  const qs = query.toString();
  return qs ? `${path}?${qs}` : path;
}
