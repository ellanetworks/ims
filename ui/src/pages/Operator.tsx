import { useState } from "react";
import { Box } from "@mui/material";
import { useQuery } from "@tanstack/react-query";
import DomainName from "@/components/DomainName";
import EditHomeDomainDialog from "@/components/EditHomeDomainDialog";
import EditNumberingDialog from "@/components/EditNumberingDialog";
import EditOperatorIdDialog from "@/components/EditOperatorIdDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import SettingsTable, { SubFields } from "@/components/SettingsTable";
import { useAuth } from "@/contexts/AuthContext";
import { getOperator } from "@/queries/operator";
import { derivedDomain, homeDomain } from "@/utils/operator";

const orNA = (value: string) => value || "N/A";

export default function Operator() {
  const auth = useAuth();
  const editable = auth.can("operator:update");
  const [editing, setEditing] = useState<"id" | "domain" | "numbering" | null>(
    null,
  );
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
            help: "Mobile Country Code and Mobile Network Code of your network. They set this IMS's Diameter host and realm, and its home network domain unless you set one.",
            value: data && `${data.mcc} / ${data.mnc}`,
            onEdit: editable ? () => setEditing("id") : undefined,
          },
          {
            label: "Home Network Domain",
            help: "Derived from the Operator ID unless you set one. Must match your HSS, and the ISIM of your SIMs if they have one. Phones without an ISIM always use the derived domain, which stays accepted.",
            value: data && (
              <SubFields
                rows={[
                  [
                    "Domain",
                    <DomainName key="domain" name={homeDomain(data)} />,
                  ],
                  [
                    "Also Accepted",
                    data.domain &&
                    data.domain !== derivedDomain(data.mcc, data.mnc) ? (
                      <DomainName
                        key="derived"
                        name={derivedDomain(data.mcc, data.mnc)}
                      />
                    ) : (
                      "N/A"
                    ),
                  ],
                ]}
              />
            ),
            onEdit: editable ? () => setEditing("domain") : undefined,
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
                  [
                    "Minimum National Digits",
                    data.numbering.min_national_digits,
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
      {editing === "domain" && data && (
        <EditHomeDomainDialog
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
