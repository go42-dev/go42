package domain

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// RBAC roles.
// MUST reflect state of table `auth_roles`.

const (
	RBACRoleUser = "user"
)

var RBACAllRoles = []string{
	RBACRoleUser,
}

// RBAC permissions.
// MUST reflect state of table `auth_permissions`.

const (
	RBACPermissionUsersReadSelf   = "users:read_self"
	RBACPermissionUsersUpdateSelf = "users:update_self"
	RBACPermissionUsersList       = "users:list"
	RBACPermissionUsersReadOthers = "users:read_others"
	RBACPermissionUsersCreate     = "users:create"
	RBACPermissionUsersUpdate     = "users:update"
	RBACPermissionUsersDelete     = "users:delete"
)

var RBACAllPermissions = []string{
	RBACPermissionUsersReadSelf,
	RBACPermissionUsersUpdateSelf,
	RBACPermissionUsersList,
	RBACPermissionUsersReadOthers,
	RBACPermissionUsersCreate,
	RBACPermissionUsersUpdate,
	RBACPermissionUsersDelete,
}

// ---- RBAC END

const (
	UserStatusActive     = "active"
	UserStatusInactive   = "inactive"
	UserListDefaultLimit = 10
	UserListMaximumLimit = 100
)

var UserStatuses = []string{
	UserStatusActive,
	UserStatusInactive,
}

const (
	TopicNameAuthEvents = "auth_events_topic"
	EventTypeAuthSignUp = "auth.signup"
	EventTypeAuthLogin  = "auth.login"
	EventTypeAuthLogout = "auth.logout"
	EventTypeUserCreate = "user.create"
	EventTypeUserUpdate = "user.update"
	EventTypeUserDelete = "user.delete"
)

var (
	ErrEntityNotFound            = errors.New("entity not found")
	ErrUserAlreadyExists         = errors.New("user already exists")
	ErrInvalidCredentials        = errors.New("invalid credentials")
	ErrInvalidEmail              = errors.New("invalid email address")
	ErrInvalidToken              = errors.New("invalid token")
	ErrAuthenticationUnavailable = errors.New("authentication unavailable")
	ErrPasswordWeak              = errors.New("password is too weak")
	ErrInvalidPagination         = errors.New("invalid pagination")
	ErrRateLimited               = errors.New("too many authentication attempts")
)

// ----

type JWTClaims struct {
	jwt.RegisteredClaims
	KID       string          `json:"kid,omitempty"`
	TokenUse  JWTTokenPurpose `json:"token_use"`
	SessionID string          `json:"sid"`
}

type JWTTokenPurpose string

const (
	JWTTokenPurposeAccess  JWTTokenPurpose = "access"
	JWTTokenPurposeRefresh JWTTokenPurpose = "refresh"
)

// Tokens represents the structure of JWT authentication tokens.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// TokenWasUsed is a message passed to channel for outside consumers.
type TokenWasUsed struct {
	ID   int
	When time.Time
}

// ----

type AuthenticationType string

const (
	AuthenticationTypeCredentials = "credentials"
	AuthenticationTypeApiToken    = "api_token"
)

// ContextAuthInfo holds authentication information in the request context.
// ID and UUID identify the authenticated user; Type identifies the authentication method.
type ContextAuthInfo struct {
	ID            int
	UUID          string
	Type          AuthenticationType
	permissions   []string
	permissionMap map[string]struct{}
}

func (ctx *ContextAuthInfo) SetPermissions(permissions []string) {
	ctx.permissions = permissions
	ctx.permissionMap = make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		ctx.permissionMap[permission] = struct{}{}
	}
}

func (ctx *ContextAuthInfo) HasPermission(permission string) bool {
	if ctx.permissionMap == nil {
		return false
	}
	_, exists := ctx.permissionMap[permission]
	return exists
}

// ----

type CreateUserData struct {
	Email    string
	Password string
}

type UpdateUserData struct {
	Email    *string
	Password *string
}

type UpdateSelfData struct {
	UpdateUserData
	CurrentPassword string
}

type AuthenticationAction string

const (
	AuthenticationActionLogin  AuthenticationAction = "login"
	AuthenticationActionSignup AuthenticationAction = "signup"
)
