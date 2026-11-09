import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import Operator from "@/pages/Operator";
import type { Operator as OperatorSettings } from "@/queries/operator";
import { json, readOnlyAuth, renderWithClient, stubApi } from "@/test/render";

const operator: OperatorSettings = {
  mcc: "001",
  mnc: "01",
  domain: "",
  numbering: {
    country_code: "1",
    national_prefix: "",
    international_prefix: "",
    min_national_digits: 7,
  },
};

const serve = (put: (body: OperatorSettings) => Response) => {
  const bodies: OperatorSettings[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/operator": (_url, init) => {
          if (init?.method !== "PUT") return json(200, { result: operator });
          const body = JSON.parse(String(init.body)) as OperatorSettings;
          bodies.push(body);
          return put(body);
        },
      }),
    ),
  );
  return bodies;
};

const rows = () =>
  Array.from(
    screen
      .getByRole("table", { name: "Operator settings" })
      .querySelectorAll(":scope > tbody > tr"),
  ).map((row) => row.querySelector("td")?.nextElementSibling?.textContent);

const fill = (label: string, value: string) =>
  fireEvent.change(screen.getByRole("textbox", { name: label }), {
    target: { value },
  });

const loaded = () => screen.findByText("001 / 01");

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Operator", () => {
  it("offers no edit to a Read Only user", async () => {
    serve(() => json(500, {}));
    renderWithClient(<Operator />, readOnlyAuth);

    expect(await screen.findByText("001 / 01")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^Edit / }),
    ).not.toBeInTheDocument();
  });

  it("shows the operator settings", async () => {
    serve(() => json(500, {}));

    renderWithClient(<Operator />);

    await loaded();
    expect(rows()).toEqual([
      "001 / 01",
      "Domainims.mnc001.mcc001.3gppnetwork.orgAlso AcceptedN/A",
      "Country Code1National PrefixN/AInternational PrefixN/AMinimum National Digits7",
    ]);
  });

  it("explains each setting on hover", async () => {
    serve(() => json(500, {}));

    renderWithClient(<Operator />);
    await loaded();

    fireEvent.mouseOver(screen.getByText("Operator ID (MCC/MNC)"));
    expect(
      await screen.findByRole("tooltip", {
        name: "Mobile Country Code and Mobile Network Code of your network. They set this IMS's Diameter host and realm, and its home network domain unless you set one.",
      }),
    ).toBeInTheDocument();
  });

  it("updates the operator ID and the home network domain", async () => {
    const bodies = serve((body) => json(200, { result: body }));

    renderWithClient(<Operator />);
    await loaded();
    fireEvent.click(
      screen.getByRole("button", { name: "Edit Operator ID (MCC/MNC)" }),
    );

    expect(screen.queryByText(/restart/)).not.toBeInTheDocument();

    fill("MNC", "1");
    expect(screen.getByText("2 or 3 digits")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();

    fill("MCC", "208");
    fill("MNC", "10");
    expect(screen.getByText(/restart/)).toHaveTextContent(
      "The home network domain becomes ims.mnc010.mcc208.3gppnetwork.org. Diameter and SIP restart: phones re-register, and calls in progress lose their QoS.",
    );
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(bodies).toEqual([{ ...operator, mcc: "208", mnc: "10" }]);
    expect(rows().slice(0, 2)).toEqual([
      "208 / 10",
      "Domainims.mnc010.mcc208.3gppnetwork.orgAlso AcceptedN/A",
    ]);
  });

  it("does not warn when the home network domain stays the same", async () => {
    serve((body) => json(200, { result: body }));

    renderWithClient(<Operator />);
    await loaded();
    fireEvent.click(
      screen.getByRole("button", { name: "Edit Operator ID (MCC/MNC)" }),
    );

    fill("MNC", "001");

    expect(screen.queryByText(/restart/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update" })).toBeEnabled();
  });

  it("sets the home network domain", async () => {
    const bodies = serve((body) => json(200, { result: body }));

    renderWithClient(<Operator />);
    await loaded();
    fireEvent.click(
      screen.getByRole("button", { name: "Edit Home Network Domain" }),
    );

    expect(screen.getByRole("textbox", { name: "Domain" })).toHaveAttribute(
      "placeholder",
      "ims.mnc001.mcc001.3gppnetwork.org",
    );
    expect(screen.queryByText(/restart/)).not.toBeInTheDocument();

    fill("Domain", "localhost");
    expect(
      screen.getByText("At least two labels, like ims.example.com"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();

    fill("Domain", "IMS.Example.com.");
    expect(screen.getByText(/restart/)).toHaveTextContent(
      "The home network domain becomes ims.example.com, and the MCPTT and IP-SM-GW are renamed under it. Phones without an ISIM keep using ims.mnc001.mcc001.3gppnetwork.org. Diameter and SIP restart: phones re-register, and calls in progress lose their QoS.",
    );
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(bodies).toEqual([{ ...operator, domain: "ims.example.com" }]);
    expect(rows()[1]).toBe(
      "Domainims.example.comAlso Acceptedims.mnc001.mcc001.3gppnetwork.org",
    );
  });

  it("warns of the derived domain changing under a home network domain set", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/operator": () =>
            json(200, { result: { ...operator, domain: "ims.example.com" } }),
        }),
      ),
    );

    renderWithClient(<Operator />);
    await loaded();
    fireEvent.click(
      screen.getByRole("button", { name: "Edit Operator ID (MCC/MNC)" }),
    );
    fill("MNC", "02");

    expect(screen.getByText(/restart/)).toHaveTextContent(
      "The Diameter realm and the domain derived from the Operator ID become ims.mnc002.mcc001.3gppnetwork.org.",
    );
  });

  it("updates the numbering plan", async () => {
    const bodies = serve((body) => json(200, { result: body }));

    renderWithClient(<Operator />);
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Edit Numbering" }));

    fill("Country Code", "33");
    fill("National Prefix", "0");
    fill("Country Code", "033");
    expect(
      screen.getByText("1 to 3 digits, not starting with 0"),
    ).toBeInTheDocument();
    fill("Country Code", "33");
    fill("International Prefix", "+");
    expect(screen.getByText("Up to 4 digits")).toBeInTheDocument();
    fill("International Prefix", "0");
    expect(
      screen.getByText("Must differ from the national prefix"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();
    fill("International Prefix", "00");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toEqual({
      mcc: "001",
      mnc: "01",
      domain: "",
      numbering: {
        country_code: "33",
        national_prefix: "0",
        international_prefix: "00",
        min_national_digits: 7,
      },
    });
  });

  it("keeps and sets the minimum national digits", async () => {
    const bodies = serve((body) => json(200, { result: body }));

    renderWithClient(<Operator />);
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Edit Numbering" }));

    expect(
      screen.getByRole("textbox", { name: "Minimum National Digits" }),
    ).toHaveValue("7");
    fill("Minimum National Digits", "16");
    expect(screen.getByText("0 to 15")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update" })).toBeDisabled();
    fill("Minimum National Digits", "0");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0].numbering).toEqual({
      ...operator.numbering,
      min_national_digits: 0,
    });
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(rows()[2]).toContain("Minimum National Digits0");
  });

  it("shows the server error and keeps the form", async () => {
    serve(() =>
      json(400, { error: "numbering.country_code must be 1 to 3 digits" }),
    );

    renderWithClient(<Operator />);
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Edit Numbering" }));
    fill("Country Code", "33");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not update: numbering.country_code must be 1 to 3 digits",
    );
    expect(screen.getByRole("textbox", { name: "Country Code" })).toHaveValue(
      "33",
    );
  });
});
