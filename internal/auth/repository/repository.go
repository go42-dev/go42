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
	err := r.GetTx(ctx).Create(user).Error
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
	result := r.GetTx(ctx).Model(user).
		Where("credential_version = ?", user.CredentialVersion).
		Updates(&updated)
	if result.Error != nil {
		if r.IsDuplicateKeyError(result.Error) {
			return domain.ErrUserAlreadyExists
		}
		return fmt.Errorf("error updating user: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		// A concurrent credential change makes the caller's password proof stale.
		return domain.ErrInvalidCredentials
	}
	user.CredentialVersion = updated.CredentialVersion
	return nil
}

func (r *Repository) DeleteUser(ctx context.Context, user *models.User) error {
	result := r.GetTx(ctx).Delete(user)
	if result.RowsAffected == 0 {
		return domain.ErrEntityNotFound
	}
	if result.Error != nil {
		return fmt.Errorf("error deleting user: %w", result.Error)
	}
	return nil
}

func (r *Repository) ListUsers(ctx context.Context, limit, offset int) ([]*models.User, error) {
	var users []*models.User

	result := r.GetReadDB(ctx).Limit(limit).Offset(offset).Order("id ASC").Find(&users)
	if result.Error != nil {
		return nil, fmt.Errorf("error listing users: %w", result.Error)
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

	var userRoles []struct {
		UserID int
		Role   models.Role `gorm:"embedded;embeddedPrefix:role_"`
	}

	err := r.GetReadDB(ctx).
		Table("auth_user_roles").
		Select("auth_user_roles.user_id, auth_roles.id as role_id, auth_roles.name as role_name, auth_roles.description as role_description, auth_roles.is_system as role_is_system, auth_roles.created_at as role_created_at, auth_roles.updated_at as role_updated_at").
		Joins("JOIN auth_roles ON auth_roles.id = auth_user_roles.role_id").
		Where("auth_user_roles.user_id IN ?", userIDs).
		Where("auth_user_roles.expires_at IS NULL OR auth_user_roles.expires_at > ?", time.Now()).
		Where("auth_roles.deleted_at IS NULL").
		Scan(&userRoles).Error

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
		var rolePermissions []struct {
			RoleID     int
			Permission models.Permission `gorm:"embedded"`
		}

		err = r.GetReadDB(ctx).
			Table("auth_permissions").
			Select("auth_role_permissions.role_id, auth_permissions.*").
			Joins("JOIN auth_role_permissions ON auth_role_permissions.permission_id = auth_permissions.id").
			Where("auth_role_permissions.role_id IN ?", roleIDs).
			Scan(&rolePermissions).Error

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
			return r.getUser(ctx, map[string]any{"id": id})
		})
}

func (r *Repository) GetUserByUUID(ctx context.Context, uuid string) (*models.User, error) {
	return tools.TraceReturnTWithErr[*models.User](
		ctx, "auth", "auth.repository.GetUserByUUID",
		func(ctx context.Context) (*models.User, error) {
			return r.getUser(ctx, map[string]any{"uuid": uuid})
		})
}

func (r *Repository) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	return tools.TraceReturnTWithErr[*models.User](
		ctx, "auth", "auth.repository.GetUserByEmail",
		func(ctx context.Context) (*models.User, error) {
			return r.getUser(ctx, map[string]any{"email": email})
		})
}

