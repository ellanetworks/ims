import { Box, Container, Link, Typography } from "@mui/material";
import { MAX_WIDTH } from "@/utils/layout";

const VENDOR = {
  company: "Ella Networks Inc.",
  websiteUrl: "https://ellanetworks.com",
};

export default function Footer() {
  return (
    <Box
      component="footer"
      sx={{
        mt: "auto",
        borderTop: 1,
        borderColor: "divider",
        py: 2,
        bgcolor: "background.paper",
      }}
    >
      <Container maxWidth={false} sx={{ maxWidth: MAX_WIDTH }}>
        <Typography
          variant="body2"
          color="textSecondary"
          sx={{
            display: "flex",
            flexWrap: "wrap",
            alignItems: "center",
            gap: "6px",
          }}
        >
          © 2026 {VENDOR.company}
          <span>·</span>
          <Link
            href={VENDOR.websiteUrl}
            target="_blank"
            rel="noopener noreferrer"
            color="textSecondary"
            underline="hover"
          >
            {VENDOR.websiteUrl.replace("https://", "")}
          </Link>
        </Typography>
      </Container>
    </Box>
  );
}
