import { Stack } from "@mui/material";
import CopyButton from "@/components/CopyButton";
import DomainName from "@/components/DomainName";

export default function Copyable({
  value,
  label,
}: {
  value: string;
  label: string;
}) {
  return (
    <Stack direction="row" sx={{ alignItems: "center", gap: 0.5 }}>
      <span>
        <DomainName name={value} />
      </span>
      <CopyButton value={value} label={label} />
    </Stack>
  );
}
