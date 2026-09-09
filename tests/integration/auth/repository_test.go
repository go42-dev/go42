package auth_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/models"
)

func TestRepository_CreateUserPopulatesDefaultsAndCustomValues(t *testing.T) {
	h := newSessionHarness(t)
	user := models.User{
		UUID: uuid.New(), Email: "defaults-" + uuid.NewString() + "@example.com",
		Status: domain.UserStatusActive, Metadata: json.RawMessage(`{"language":"en","attempts":0}`),
	}
	require.NoError(t, h.repo.CreateUser(t.Context(), &user))
	assert.Positive(t, user.ID)
	assert.EqualValues(t, 1, user.CredentialVersion)
	assert.False(t, user.CreatedAt.IsZero())
	assert.False(t, user.UpdatedAt.IsZero())
	stored, err := h.repo.GetUserByUUID(t.Context(), user.UUID.String())
	require.NoError(t, err)
	assert.Equal(t, user.ID, stored.ID)
	assert.Equal(t, user.UUID, stored.UUID)
	assert.False(t, stored.Password.Valid)
	assert.JSONEq(t, string(user.Metadata), string(stored.Metadata))
}

func TestRepository_UpdateUserScopesVersionAndPreservesOmittedValues(t *testing.T) {
	h := newSessionHarness(t)
	other := createRepositoryUser(t, h)
	otherBefore := loadStoredRepositoryUser(t, h, other.ID)
	require.NoError(t, h.db.Master().WithContext(t.Context()).Model(h.user).
		Update("is_system", true).Error)
	before := loadStoredRepositoryUser(t, h, h.user.ID)
	patch := models.User{
		ID: before.ID, CredentialVersion: before.CredentialVersion,
		Email: "updated-" + uuid.NewString() + "@example.com",
	}
	require.NoError(t, h.repo.UpdateUser(t.Context(), &patch))
	after := loadStoredRepositoryUser(t, h, before.ID)
	assert.Equal(t, patch.Email, after.Email)
	assert.Equal(t, before.CredentialVersion+1, after.CredentialVersion)
	assert.Equal(t, after.CredentialVersion, patch.CredentialVersion)
	assert.True(t, after.IsSystem, "struct updates retain the existing zero-value omission policy")
	assert.Equal(t, before.Password, after.Password)
	assert.Equal(t, before.UUID, after.UUID)
	assert.Equal(t, before.Status, after.Status)
	assert.False(t, patch.UpdatedAt.IsZero())
	assert.Equal(t, otherBefore, loadStoredRepositoryUser(t, h, other.ID))

	// A missing ID or stale version must never update other users sharing that version.
	for _, id := range []int{0, -1, before.ID} {
		stale := models.User{ID: id, CredentialVersion: before.CredentialVersion}
		require.ErrorIs(t, h.repo.UpdateUser(t.Context(), &stale), domain.ErrInvalidCredentials)
		assert.Equal(t, before.CredentialVersion, stale.CredentialVersion)
	}
	assert.Equal(t, after, loadStoredRepositoryUser(t, h, before.ID))
	assert.Equal(t, otherBefore, loadStoredRepositoryUser(t, h, other.ID))
}

