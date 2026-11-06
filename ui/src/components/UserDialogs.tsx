import { useState } from "react";
import {
  Checkbox,
  FormControlLabel,
  FormHelperText,
  Switch,
  TextField,
} from "@mui/material";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import PasswordField from "@/components/PasswordField";
import RoleField from "@/components/RoleField";
import { getOIDC, roleFromGroups } from "@/queries/oidc";
import {
  createUser,
  passwordError,
  RoleID,
  updateMyPassword,
  updateUserPassword,
  updateUserRole,
  type User,
} from "@/queries/users";

const EMAIL = /^[^\s@]+@[^\s@]+$/;

const useOIDC = () => useQuery({ queryKey: ["oidc"], queryFn: getOIDC });

function PinRoleField({
  checked,
  onChange,
  required = false,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  required?: boolean;
}) {
  return (
    <div>
      <FormControlLabel
        control={
          <Checkbox
            checked={checked}
            disabled={required}
            onChange={(e) => onChange(e.target.checked)}
            slotProps={{ input: { "aria-describedby": "pin-role-help" } }}
          />
        }
        label="Pin role"
      />
      <FormHelperText id="pin-role-help" sx={{ mt: 0 }}>
        {required
          ? "Pinned, or groups would replace it at the next login."
          : "A pinned role is not updated from the user's groups."}
      </FormHelperText>
    </div>
  );
}

export function CreateUserDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState(RoleID.ReadOnly);
  const [pinned, setPinned] = useState(false);
  const [withPassword, setWithPassword] = useState(true);
  const oidc = useOIDC().data;
  const mapped = (oidc?.group_roles.length ?? 0) > 0;
  const ssoConfigured =
    oidc !== undefined && oidc.issuer !== "" && oidc.client_id !== "";
  const needsPassword = !ssoConfigured || withPassword;

  const mutation = useMutation({
    mutationFn: () =>
      createUser(
        email,
        needsPassword ? password : "",
        role,
        mapped ? pinned : undefined,
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["users"] });
      showSnackbar("User created successfully.", "success");
      onClose();
    },
  });

  const emailError =
    email === "" || EMAIL.test(email) ? undefined : "Enter an email address.";
  const newPasswordError =
    password === "" ? undefined : passwordError(password);

  return (
    <EditDialog
      title="Create User"
      valid={
        EMAIL.test(email) &&
        (!needsPassword || passwordError(password) === undefined)
      }
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
      submitLabel="Create"
      pendingLabel="Creating…"
      failure="Could not create the user"
    >
      <TextField
        label="Email"
        type="email"
        value={email}
        onChange={(e) => setEmail(e.target.value.trim())}
        error={emailError !== undefined}
        helperText={emailError}
        required
        autoFocus
      />
      {ssoConfigured && (
        <FormControlLabel
          control={
            <Switch
              checked={withPassword}
              onChange={(e) => setWithPassword(e.target.checked)}
            />
          }
          label="Password"
        />
      )}
      {needsPassword && (
        <PasswordField
          label="Password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          error={newPasswordError !== undefined}
          helperText={newPasswordError ?? "8 to 72 characters."}
          required
          autoComplete="new-password"
        />
      )}
      <RoleField value={role} onChange={setRole} />
      {mapped && <PinRoleField checked={pinned} onChange={setPinned} />}
    </EditDialog>
  );
}

export function EditRoleDialog({
  user,
  onClose,
}: {
  user: User;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [role, setRole] = useState(user.role_id);
  const [chosenPin, setPinned] = useState(user.role_pinned);
  const oidc = useOIDC();
  const pinnable = (oidc.data?.group_roles.length ?? 0) > 0 || user.role_pinned;
  const pinRequired = roleFromGroups(user, oidc.data) && role !== user.role_id;
  const pinned = chosenPin || pinRequired;

  const mutation = useMutation({
    mutationFn: () =>
      updateUserRole(user.email, role, pinnable ? pinned : undefined),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["users"] });
      showSnackbar("User updated successfully.", "success");
      onClose();
    },
  });

  return (
    <EditDialog
      title="Edit Role"
      valid={role !== user.role_id || pinned !== user.role_pinned}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
    >
      <TextField label="Email" value={user.email} disabled />
      <RoleField value={role} onChange={setRole} />
      {pinnable && (
        <PinRoleField
          checked={pinned}
          onChange={setPinned}
          required={pinRequired}
        />
      )}
    </EditDialog>
  );
}

export function PasswordDialog({
  email,
  onClose,
}: {
  email?: string;
  onClose: () => void;
}) {
  const { showSnackbar } = useSnackbar();
  const [current, setCurrent] = useState("");
  const [password, setPassword] = useState("");

  const mutation = useMutation({
    mutationFn: () =>
      email === undefined
        ? updateMyPassword(current, password)
        : updateUserPassword(email, password),
    onSuccess: () => {
      showSnackbar("Password updated successfully.", "success");
      onClose();
    },
  });

  const newPasswordError =
    password === "" ? undefined : passwordError(password);

  return (
    <EditDialog
      title="Change Password"
      valid={
        (email !== undefined || current !== "") &&
        passwordError(password) === undefined
      }
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
      failure="Could not change the password"
    >
      {email === undefined && (
        <PasswordField
          label="Current password"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          required
          autoFocus
          autoComplete="current-password"
        />
      )}
      <PasswordField
        label="New password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        error={newPasswordError !== undefined}
        helperText={
          newPasswordError ??
          (email === undefined
            ? "8 to 72 characters. Your other sessions end."
            : "8 to 72 characters. The user's sessions end.")
        }
        required
        autoFocus={email !== undefined}
        autoComplete="new-password"
      />
    </EditDialog>
  );
}
