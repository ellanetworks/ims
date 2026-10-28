import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import Initialize from "@/pages/Initialize";
import { makeToken } from "@/test/jwt";
import { json, renderWithClient, stubApi } from "@/test/render";

const serve = (initialized: boolean, bodies: unknown[] = []) =>
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/status": () =>
          json(200, {
            result: { version: "v0.0.1", revision: "", initialized },
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
});
