import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import Logo from "@/components/Logo";

describe("Logo", () => {
  it("renders the mark", () => {
    render(<Logo />);
    expect(screen.getByRole("img", { name: "Ella IMS Logo" })).toHaveAttribute(
      "src",
      "/logo-mark.svg",
    );
  });
});
