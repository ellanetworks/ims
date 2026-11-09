package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
	"github.com/ellanetworks/ims/internal/settings"
	"github.com/mattn/go-sqlite3"
)

type CommandType uint16

const (
	cmdLeaderStart CommandType = iota + 1
	cmdMigrate
	cmdInitSecrets
	cmdRegisterMember
	cmdCreateJoinToken
	cmdAdmitMember
	cmdRemoveMember
	cmdSetDisplayName
	cmdDeleteExpiredJoinTokens
	cmdSetDrainState
	cmdCreateUser
	cmdUpdateUserRole
	cmdUpdateUserPassword
	cmdDeleteUser
	cmdCreateSession
	cmdDeleteSession
	cmdDeleteExpiredSessions
	cmdCreateAPIToken
	cmdDeleteAPIToken
	cmdTouchAPIToken
	cmdUpdateOIDC
	cmdLogInWithOIDC
	cmdUseOIDCState
	cmdCreateIdentity
	cmdDeleteIdentities
	cmdUpdateOperator
	cmdCreatePeer
	cmdUpdatePeer
	cmdDeletePeer
	cmdUpdateRoute
	cmdUpdatePolicy
	cmdUpdateCallRecords
	cmdUpdateIPSMGW
	cmdCreateSMSRoute
	cmdUpdateSMSRoute
	cmdDeleteSMSRoute
	cmdCreateTrunk
	cmdUpdateTrunk
	cmdDeleteTrunk
	cmdCreateSIPRoute
	cmdUpdateSIPRoute
	cmdDeleteSIPRoute
	cmdUpdateMCPTTServiceConfiguration
	cmdCreateMCPTTUser
	cmdUpdateMCPTTUser
	cmdDeleteMCPTTUser
	cmdCreateMCPTTGroup
	cmdUpdateMCPTTGroup
	cmdDeleteMCPTTGroup
	cmdPutMCPTTMember
	cmdDeleteMCPTTMember
	cmdSaveCallRecords
	cmdPruneCallRecords
	cmdCloseOpenCallRecords
	cmdPutMCPTTAffiliation
	cmdClaimMCPTTGroupCall
	cmdReleaseMCPTTGroupCall
	cmdReleaseMCPTTGroupCallsOf
)

const entryBatch uint16 = 1

type RequestID [16]byte

type Command struct {
	Type      CommandType     `json:"t"`
	Version   uint16          `json:"v"`
	RequestID RequestID       `json:"r"`
	Retryable bool            `json:"x,omitempty"`
	Payload   json.RawMessage `json:"p"`
}

type Batch struct {
	LeaderTime int64      `json:"l"`
	Commands   []Command  `json:"c"`
	Check      *HashCheck `json:"h,omitempty"`
}

type HashCheck struct {
	Index uint64 `json:"i"`
	Hash  []byte `json:"h"`
}

type Result struct {
	Value    json.RawMessage `json:"v,omitempty"`
	Code     string          `json:"c,omitempty"`
	Message  string          `json:"m,omitempty"`
	Replayed bool            `json:"-"`
}

func EncodeBatch(b Batch) ([]byte, error) {
	body, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("encode batch: %w", err)
	}

	entry := make([]byte, 2, 2+len(body))
	binary.BigEndian.PutUint16(entry, entryBatch)

	return append(entry, body...), nil
}

func DecodeBatch(entry []byte) (Batch, error) {
	if len(entry) < 2 {
		return Batch{}, errors.New("decode batch: entry too short")
	}

	if t := binary.BigEndian.Uint16(entry); t != entryBatch {
		return Batch{}, fmt.Errorf("decode batch: unknown entry type %d", t)
	}

	var b Batch
	if err := json.Unmarshal(entry[2:], &b); err != nil {
		return Batch{}, fmt.Errorf("decode batch: %w", err)
	}

	return b, nil
}

var (
	ErrConstraint           = errors.New("constraint violated")
	ErrConstraintCheck      = fmt.Errorf("%w: check", ErrConstraint)
	ErrConstraintUnique     = fmt.Errorf("%w: unique", ErrConstraint)
	ErrConstraintPrimaryKey = fmt.Errorf("%w: primary key", ErrConstraint)
	ErrConstraintForeignKey = fmt.Errorf("%w: foreign key", ErrConstraint)
	ErrBusy                 = errors.New("too busy")
	ErrAlreadyApplied       = errors.New("the entry was already applied")
)

