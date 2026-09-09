package repository

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/models"
	"github.com/go42-dev/go42/internal/cache"
	"github.com/go42-dev/go42/internal/database"
	"github.com/go42-dev/go42/internal/tools"
)

type cacheAccessor interface {
	Get(ctx context.Context, key string) (value string, found bool, err error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
}

type Repository struct {
	*database.BaseRepository
	cache          cacheAccessor
	secretCacheTTL time.Duration
}

func New(
	baseRepository *database.BaseRepository,
	cache cacheAccessor,
	secretCacheTTL time.Duration,
) *Repository {
	return &Repository{
		BaseRepository: baseRepository,
		cache:          cache,
		secretCacheTTL: secretCacheTTL,
	}
}

func (r *Repository) CreateUser(ctx context.Context, user *models.User) error {
	err := gorm.G[models.User](r.GetTx(ctx)).Create(ctx, user)
	if err != nil {
		if r.IsDuplicateKeyError(err) {
			return domain.ErrUserAlreadyExists
		}
		return fmt.Errorf("error creating user: %w", err)
	}
	return nil
}

func (r *Repository) UpdateUser(ctx context.Context, user *models.User) error {
	updated := *user
	updated.CredentialVersion++

	rows, err := gorm.G[*models.User](r.GetTx(ctx)).
		Where("id = ?", user.ID).
		Where("credential_version = ?", user.CredentialVersion).
		Updates(ctx, &updated)
	if err != nil {
		if r.IsDuplicateKeyError(err) {
			return domain.ErrUserAlreadyExists
		}
		return fmt.Errorf("error updating user: %w", err)
	}

	if rows == 0 {
		// A concurrent credential change makes the caller's password proof stale.
		return domain.ErrInvalidCredentials
	}

	user.CredentialVersion = updated.CredentialVersion
	user.UpdatedAt = updated.UpdatedAt

	return nil
}

func (r *Repository) DeleteUser(ctx context.Context, user *models.User) error {
	rows, err := gorm.G[models.User](r.GetTx(ctx)).
		Where("id = ?", user.ID).
		Delete(ctx)
	if err != nil {
		return fmt.Errorf("error deleting user: %w", err)
	}
	if rows == 0 {
		return domain.ErrEntityNotFound
	}
	return nil
}

type userRoleProjection struct {
	UserID int
	Role   models.Role `gorm:"embedded"`
}

type rolePermissionProjection struct {
	RoleID     int
	Permission models.Permission `gorm:"embedded"`
}

func (r *Repository) ListUsers(ctx context.Context, limit, offset int) ([]*models.User, error) {
	db := r.GetReadDB(ctx)

	users, err := gorm.G[*models.User](db).Limit(limit).Offset(offset).Order("id ASC").Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("error listing users: %w", err)
	}

	if len(users) == 0 {
		return users, nil
	}

	userIDs := make([]int, len(users))
	userMap := make(map[int]*models.User)
	for i, user := range users {
		userIDs[i] = user.ID
		userMap[user.ID] = users[i]
	}

	userRoles, err := gorm.G[userRoleProjection](db).Raw(`
		SELECT ur.user_id, r.*
		FROM auth_user_roles AS ur
		JOIN auth_roles AS r ON r.id = ur.role_id
		WHERE ur.user_id IN ?
			AND (ur.expires_at IS NULL OR ur.expires_at > ?)
			AND r.deleted_at IS NULL
	`, userIDs, time.Now()).Find(ctx)

	if err != nil {
		return nil, fmt.Errorf("error fetching user roles: %w", err)
	}

	roleIDs := make([]int, 0)
	roleMap := make(map[int]*models.Role)
	for _, ur := range userRoles {
		if _, exists := roleMap[ur.Role.ID]; !exists {
			roleIDs = append(roleIDs, ur.Role.ID)
			roleMap[ur.Role.ID] = &ur.Role
		}
	}

	if len(roleIDs) > 0 {
		rolePermissions, err := loadRolePermissions(ctx, db, roleIDs)

		if err != nil {
			return nil, fmt.Errorf("error fetching permissions: %w", err)
		}

		for _, rp := range rolePermissions {
			if role, exists := roleMap[rp.RoleID]; exists {
				role.Permissions = append(role.Permissions, rp.Permission)
			}
		}
	}

	for _, ur := range userRoles {
		if user, exists := userMap[ur.UserID]; exists {
			if role, exists := roleMap[ur.Role.ID]; exists {
				user.Roles = append(user.Roles, *role)
			}
		}
	}

	return users, nil
}