func TestRepository_UserQueriesSeeCurrentTransaction(t *testing.T) {
	h := newSessionHarness(t)
	user := models.User{
		UUID: uuid.New(), Email: "transaction-" + uuid.NewString() + "@example.com",
		Status: domain.UserStatusActive,
	}
	rollback := errors.New("rollback repository query test")
	err := h.repo.WithTransaction(t.Context(), func(ctx context.Context) error {
		if err := h.repo.CreateUser(ctx, &user); err != nil {
			return err
		}
		if err := h.repo.AssignRoleToUser(ctx, user.ID, domain.RBACRoleUser); err != nil {
			return err
		}
		byID, err := h.repo.GetUserByID(ctx, user.ID)
		require.NoError(t, err)
		byUUID, err := h.repo.GetUserByUUID(ctx, user.UUID.String())
		require.NoError(t, err)
		byEmail, err := h.repo.GetUserByEmail(ctx, user.Email)
		require.NoError(t, err)
		for _, stored := range []*models.User{byID, byUUID, byEmail} {
			assert.Equal(t, user.ID, stored.ID)
			assert.Contains(t, stored.RoleList(), domain.RBACRoleUser)
		}
		users, err := h.repo.ListUsers(ctx, 100, 0)
		require.NoError(t, err)
		found := false
		for _, stored := range users {
			if stored.ID == user.ID {
				found = true
				assert.Contains(t, stored.RoleList(), domain.RBACRoleUser)
			}
		}
		require.True(t, found, "replica-eligible lists must also use the current transaction")
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	_, err = h.repo.GetUserByID(t.Context(), user.ID)
	require.ErrorIs(t, err, domain.ErrEntityNotFound)
}

func TestRepository_SessionWritesPreserveTimestampsAndDeletedOwnerRevocation(t *testing.T) {
	h := newSessionHarness(t)
	other := createRepositoryUser(t, h)
	old := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	session := repositoryCleanupSession(h, time.Now().UTC().Add(time.Hour))
	session.UpdatedAt = old
	require.NoError(t, h.repo.CreateSession(t.Context(), &session))
	require.NoError(t, h.repo.RevokeSession(t.Context(), session.ID.String(), other.UUID.String()))
	var stored models.Session
	require.NoError(t, h.db.Master().WithContext(t.Context()).First(&stored, "id = ?", session.ID).Error)
	assert.False(t, stored.RevokedAt.Valid, "another user cannot revoke the session")
	assert.True(t, stored.UpdatedAt.Equal(old), "a rejected write must preserve its timestamp")

	next := uuid.NewString()
	rotated, err := h.repo.RotateSession(t.Context(), session.ID.String(), h.user.UUID.String(),
		session.RefreshTokenID.String(), next, session.ExpiresAt.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, rotated)
	require.NoError(t, h.db.Master().WithContext(t.Context()).First(&stored, "id = ?", session.ID).Error)
	assert.Equal(t, next, stored.RefreshTokenID.String())
	assert.True(t, stored.UpdatedAt.After(old), "successful rotation must also update updated_at")

	require.NoError(t, h.db.Master().WithContext(t.Context()).Model(&session).UpdateColumn("updated_at", old).Error)
	require.NoError(t, h.repo.DeleteUser(t.Context(), h.user))
	require.NoError(t, h.repo.RevokeSession(t.Context(), session.ID.String(), h.user.UUID.String()))
	require.NoError(t, h.db.Master().WithContext(t.Context()).First(&stored, "id = ?", session.ID).Error)
	assert.True(t, stored.RevokedAt.Valid, "deleted owners' sessions must remain revocable")
	assert.True(t, stored.UpdatedAt.After(old))
}

func TestRepository_TokenLastUsePreservesUpdateTimestamp(t *testing.T) {
	h := newSessionHarness(t)
	old := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	token := models.Token{
		UUID: uuid.New(), UserID: h.user.ID, Token: uuid.NewString(), Name: "timestamp",
		UpdatedAt: old, LastUsedAt: sql.Null[time.Time]{V: old, Valid: true},
	}
	require.NoError(t, h.db.Master().WithContext(t.Context()).Create(&token).Error)
	// Use a fixed GORM clock to verify automatic update timestamps.
	fixed := old.Add(time.Hour)
	previousClock := h.db.Master().NowFunc
	h.db.Master().NowFunc = func() time.Time { return fixed }
	t.Cleanup(func() { h.db.Master().NowFunc = previousClock })
	require.NoError(t, h.repo.UpdateTokenLastUsed(t.Context(), token.ID, fixed))
	var stored models.Token
	require.NoError(t, h.db.Master().WithContext(t.Context()).First(&stored, token.ID).Error)
	assert.True(t, stored.UpdatedAt.Equal(fixed))
	assert.True(t, stored.LastUsedAt.Valid)
	assert.True(t, stored.LastUsedAt.V.Equal(fixed))
	require.NoError(t, h.repo.UpdateTokenLastUsed(t.Context(), token.ID, old))
	var unchanged models.Token
	require.NoError(t, h.db.Master().WithContext(t.Context()).First(&unchanged, token.ID).Error)
	assert.Equal(t, stored, unchanged, "an older observation must not change either timestamp")
}
