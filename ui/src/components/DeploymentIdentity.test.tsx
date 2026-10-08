import { afterEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import DeploymentIdentity from "@/components/DeploymentIdentity";
import { json, renderWithClient, stubApi } from "@/test/render";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("DeploymentIdentity", () => {
  it("shows the version", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/status": () =>
            json(200, { result: { version: "v0.0.1", revision: "85faf74" } }),
        }),
      ),
    );

    renderWithClient(<DeploymentIdentity />);

    expect(await screen.findByText("v0.0.1")).toBeInTheDocument();
    expect(screen.queryByText("85faf74")).not.toBeInTheDocument();
  });

  it("shows nothing until the status loads", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>(() => {})),
    );

    const { container } = renderWithClient(<DeploymentIdentity />);

    expect(container).toBeEmptyDOMElement();
  });
});
