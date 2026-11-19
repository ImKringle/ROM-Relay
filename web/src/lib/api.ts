// One door to the relay's JSON API. Every mutation carries the CSRF token,
// which the server requires of cookie sessions; errors surface the server's
// own message, which is written for people.

let csrfToken = "";

export function setCSRF(token: string) {
  csrfToken = token;
}

export class ApiError extends Error {
  status: number;
  report?: string[]; // a legality rejection's per-line violations
  constructor(status: number, message: string, report?: string[]) {
    super(message);
    this.status = status;
    this.report = report;
  }
}

type Body = Record<string, unknown> | FormData | URLSearchParams | undefined;

async function request<T>(method: string, path: string, body?: Body): Promise<T> {
  const headers: Record<string, string> = {};
  let payload: BodyInit | undefined;
  if (method !== "GET" && method !== "HEAD") headers["X-CSRF-Token"] = csrfToken;
  if (body instanceof FormData || body instanceof URLSearchParams) {
    payload = body;
  } else if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }
  const response = await fetch(path, { method, headers, body: payload, credentials: "same-origin" });
  if (!response.ok) {
    const text = (await response.text()).trim();
    // Some endpoints answer errors as JSON {error, report}; most as plain text.
    try {
      const j = JSON.parse(text);
      if (j && typeof j.error === "string") throw new ApiError(response.status, j.error, Array.isArray(j.report) ? j.report : undefined);
    } catch (e) {
      if (e instanceof ApiError) throw e;
    }
    throw new ApiError(response.status, text || `${response.status} ${response.statusText}`);
  }
  if (response.status === 204) return undefined as T;
  const type = response.headers.get("Content-Type") || "";
  return (type.includes("application/json") ? response.json() : response.text()) as Promise<T>;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body?: Body) => request<T>("POST", path, body),
  put: <T>(path: string, body?: Body) => request<T>("PUT", path, body),
  del: <T>(path: string, body?: Body) => request<T>("DELETE", path, body),
};

// Query string from an object, skipping empty values; arrays repeat the key.
export function qs(params: Record<string, string | number | boolean | string[] | undefined>) {
  const out = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === "" || v === false) continue;
    if (Array.isArray(v)) v.forEach((x) => out.append(k, x));
    else out.set(k, v === true ? "1" : String(v));
  }
  const s = out.toString();
  return s ? `?${s}` : "";
}

export function bytes(n: number) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(i ? 1 : 0)} ${units[i]}`;
}
