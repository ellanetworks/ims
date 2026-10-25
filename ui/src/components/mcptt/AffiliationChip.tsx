import { Chip, Tooltip } from "@mui/material";
import type { Affiliation } from "@/queries/mcptt";

const labels = {
  affiliated: "Affiliated",
  affiliating: "Affiliating",
  deaffiliating: "Leaving",
} as const;

// AffiliationChip is the affiliation of a user's phones to a group: the best status of them, how many phones, or
// none.
export default function AffiliationChip({
  affiliations,
}: {
  affiliations: Affiliation[];
}) {
  if (affiliations.length === 0) {
    return <Chip size="small" variant="outlined" label="Not affiliated" />;
  }

  const affiliated = affiliations.filter(
    (a) => a.status === "affiliated",
  ).length;
  const status = affiliated > 0 ? "affiliated" : affiliations[0].status;
  const phones =
    affiliations.length === 1 ? "1 phone" : `${affiliations.length} phones`;

  return (
    <Tooltip title={`On ${phones}`} arrow>
      <Chip
        size="small"
        color={status === "affiliated" ? "success" : "info"}
        label={labels[status]}
      />
    </Tooltip>
  );
}
