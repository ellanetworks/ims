import { useState } from "react";
import { Box } from "@mui/material";
import { useQuery } from "@tanstack/react-query";
import DomainName from "@/components/DomainName";
import EditNumberingDialog from "@/components/EditNumberingDialog";
import EditOperatorIdDialog from "@/components/EditOperatorIdDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import SettingsTable, { SubFields } from "@/components/SettingsTable";
import { getOperator } from "@/queries/operator";
import { homeDomain } from "@/utils/operator";

const orNone = (value: string) => value || "None";

export default function Operator() {
  const [editing, setEditing] = useState<"id" | "numbering" | null>(null);
  const { data, error, isPending } = useQuery({
    queryKey: ["operator"],
    queryFn: getOperator,
  });

  return (
    <Box component="section" aria-labelledby="operator-title">
      <PageHeader
        id="operator-title"
        title="Operator"
        description="Your network's identity and numbering plan."
      />
      <QueryAlert
        error={error}
        hasData={data !== undefined}
        subject="operator settings"
      />
      <SettingsTable
        label="Operator settings"
        loading={isPending}
        rows={[
          {
            label: "Operator ID (MCC/MNC)",
            help: "Must match your subscribers' IMSIs and your core.",
            value: data && `${data.mcc} / ${data.mnc}`,
            onEdit: () => setEditing("id"),
          },
          {
            label: "Home Network Domain",
            help: "Set by the Operator ID. Must match the ISIM of your SIMs, if they have one.",
            value: data && <DomainName name={homeDomain(data.mcc, data.mnc)} />,
          },
          {
            label: "Numbering Plan",
            help: "How the local numbers phones dial become international ones.",
            value: data && (
              <SubFields
                rows={[
                  ["Country Code", `+${data.numbering.country_code}`],
                  ["National Prefix", orNone(data.numbering.national_prefix)],
                  [
                    "International Prefix",
                    orNone(data.numbering.international_prefix),
                  ],
                ]}
              />
            ),
            onEdit: () => setEditing("numbering"),
          },
        ]}
      />
      {editing === "id" && data && (
        <EditOperatorIdDialog
          operator={data}
          onClose={() => setEditing(null)}
        />
      )}
      {editing === "numbering" && data && (
        <EditNumberingDialog operator={data} onClose={() => setEditing(null)} />
      )}
    </Box>
  );
}
