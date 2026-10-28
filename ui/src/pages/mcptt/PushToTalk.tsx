import { Box, Tab, Tabs } from "@mui/material";
import { Link, Navigate, useParams } from "react-router-dom";
import MCPTTGate from "@/components/MCPTTGate";
import PageHeader from "@/components/PageHeader";
import { CallsTab } from "@/pages/mcptt/GroupCalls";
import { GroupsTab } from "@/pages/mcptt/Groups";
import { SettingsTab } from "@/pages/mcptt/Settings";
import { UsersTab } from "@/pages/mcptt/Users";
import type { MCPTTStatus } from "@/queries/mcptt";

// TABS are the tabs of the page, each of its path: groups first, what operators think in.
export const TABS = ["groups", "users", "calls", "settings"] as const;

type TabName = (typeof TABS)[number];

function PushToTalkTabs({
  tab,
  status,
}: {
  tab: TabName;
  status: MCPTTStatus;
}) {
  return (
    <>
      <Box sx={{ borderBottom: 1, borderColor: "divider", mb: 3 }}>
        <Tabs value={tab} aria-label="Push-to-talk">
          <Tab
            value="groups"
            label="Groups"
            component={Link}
            to="/ptt/groups"
          />
          <Tab value="users" label="Users" component={Link} to="/ptt/users" />
          <Tab value="calls" label="Calls" component={Link} to="/ptt/calls" />
          <Tab
            value="settings"
            label="Settings"
            component={Link}
            to="/ptt/settings"
          />
        </Tabs>
      </Box>
      <Box role="tabpanel" aria-label={tab}>
        {tab === "groups" && <GroupsTab />}
        {tab === "users" && <UsersTab />}
        {tab === "calls" && <CallsTab />}
        {tab === "settings" && <SettingsTab status={status} />}
      </Box>
    </>
  );
}

export default function PushToTalk() {
  const { tab } = useParams();

  if (!TABS.includes(tab as TabName)) {
    return <Navigate to="/ptt/groups" replace />;
  }

  return (
    <Box component="section" aria-labelledby="ptt-title">
      <PageHeader
        id="ptt-title"
        title="Push-to-Talk"
        description="Mission critical push-to-talk (MCPTT) served by this IMS."
      />
      <MCPTTGate>
        {(status) => <PushToTalkTabs tab={tab as TabName} status={status} />}
      </MCPTTGate>
    </Box>
  );
}