var constraintCodes = map[sqlite3.ErrNoExtended]error{
	sqlite3.ErrConstraintCheck:      ErrConstraintCheck,
	sqlite3.ErrConstraintUnique:     ErrConstraintUnique,
	sqlite3.ErrConstraintPrimaryKey: ErrConstraintPrimaryKey,
	sqlite3.ErrConstraintForeignKey: ErrConstraintForeignKey,
}

var resultCodes = []struct {
	code string
	err  error
}{
	{"not_found", ErrNotFound},
	{"last_password_admin", ErrLastPasswordAdmin},
	{"too_many_users", ErrTooManyUsers},
	{"no_password", ErrNoPassword},
	{"duplicate", ErrDuplicate},
	{"duplicate_icid", ErrDuplicateICID},
	{"identity_conflict", ErrIdentityConflict},
	{"subscription_exists", ErrSubscriptionExists},
	{"oidc_disabled", ErrOIDCDisabled},
	{"oidc_changed", ErrOIDCChanged},
	{"not_linked", ErrNotLinked},
	{"no_user", ErrNoUser},
	{"no_role", ErrNoRole},
	{"linked_elsewhere", ErrLinkedElsewhere},
	{"state_used", ErrStateUsed},
	{"settings_conflict", settings.ErrConflict},
	{"settings_not_found", settings.ErrNotFound},
	{"settings_invalid", settings.ErrInvalid},
	{"mcptt_conflict", mcpttsettings.ErrConflict},
	{"mcptt_not_found", mcpttsettings.ErrNotFound},
	{"mcptt_invalid", mcpttsettings.ErrInvalid},
	{"token_used", ErrTokenUsed},
	{"token_expired", ErrTokenExpired},
	{"invalid", ErrInvalid},
	{"constraint_check", ErrConstraintCheck},
	{"constraint_unique", ErrConstraintUnique},
	{"constraint_primary_key", ErrConstraintPrimaryKey},
	{"constraint_foreign_key", ErrConstraintForeignKey},
	{"constraint", ErrConstraint},
	{"already_applied", ErrAlreadyApplied},
}

var ErrInvalid = errors.New("invalid command")

func CodeOf(err error) string {
	code, _ := codeOf(err)
	return code
}

func codeOf(err error) (string, bool) {
	for _, c := range resultCodes {
		if errors.Is(err, c.err) {
			return c.code, true
		}
	}

	var e sqlite3.Error
	if errors.As(err, &e) && e.Code == sqlite3.ErrConstraint {
		if sentinel, ok := constraintCodes[e.ExtendedCode]; ok {
			return codeOf(sentinel)
		}

		return "constraint", true
	}

	return "", false
}

type resultError struct {
	message  string
	sentinel error
}

func (e *resultError) Error() string { return e.message }

func (e *resultError) Unwrap() error { return e.sentinel }

func (r Result) Err() error {
	if r.Code == "" {
		return nil
	}

	for _, c := range resultCodes {
		if c.code == r.Code {
			return &resultError{message: r.Message, sentinel: c.err}
		}
	}

	return fmt.Errorf("%s: %s", r.Code, r.Message)
}

type requestIDKey struct{}

func WithRequestID(ctx context.Context, id RequestID) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

func RequestIDOf(parts ...[]byte) RequestID {
	h := sha256.New()

	for _, p := range parts {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write(p)
	}

	var id RequestID

	copy(id[:], h.Sum(nil))

	return id
}

func requestID(ctx context.Context, t CommandType) (RequestID, bool) {
	if id, ok := ctx.Value(requestIDKey{}).(RequestID); ok && id != (RequestID{}) {
		var typ [2]byte
		binary.BigEndian.PutUint16(typ[:], uint16(t))

		return RequestIDOf(id[:], typ[:]), true
	}

	var id RequestID

	_, _ = rand.Read(id[:])

	return id, false
}
