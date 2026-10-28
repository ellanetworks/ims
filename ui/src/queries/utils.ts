export type ApiErrorKind = "network" | "auth" | "forbidden" | "http";

// The access token is kept in memory only, never in storage, so that a script injected into the page cannot read it
// after a reload.
let accessToken: string | null = null;

export function setAccessToken(token: string | null) {
  accessToken = token;
}

let renewAccessToken: (() => Promise<boolean>) | null = null;

// setAccessTokenRenewer sets what renews the access token when the server refuses it, as when it expired while the
// computer slept. It resolves to whether there is a new one; when the session is over, it ends it.
export function setAccessTokenRenewer(renew: (() => Promise<boolean>) | null) {
  renewAccessToken = renew;
}

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
  // anonymous requests send no access token: those of the session itself, which the cookie authenticates.
  anonymous?: boolean;
}

export async function apiFetch<T>(
  url: string,
  options: ApiFetchOptions = {},
  retried = false,
): Promise<T> {
  const { method = "GET", body, anonymous = false } = options;

  const headers: Record<string, string> = {};
  const token = anonymous ? null : accessToken;
  if (token) headers.Authorization = `Bearer ${token}`;

  const init: RequestInit = { method };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  if (Object.keys(headers).length > 0) init.headers = headers;

  let response: Response;
  try {
    response = await fetch(url, init);
  } catch {
    throw new ApiError("network", "Cannot reach the server.");
  }

  if (response.status === 204) return undefined as T;

  let data: { result?: T; error?: string } | undefined;
  try {
    data = await response.json();
  } catch {
    data = undefined;
  }

  if (!response.ok) {
    const message =
      data?.error || response.statusText || `HTTP ${response.status}`;
    // A refused access token is renewed once, and the request retried. A failed login sends none.
    if (response.status === 401 && token) {
      if (!retried && (await renewAccessToken?.())) {
        return apiFetch<T>(url, options, true);
      }
      throw new ApiError("auth", message, response.status);
    }
    if (response.status === 403) {
      throw new ApiError("forbidden", message, response.status);
    }
    throw new ApiError("http", message, response.status);
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

// withQuery adds query parameters to a path: a list is repeated, and an empty value dropped.
export function withQuery(
  path: string,
  params: Record<string, string | number | string[] | undefined>,
): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    for (const v of Array.isArray(value) ? value : [value]) {
      if (v !== undefined && v !== "") {
        query.append(key, String(v));
      }
    }
  }

  const qs = query.toString();
  return qs ? `${path}?${qs}` : path;
}
