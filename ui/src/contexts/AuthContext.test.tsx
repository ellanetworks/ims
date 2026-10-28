import { afterEach, describe, expect, it, vi } from "vitest";
import { act, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { AuthProvider, useAuth } from "@/contexts/AuthContext";
import { RoleID } from "@/queries/users";
import { apiFetch } from "@/queries/utils";
import { makeToken } from "@/test/jwt";
import { json, renderWithClient, stubApi } from "@/test/render";

function Who() {
  const auth = useAuth();
  return (
    <p>
      {auth.email} {auth.can("user:admin") ? "admin" : "not admin"}
    </p>
  );
}

const renderProvider = () =>
  renderWithClient(
    <MemoryRouter initialEntries={["/cores"]}>
      <Routes>
        <Route path="/login" element={<p>login page</p>} />
        <Route
          path="*"
          element={
            <AuthProvider>
              <Who />
            </AuthProvider>
          }
        />
      </Routes>
    </MemoryRouter>,
  );

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("AuthProvider", () => {
  it("gets the access token of the session and its user's role", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/auth/refresh": () =>
            json(200, {
              result: { token: makeToken("ro@example.com", RoleID.ReadOnly) },
            }),
        }),
      ),
    );

    renderProvider();

    expect(
      await screen.findByText("ro@example.com not admin"),
    ).toBeInTheDocument();
  });

  it("renews the access token two minutes before it expires", async () => {
    vi.useFakeTimers();
    const refresh = vi.fn(() =>
      json(200, {
        result: { token: makeToken("admin@example.com", RoleID.Admin, 900) },
      }),
    );
    vi.stubGlobal("fetch", vi.fn(stubApi({ "/api/v1/auth/refresh": refresh })));

    renderProvider();
    await act(() => vi.advanceTimersByTimeAsync(0));
    // StrictMode starts the session twice in tests.
    const started = refresh.mock.calls.length;

    await act(() => vi.advanceTimersByTimeAsync(779_000));
    expect(refresh).toHaveBeenCalledTimes(started);

    await act(() => vi.advanceTimersByTimeAsync(1_000));
    expect(refresh).toHaveBeenCalledTimes(started + 1);
  });

  it("goes to the login page without a session", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/auth/refresh": () =>
            json(401, { error: "No valid session" }),
        }),
      ),
    );

    renderProvider();

    expect(await screen.findByText("login page")).toBeInTheDocument();
  });

  it("keeps the session when a refresh fails without ending it, and retries", async () => {
    vi.useFakeTimers();
    let answer = () =>
      json(200, {
        result: { token: makeToken("admin@example.com", RoleID.Admin, 900) },
      });
    const refresh = vi.fn(() => answer());
    vi.stubGlobal("fetch", vi.fn(stubApi({ "/api/v1/auth/refresh": refresh })));

    renderProvider();
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(screen.getByText("admin@example.com admin")).toBeInTheDocument();
    const started = refresh.mock.calls.length;

    answer = () => json(503, { error: "restarting" });
    await act(() => vi.advanceTimersByTimeAsync(780_000));
    expect(refresh).toHaveBeenCalledTimes(started + 1);
    expect(screen.getByText("admin@example.com admin")).toBeInTheDocument();

    await act(() => vi.advanceTimersByTimeAsync(30_000));
    expect(refresh).toHaveBeenCalledTimes(started + 2);
    expect(screen.queryByText("login page")).not.toBeInTheDocument();
  });

  it("renews an access token the server refuses, and retries the request", async () => {
    let token = makeToken("admin@example.com", RoleID.Admin, 900);
    const fetchMock = vi.fn(
      stubApi({
        "/api/v1/auth/refresh": () => json(200, { result: { token } }),
        "/api/v1/operator": (_url, init) =>
          (init?.headers as Record<string, string>)?.Authorization ===
          `Bearer ${token}`
            ? json(200, { result: { ok: true } })
            : json(401, { error: "Invalid token" }),
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    renderProvider();
    await screen.findByText("admin@example.com admin");

    // The access token expired while the computer slept.
    token = makeToken("admin@example.com", RoleID.Admin, 900) + "renewed";
    await expect(apiFetch("/api/v1/operator")).resolves.toEqual({ ok: true });
    expect(screen.queryByText("login page")).not.toBeInTheDocument();
  });
});
