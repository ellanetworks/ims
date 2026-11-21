# Push-to-Talk

Mission Critical Push To Talk (MCPTT) is the 3GPP standard for group calls where one member talks at a time and the others listen. Ella IMS includes an MCPTT server, which runs in the same process as the CSCFs. Unlike a regular call, a group call's voice goes through Ella IMS, which copies each talker's audio to every listener.

## Users and groups

A "user" is a person, such as `alice`. A "group" gathers users who talk together, such as a team.

A user hears and talks in a group's calls once their app has joined the group.
