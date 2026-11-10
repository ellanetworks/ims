package db

func init() {
	register(cmdLeaderStart, handle(applyLeaderStart))
	register(cmdMigrate, handle(applyMigrate))
	register(cmdInitSecrets, handle(applyInitSecrets))
	register(cmdRegisterMember, handle(applyRegisterMember))
	register(cmdCreateJoinToken, handle(applyCreateJoinToken))
	register(cmdAdmitMember, handle(applyAdmitMember))
	register(cmdRemoveMember, handle(applyRemoveMember))
	register(cmdSetDisplayName, handle(applySetDisplayName))
	register(cmdDeleteExpiredJoinTokens, handle(applyDeleteExpiredJoinTokens))
	register(cmdSetDrainState, handle(applySetDrainState))
}