func (r *Repository) GetUserByID(ctx context.Context, id int) (*models.User, error) {
	return tools.TraceReturnTWithErr[*models.User](
		ctx, "auth", "auth.repository.GetUserByID",
		func(ctx context.Context) (*models.User, error) {
			return r.getUser(ctx, "id = ?", id)
		})
}

func (r *Repository) GetUserByUUID(ctx context.Context, uuid string) (*models.User, error) {
	return tools.TraceReturnTWithErr[*models.User](
		ctx, "auth", "auth.repository.GetUserByUUID",
		func(ctx context.Context) (*models.User, error) {
			return r.getUser(ctx, "uuid = ?", uuid)
		})
}

func (r *Repository) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	return tools.TraceReturnTWithErr[*models.User](
		ctx, "auth", "auth.repository.GetUserByEmail",
		func(ctx context.Context) (*models.User, error) {
			return r.getUser(ctx, "email = ?", email)
		})
}

func (r *Repository) getUser(ctx context.Context, filter string, args ...any) (*models.User, error) {
	db := r.GetTx(ctx)

	user, err := gorm.G[models.User](db).Where(filter, args...).First(ctx)
	if r.IsNotFoundError(err) {
		return nil, domain.ErrEntityNotFound
	}
	if err != nil {
		return nil, err
	}

	roles, err := gorm.G[models.Role](db).Raw(`
		SELECT DISTINCT r.*
		FROM auth_roles AS r
		JOIN auth_user_roles AS ur ON ur.role_id = r.id
		WHERE ur.user_id = ?
			AND (ur.expires_at IS NULL OR ur.expires_at > ?)
			AND r.deleted_at IS NULL
	`, user.ID, time.Now()).Find(ctx)

	if err != nil {
		return nil, fmt.Errorf("error fetching roles: %w", err)
	}

	if len(roles) > 0 {
		roleIDs := make([]int, len(roles))
		for i, role := range roles {
			roleIDs[i] = role.ID
		}

		permissions, err := loadRolePermissions(ctx, db, roleIDs)

		if err != nil {
			return nil, fmt.Errorf("error fetching permissions: %w", err)
		}

		permissionMap := make(map[int][]models.Permission)
		for _, p := range permissions {
			permissionMap[p.RoleID] = append(permissionMap[p.RoleID], p.Permission)
		}

		for i := range roles {
			roles[i].Permissions = permissionMap[roles[i].ID]
		}
	}

	user.Roles = roles
	return &user, nil
}

func loadRolePermissions(ctx context.Context, db *gorm.DB, roleIDs []int) ([]rolePermissionProjection, error) {
	return gorm.G[rolePermissionProjection](db).Raw(`
		SELECT rp.role_id, p.*
		FROM auth_permissions AS p
		JOIN auth_role_permissions AS rp ON rp.permission_id = p.id
		WHERE rp.role_id IN ?
	`, roleIDs).Find(ctx)
}

func (r *Repository) CreateSession(ctx context.Context, session *models.Session) error {
	session.ExpiresAt = session.ExpiresAt.UTC()
	return gorm.G[models.Session](r.GetTx(ctx)).Create(ctx, session)
}

// activeSessionQuery always uses the primary, including the credential-version
// comparison. Missing, expired, revoked and obsolete sessions all fail closed.
func activeSessionQuery(db *gorm.DB, sessionID, userUUID string) *gorm.DB {
	owner := db.Model(&models.User{}).Select("id").
		Where("uuid = ? AND status = ?", userUUID, domain.UserStatusActive).
		Where("credential_version = auth_sessions.credential_version")
	return db.Model(&models.Session{}).
		Where("id = ? AND revoked_at IS NULL AND expires_at > ?", sessionID, time.Now().UTC()).
		Where("user_id IN (?)", owner)
}

