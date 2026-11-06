import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { returnPath } from "@/utils/returnPath";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import Login from "@/pages/Login";
import { makeToken } from "@/test/jwt";
import { json, renderWithClient, stubApi } from "@/test/render";

const serve = ({
  initialized = true,
  session = false,
  login = () => json(200, { result: { token: makeToken() } }),
  oidc = { enabled: false },
}: {
  initialized?: boolean;
  session?: boolean;
  login?: (init?: RequestInit) => Response;
  oidc?: { enabled: boolean; display_name?: string };
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
        "/api/v1/auth/oidc": () => json(200, { result: oidc }),
      }),
    ),
  );

function Search() {
  return <output aria-label="search">{useLocation().search}</output>;
}

const renderLogin = (from?: string, search = "") =>
  renderWithClient(
    <MemoryRouter
      initialEntries={[
        { pathname: "/login", search, state: from ? { from } : null },
      ]}
    >
      <Routes>
        <Route
          path="/login"
          element={
            <>
              <Login />
              <Search />
            </>
          }
        />
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
  sessionStorage.clear();
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

  it("offers single sign-on when it is on", async () => {
    serve({ oidc: { enabled: true, display_name: "Keycloak" } });
    renderLogin();

    const button = await screen.findByRole("link", {
      name: "Log in with Keycloak",
    });
    expect(button).toHaveAttribute("href", "/api/v1/auth/oidc/login");
    expect(button).toHaveClass("MuiButton-contained");
    expect(screen.getByRole("button", { name: "Login" })).toHaveClass(
      "MuiButton-outlined",
    );
  });

  it("offers only the password without single sign-on", async () => {
    serve();
    renderLogin();

    await screen.findByRole("heading", { name: "Login" });
    expect(
      screen.queryByRole("link", { name: /Log in with/ }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Login" })).toHaveClass(
      "MuiButton-contained",
    );
  });

  it("says why single sign-on failed", async () => {
    serve({ oidc: { enabled: true, display_name: "Keycloak" } });
    renderLogin(undefined, "?error=not_set_up&x=1");

    expect(
      await screen.findByText(
        "Your account has not been set up in Ella IMS. Contact an administrator.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "search" })).toHaveTextContent(
      /^\?x=1$/,
    );
  });

  it("gives a general message for an unknown single sign-on error", async () => {
    serve();
    renderLogin(undefined, "?error=something_new");

    expect(
      await screen.findByText(
        "Single sign-on failed. Try again, or contact an administrator.",
      ),
    ).toBeInTheDocument();
  });

  it.each(["__proto__", "constructor", "toString", "hasOwnProperty"])(
    "gives the general message for the error %s",
    async (code) => {
      serve({ oidc: { enabled: true, display_name: "Keycloak" } });
      renderLogin(undefined, `?error=${code}`);

      expect(
        await screen.findByText(
          "Single sign-on failed. Try again, or contact an administrator.",
        ),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("link", { name: "Log in with Keycloak" }),
      ).toBeInTheDocument();
    },
  );

  it("focuses single sign-on when it is on", async () => {
    serve({ oidc: { enabled: true, display_name: "Keycloak" } });
    renderLogin();

    expect(
      await screen.findByRole("link", { name: "Log in with Keycloak" }),
    ).toHaveFocus();
  });

  it("focuses the email without single sign-on", async () => {
    serve();
    renderLogin();

    await screen.findByRole("heading", { name: "Login" });
    expect(
      screen.getByLabelText("Email", { exact: false, selector: "input" }),
    ).toHaveFocus();
  });

  it("remembers the page asked for across single sign-on", async () => {
    serve({ oidc: { enabled: true, display_name: "Keycloak" } });
    renderLogin("/cores");

    const link = await screen.findByRole("link", {
      name: "Log in with Keycloak",
    });
    link.addEventListener("click", (e) => e.preventDefault());
    fireEvent.click(link);

    expect(returnPath()).toBe("/cores");
  });

  it("returns to the page asked for after a failed single sign-on", async () => {
    sessionStorage.setItem(
      "ims.returnPath",
      JSON.stringify({ path: "/cores", at: Date.now() }),
    );
    serve({ oidc: { enabled: true, display_name: "Keycloak" } });
    renderLogin(undefined, "?error=denied");

    await screen.findByRole("heading", { name: "Login" });
    fill("Email", "admin@example.com");
    fill("Password", "correct horse");
    fireEvent.click(screen.getByRole("button", { name: "Login" }));

    expect(await screen.findByText("cores page")).toBeInTheDocument();
    expect(returnPath()).toBeUndefined();
  });
});
