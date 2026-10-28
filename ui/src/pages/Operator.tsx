import { useState } from "react";
import { Box } from "@mui/material";
import { useQuery } from "@tanstack/react-query";
import DomainName from "@/components/DomainName";
import EditNumberingDialog from "@/components/EditNumberingDialog";
import EditOperatorIdDialog from "@/components/EditOperatorIdDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import SettingsTable, { SubFields } from "@/components/SettingsTable";
import { useAuth } from "@/contexts/AuthContext";
import { getOperator } from "@/queries/operator";
import { homeDomain } from "@/utils/operator";

const orNA = (value: string) => value || "N/A";

export default function Operator() {
  const auth = useAuth();
  const editable = auth.can("operator:update");
  const [editing, setEditing] = useState<"id" | "numbering" | null>(null);
  const operator = useQuery({
    queryKey: ["operator"],
    queryFn: getOperator,
  });
  const { data, isPending } = operator;

  return (
    <Box component="section" aria-labelledby="operator-title">
      <PageHeader
        id="operator-title"
        title="Operator"
        description="Your network's identity and numbering plan."
      />
      <QueryAlert query={operator} subject="operator settings" />
      <SettingsTable
        label="Operator settings"
        loading={isPending}
        rows={[
          {
            label: "Operator ID (MCC/MNC)",
            help: "Mobile Country Code and Mobile Network Code of your network. They set this IMS's home network domain, Diameter host and realm.",
            value: data && `${data.mcc} / ${data.mnc}`,
            onEdit: editable ? () => setEditing("id") : undefined,
          },
          {
            label: "Home Network Domain",
            help: "Set by the Operator ID. Must match the ISIM of your SIMs, if they have one.",
            value: data && <DomainName name={homeDomain(data.mcc, data.mnc)} />,
          },
          {
            label: "Numbering",
            help: "Used to turn the national numbers phones send into international ones.",
            value: data && (
              <SubFields
                rows={[
                  ["Country Code", data.numbering.country_code],
                  ["National Prefix", orNA(data.numbering.national_prefix)],
                  [
                    "International Prefix",
                    orNA(data.numbering.international_prefix),
                  ],
                ]}
              />
            ),
            onEdit: editable ? () => setEditing("numbering") : undefined,
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
