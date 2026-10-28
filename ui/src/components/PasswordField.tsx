import { useState } from "react";
import {
  IconButton,
  InputAdornment,
  TextField,
  type TextFieldProps,
} from "@mui/material";
import { Visibility, VisibilityOff } from "@mui/icons-material";

// PasswordField is a password input that can show what it holds.
export default function PasswordField({ slotProps, ...props }: TextFieldProps) {
  const [shown, setShown] = useState(false);

  return (
    <TextField
      {...props}
      type={shown ? "text" : "password"}
      slotProps={{
        ...slotProps,
        input: {
          ...(slotProps?.input as object | undefined),
          endAdornment: (
            <InputAdornment position="end">
              <IconButton
                aria-label={shown ? "Hide password" : "Show password"}
                onClick={() => setShown((s) => !s)}
                edge="end"
              >
                {shown ? <VisibilityOff /> : <Visibility />}
              </IconButton>
            </InputAdornment>
          ),
        },
      }}
    />
  );
}
