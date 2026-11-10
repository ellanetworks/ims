import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import Initialize from "@/pages/Initialize";
import { makeToken } from "@/test/jwt";
import { json, renderWithClient, stubApi } from "@/test/render";

const serve = (
  initialized: boolean,
  bodies: unknown[] = [],
  clustered = false,
) =>
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/status": () =>
          json(200, {
            result: { version: "v0.0.1", revision: "", initialized, clustered },
          }),
        "/api/v1/init": (_url, init) => {
          bodies.push(JSON.parse(String(init?.body)));
          return json(201, { result: { token: makeToken() } });
        },
      }),
    ),
  );

const renderInitialize = () =>
  renderWithClient(
    <MemoryRouter initialEntries={["/initialize"]}>
      <Routes>
        <Route path="/initialize" element={<Initialize />} />
        <Route path="/login" element={<p>login page</p>} />
        <Route path="/initialize/join" element={<p>join page</p>} />
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

describe("Initialize", () => {
  it("sends an initialized system to the login page", async () => {
    serve(true);
    renderInitialize();

    expect(await screen.findByText("login page")).toBeInTheDocument();
  });

  it("creates the first user", async () => {
    const bodies: unknown[] = [];
    serve(false, bodies);
    renderInitialize();

    await screen.findByRole("heading", { name: "Initialize Ella IMS" });
    fill("Email", "admin@example.com");
    fill("Password", "short");
    const create = screen.getByRole("button", { name: "Create" });
    expect(create).toBeDisabled();

    fill("Password", "correct horse");
    fireEvent.click(create);

    expect(await screen.findByText("home page")).toBeInTheDocument();
    expect(bodies).toEqual([
      { email: "admin@example.com", password: "correct horse" },
    ]);
  });

  it("offers to join a cluster when the node has a cluster address", async () => {
    serve(false, [], true);
    renderInitialize();

    const join = await screen.findByRole("button", {
      name: "Join an existing cluster instead",
    });
    expect(join).toBeEnabled();
    fireEvent.click(join);

    expect(await screen.findByText("join page")).toBeInTheDocument();
  });

  it("does not offer to join a cluster without a cluster address", async () => {
    serve(false);
    renderInitialize();

    expect(
      await screen.findByRole("button", {
        name: "Join an existing cluster instead",
      }),
    ).toBeDisabled();
  });

  it("shows the formation progress until the node is a member", async () => {
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/status": () => {
            calls += 1;
            const formation =
              calls < 3
                ? {
                    state: "forming",
                    expected: 3,
                    found: 2,
                    detail: "found 2 of 3 nodes",
                  }
                : { state: "member" };
            return json(200, {
              result: {
                version: "v0.0.1",
                revision: "",
                initialized: false,
                clustered: true,
                formation,
              },
            });
          },
        }),
      ),
    );
    renderInitialize();

    expect(
      await screen.findByRole("heading", { name: "Forming 2/3" }),
    ).toBeInTheDocument();
    expect(screen.getByText("found 2 of 3 nodes")).toBeInTheDocument();

    expect(
      await screen.findByRole(
        "heading",
        { name: "Initialize Ella IMS" },
        { timeout: 5000 },
      ),
    ).toBeInTheDocument();
  });

  it("explains that a removed node is fenced", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/status": () =>
            json(200, {
              result: {
                version: "v0.0.1",
                revision: "",
                initialized: false,
                clustered: true,
                formation: {
                  state: "removed",
                  detail: "delete its data directory to join it again",
                },
              },
            }),
        }),
      ),
    );
    renderInitialize();

    expect(
      await screen.findByRole("heading", { name: "Removed from the cluster" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("delete its data directory to join it again"),
    ).toBeInTheDocument();
  });
});
