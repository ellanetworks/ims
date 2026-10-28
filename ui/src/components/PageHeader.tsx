import { useEffect, type ReactNode } from "react";
import { Box, Typography } from "@mui/material";
import { Link as RouterLink } from "react-router-dom";

interface PageHeaderProps {
  id: string;
  title: string;
  count?: number;
  description?: string;
  action?: ReactNode;
  // parent is the page this one is under, which the title leads with, as in Ella Core: "Users / admin@example.com".
  parent?: { label: string; to: string };
}

export default function PageHeader({
  id,
  title,
  count,
  description,
  action,
  parent,
}: PageHeaderProps) {
  const name = parent ? `${parent.label} / ${title}` : title;

  useEffect(() => {
    document.title = `${name} · Ella IMS`;
  }, [name]);

  return (
    <Box sx={{ mb: 3 }}>
      <Box
        sx={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          gap: 2,
        }}
      >
        {parent ? (
          <Typography
            id={id}
            variant="h4"
            component="h1"
            sx={{ display: "flex", alignItems: "baseline", flexWrap: "wrap" }}
          >
            <Typography
              component={RouterLink}
              to={parent.to}
              variant="h4"
              sx={{
                color: "text.secondary",
                textDecoration: "none",
                "&:hover": { textDecoration: "underline" },
              }}
            >
              {parent.label}
            </Typography>
            <Typography
              component="span"
              variant="h4"
              sx={{ color: "text.secondary", mx: 1 }}
            >
              /
            </Typography>
            <Typography
              component="span"
              variant="h4"
              sx={{ overflowWrap: "anywhere" }}
            >
              {title}
            </Typography>
          </Typography>
        ) : (
          <Typography id={id} variant="h4" component="h1">
            {count === undefined ? title : `${title} (${count})`}
          </Typography>
        )}
        {action}
      </Box>
      {description && (
        <Typography variant="body1" color="textSecondary" sx={{ mt: 1 }}>
          {description}
        </Typography>
      )}
    </Box>
  );
}
