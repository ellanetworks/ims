import { ListItemText, MenuItem, TextField } from "@mui/material";
import { RoleID, roleDescriptions, roleLabels } from "@/queries/users";

const roles = [RoleID.Admin, RoleID.NetworkManager, RoleID.ReadOnly];

export default function RoleField({
  value,
  onChange,
  fullWidth,
}: {
  value: RoleID;
  onChange: (role: RoleID) => void;
  fullWidth?: boolean;
}) {
  return (
    <TextField
      select
      label="Role"
      value={value}
      onChange={(e) => onChange(Number(e.target.value) as RoleID)}
      helperText={roleDescriptions[value]}
      required
      fullWidth={fullWidth}
      slotProps={{
        select: { renderValue: (v) => roleLabels[v as RoleID] },
      }}
    >
      {roles.map((role) => (
        <MenuItem key={role} value={role}>
          <ListItemText
            primary={roleLabels[role]}
            secondary={roleDescriptions[role]}
          />
        </MenuItem>
      ))}
    </TextField>
  );
}