func (r *Repository) getUser(ctx context.Context, filter map[string]any) (*models.User, error) {
	// Authentication must observe current credentials, status and permissions.
	tx := r.GetTx(ctx)
	for key, value := range filter {
		tx = tx.Where(fmt.Sprintf("%s = ?", key), value)
	}

	var user models.User
	err := tx.First(&user).Error
	if r.IsNotFoundError(err) {
		return nil, domain.ErrEntityNotFound
	}
	if err != nil {
		return nil, err
	}

	var roles []models.Role
	err = r.GetTx(ctx).
		Distinct().
		Select("auth_roles.*").
		Joins("JOIN auth_user_roles ON auth_user_roles.role_id = auth_roles.id").
		Where("auth_user_roles.user_id = ?", user.ID).
		Where("auth_user_roles.expires_at IS NULL OR auth_user_roles.expires_at > ?", time.Now()).
		Find(&roles).Error

	if err != nil {
		return nil, fmt.Errorf("error fetching roles: %w", err)
	}
	if len(roles) > 0 {
		roleIDs := make([]int, len(roles))
		for i, role := range roles {
			roleIDs[i] = role.ID
		}

		var permissions []struct {
			RoleID     int
			Permission models.Permission `gorm:"embedded"`
		}

		err = r.GetTx(ctx).
			Table("auth_permissions").
			Select("auth_role_permissions.role_id, auth_permissions.*").
			Joins("JOIN auth_role_permissions ON auth_role_permissions.permission_id = auth_permissions.id").
			Where("auth_role_permissions.role_id IN ?", roleIDs).
			Scan(&permissions).Error

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

func (r *Repository) CreateSession(ctx context.Context, session *models.Session) error {
	session.ExpiresAt = session.ExpiresAt.UTC()
	return r.GetTx(ctx).Create(session).Error
}

// activeSessionQuery always uses the primary, including the credential-version
// comparison. Missing, expired, revoked and obsolete sessions all fail closed.
func (r *Repository) activeSessionQuery(ctx context.Context, sessionID, userUUID string) *gorm.DB {
	return r.GetTx(ctx).Model(&models.Session{}).
		Where("auth_sessions.id = ?", sessionID).
		Where("auth_sessions.revoked_at IS NULL AND auth_sessions.expires_at > ?", time.Now().UTC()).
		Where(`auth_sessions.user_id IN (
			SELECT id FROM auth_users WHERE uuid = ? AND status = ? AND deleted_at IS NULL
			AND credential_version = auth_sessions.credential_version
		)`, userUUID, domain.UserStatusActive)
}

func (r *Repository) GetActiveSession(ctx context.Context, sessionID, userUUID string) (*models.Session, error) {
	var session models.Session
	err := r.activeSessionQuery(ctx, sessionID, userUUID).First(&session).Error
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
	result := r.activeSessionQuery(ctx, sessionID, userUUID).
		Where("refresh_token_id = ?", previousTokenID).
		Updates(map[string]any{"refresh_token_id": nextTokenID, "expires_at": expiresAt.UTC()})
	return result.RowsAffected == 1, result.Error
}

func (r *Repository) RevokeSession(ctx context.Context, sessionID, userUUID string) error {
	return r.GetTx(ctx).Model(&models.Session{}).
		Where("id = ? AND revoked_at IS NULL", sessionID).
		Where("user_id IN (SELECT id FROM auth_users WHERE uuid = ?)", userUUID).
		Update("revoked_at", time.Now().UTC()).Error
}

// DeleteExpiredSessions removes a bounded batch so cleanup releases database
// locks between batches. Recheck expiry when deleting in case a refresh that
// started before expiry committed after the selection.
func (r *Repository) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	cutoff := time.Now().UTC()
	var ids []string
	if err := r.GetTx(ctx).Model(&models.Session{}).
		Where("expires_at <= ?", cutoff).Order("expires_at ASC").Limit(1000).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.GetTx(ctx).Where("id IN ? AND expires_at <= ?", ids, cutoff).Delete(&models.Session{})
	return result.RowsAffected, result.Error
}

func (r *Repository) AssignRoleToUser(ctx context.Context, userID int, roleName string) error {
	var role models.Role
	err := r.GetTx(ctx).
		Where("name = ?", roleName).
		First(&role).Error
	if err != nil {
		return fmt.Errorf("error retrieving role: %w", err)
	}

	userRole := models.UserRole{
		UserID: userID,
		RoleID: role.ID,
	}

	err = r.GetTx(ctx).Create(&userRole).Error
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

	var apiToken models.Token
	err = r.GetReadDB(ctx).
		Where("token = ?", hashedToken).
		First(&apiToken).Error

	if r.IsNotFoundError(err) {
		return nil, domain.ErrEntityNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("error fetching api token: %w", err)
	}

	err = r.GetReadDB(ctx).
		Table("auth_permissions").
		Select("auth_permissions.*").
		Joins("JOIN auth_api_tokens_permissions ON auth_api_tokens_permissions.permission_id = auth_permissions.id").
		Where("auth_api_tokens_permissions.token_id = ?", apiToken.ID).
		Scan(&apiToken.Permissions).Error
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

	return &apiToken, err
}

func (r *Repository) UpdateTokenLastUsed(ctx context.Context, tokenID int, when time.Time) error {
	result := r.GetTx(ctx).
		Model(&models.Token{}).
		Where("id = ?", tokenID).
		Update("last_used_at", when)

	if r.IsNotFoundError(result.Error) || result.RowsAffected == 0 {
		return domain.ErrEntityNotFound
	}
	if result.Error != nil {
		return fmt.Errorf("error updating api token: %w", result.Error)
	}
	return nil
}

func (r *Repository) SaveUserHistoryRecord(ctx context.Context, record *models.UserHistoryRecord) error {
	return r.GetTx(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoNothing: true,
		}).
		Create(record).
		Error
}