func (r *Repository) GetActiveSession(ctx context.Context, sessionID, userUUID string) (*models.Session, error) {
	var session models.Session
	err := activeSessionQuery(r.GetTx(ctx), sessionID, userUUID).First(&session).Error
	if r.IsNotFoundError(err) {
		return nil, domain.ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// RotateSession is a single compare-and-swap. A caller that loses the race
// must revoke the family separately, so returning an authentication error cannot
// roll back the revocation.
func (r *Repository) RotateSession(
	ctx context.Context, sessionID, userUUID, previousTokenID, nextTokenID string, expiresAt time.Time,
) (bool, error) {
	result := activeSessionQuery(r.GetTx(ctx), sessionID, userUUID).
		Where("refresh_token_id = ?", previousTokenID).
		Updates(map[string]any{
			"refresh_token_id": nextTokenID,
			"expires_at":       expiresAt.UTC(),
		})
	return result.RowsAffected == 1, result.Error
}

func (r *Repository) RevokeSession(ctx context.Context, sessionID, userUUID string) error {
	db := r.GetTx(ctx)
	// Revocation must also reach sessions owned by a soft-deleted user.
	owner := db.Model(&models.User{}).Unscoped().Select("id").Where("uuid = ?", userUUID)
	return db.Model(&models.Session{}).
		Where("id = ? AND revoked_at IS NULL", sessionID).
		Where("user_id IN (?)", owner).
		Update("revoked_at", time.Now().UTC()).Error
}

// DeleteExpiredSessions removes a bounded batch so cleanup releases database
// locks between batches. Recheck expiry when deleting in case a refresh that
// started before expiry committed after the selection.
func (r *Repository) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	cutoff := time.Now().UTC()
	db := r.GetTx(ctx)

	var ids []string
	if err := gorm.G[models.Session](db).Select("id").
		Where("expires_at <= ?", cutoff).Order("expires_at ASC").Limit(1000).
		Scan(ctx, &ids); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	rows, err := gorm.G[models.Session](db).
		Where("id IN ? AND expires_at <= ?", ids, cutoff).Delete(ctx)

	return int64(rows), err
}

func (r *Repository) AssignRoleToUser(ctx context.Context, userID int, roleName string) error {
	db := r.GetTx(ctx)
	role, err := gorm.G[models.Role](db).Where("name = ?", roleName).First(ctx)
	if err != nil {
		return fmt.Errorf("error retrieving role: %w", err)
	}

	userRole := models.UserRole{
		UserID: userID,
		RoleID: role.ID,
	}

	err = gorm.G[models.UserRole](db).Create(ctx, &userRole)
	if err != nil {
		return fmt.Errorf("error assigning role to user: %w", err)
	}

	return nil
}

const tokenCacheKeyPrefix = "cache:token"

func (r *Repository) GetToken(ctx context.Context, hashedToken string) (*models.Token, error) {
	cacheKey := fmt.Sprintf("%s:%s", tokenCacheKeyPrefix, hashedToken)
	cachedToken, err := cache.GetDecode[*models.Token](ctx, r.cache, cacheKey)
	if err != nil {
		slog.Default().ErrorContext(
			ctx, "error retrieving cached api token",
			slog.Any("err", err),
		)
	}
	if cachedToken != nil {
		return cachedToken, nil
	}

	db := r.GetReadDB(ctx)
	apiToken, err := gorm.G[models.Token](db).Where("token = ?", hashedToken).First(ctx)

	if r.IsNotFoundError(err) {
		return nil, domain.ErrEntityNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("error fetching api token: %w", err)
	}

	apiToken.Permissions, err = gorm.G[models.Permission](db).Raw(`
		SELECT p.*
		FROM auth_permissions AS p
		JOIN auth_api_tokens_permissions AS tp ON tp.permission_id = p.id
		WHERE tp.token_id = ?
	`, apiToken.ID).Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("error fetching api token permissions: %w", err)
	}

	if err := cache.SetEncode[*models.Token](
		ctx, r.cache, cacheKey, &apiToken, r.secretCacheTTL,
	); err != nil {
		slog.Default().ErrorContext(
			ctx, "error caching api token",
			slog.Int("token_id", apiToken.ID),
			slog.Any("err", err),
		)
	}

	return &apiToken, nil
}

func (r *Repository) UpdateTokenLastUsed(ctx context.Context, tokenID int, when time.Time) error {
	db := r.GetTx(ctx)
	rows, err := gorm.G[models.Token](db).Where("id = ?", tokenID).
		Where("last_used_at IS NULL OR last_used_at < ?", when).
		Update(ctx, "last_used_at", when)

	if err != nil {
		return fmt.Errorf("error updating api token: %w", err)
	}
	if rows > 0 {
		return nil
	}

	// An older or repeated timestamp is a successful no-op for an existing token.
	// Use the current transaction/primary so replica lag cannot report it missing.
	count, err := gorm.G[models.Token](db).Where("id = ?", tokenID).Count(ctx, "*")
	if err != nil {
		return fmt.Errorf("error checking api token after update: %w", err)
	}
	if count == 0 {
		return domain.ErrEntityNotFound
	}

	return nil
}

func (r *Repository) SaveUserHistoryRecord(ctx context.Context, record *models.UserHistoryRecord) error {
	return gorm.G[models.UserHistoryRecord](r.GetTx(ctx), clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(ctx, record)
}
