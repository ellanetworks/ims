import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import Login from "@/pages/Login";
import { makeToken } from "@/test/jwt";
import { json, renderWithClient, stubApi } from "@/test/render";

const serve = ({
  initialized = true,
  session = false,
  login = () => json(200, { result: { token: makeToken() } }),
}: {
  initialized?: boolean;
  session?: boolean;
  login?: (init?: RequestInit) => Response;
} = {}) =>
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/status": () =>
          json(200, {
            result: { version: "v0.0.1", revision: "", initialized },
          }),
        "/api/v1/auth/refresh": () =>
          session
            ? json(200, { result: { token: makeToken() } })
            : json(401, { error: "No valid session" }),
        "/api/v1/auth/login": (_url, init) => login(init),
      }),
    ),
  );

const renderLogin = (from?: string) =>
  renderWithClient(
    <MemoryRouter
      initialEntries={[{ pathname: "/login", state: from ? { from } : null }]}
    >
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/initialize" element={<p>initialize page</p>} />
        <Route path="/cores" element={<p>cores page</p>} />
        <Route path="/" element={<p>home page</p>} />
      </Routes>
    </MemoryRouter>,
  );

const fill = (label: string, value: string) =>
  fireEvent.change(
    screen.getByLabelText(label, { exact: false, selector: "input" }),
    {
      target: { value },
    },
  );

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Login", () => {
  it("sends an uninitialized system to its initialization", async () => {
    serve({ initialized: false });
    renderLogin();

    expect(await screen.findByText("initialize page")).toBeInTheDocument();
  });

  it("lets a user with a session through", async () => {
    serve({ session: true });
    renderLogin("/cores");

    expect(await screen.findByText("cores page")).toBeInTheDocument();
  });

  it("logs in and returns to the page asked for", async () => {
    const bodies: unknown[] = [];
    serve({
      login: (init) => {
        bodies.push(JSON.parse(String(init?.body)));
        return json(200, { result: { token: makeToken() } });
      },
    });
    renderLogin("/cores");

    await screen.findByRole("heading", { name: "Login" });
    expect(screen.getByRole("button", { name: "Login" })).toBeDisabled();
    fill("Email", "admin@example.com");
    fill("Password", "correct horse");
    fireEvent.click(screen.getByRole("button", { name: "Login" }));

    expect(await screen.findByText("cores page")).toBeInTheDocument();
    expect(bodies).toEqual([
      { email: "admin@example.com", password: "correct horse" },
    ]);
  });

  it("shows why a login failed", async () => {
    serve({
      login: () =>
        json(401, { error: "The email or password is incorrect. Try again." }),
    });
    renderLogin();

    await screen.findByRole("heading", { name: "Login" });
    fill("Email", "admin@example.com");
    fill("Password", "wrong");
    fireEvent.click(screen.getByRole("button", { name: "Login" }));

    expect(
      await screen.findByText("The email or password is incorrect. Try again."),
    ).toBeInTheDocument();
  });
});
