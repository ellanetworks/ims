import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import Footer from "@/components/Footer";

describe("Footer", () => {
  it("shows the vendor and links to its website", () => {
    render(<Footer />);

    expect(screen.getByRole("contentinfo")).toHaveTextContent(
      "© 2026 Ella Networks Inc.·ellanetworks.com",
    );
    expect(
      screen.getByRole("link", { name: "ellanetworks.com" }),
    ).toHaveAttribute("href", "https://ellanetworks.com");
  });
});
