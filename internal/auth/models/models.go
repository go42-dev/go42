package models

import (
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/go42-dev/go42/internal/auth/domain"
)

type User struct {
	ID                int
	UUID              uuid.UUID
	Email             string
	Password          sql.Null[string]
	CredentialVersion int64 `gorm:"default:1"`
	Status            string
	IsSystem          bool
	Metadata          json.RawMessage
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         gorm.DeletedAt

	Roles []Role `gorm:"-"`
}

func (*User) TableName() string { return "auth_users" }

func (u *User) SetPassword(password string) (err error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.Password = sql.Null[string]{V: string(hash), Valid: true}
	return
}

func (u *User) IsActive() bool {
	return u.Status == domain.UserStatusActive
}

// Session is one login and its rotating refresh-token family. JWT strings are
// never persisted; the signed session and token IDs identify its state.
type Session struct {
	ID                uuid.UUID `gorm:"primaryKey"`
	UserID            int
	CredentialVersion int64
	RefreshTokenID    uuid.UUID
	ExpiresAt         time.Time
	RevokedAt         sql.Null[time.Time]
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (*Session) TableName() string { return "auth_sessions" }

func (u *User) RoleList() []string {
	roles := make([]string, len(u.Roles))
	for i, role := range u.Roles {
		roles[i] = role.Name
	}
	return roles
}

func (u *User) PermissionList() []string {
	permissions := make([]string, 0)
	for _, role := range u.Roles {
		for _, permission := range role.Permissions {
			if !slices.Contains(permissions, permission.Resource+":"+permission.Action) {
				permissions = append(permissions, permission.Resource+":"+permission.Action)
			}
		}
	}
	return permissions
}

type UserHistoryRecord struct {
	ID         uuid.UUID
	OccurredAt time.Time
	CreatedAt  time.Time
	UserID     int
	EventType  string
	Data       []byte
	Metadata   string
}

func (UserHistoryRecord) TableName() string { return "auth_users_history" }

type Role struct {
	ID          int
	Name        string
	Description string
	IsSystem    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   gorm.DeletedAt

	Permissions []Permission `gorm:"-"`
}

func (Role) TableName() string { return "auth_roles" }

type Permission struct {
	ID        int
	Resource  string
	Action    string
	CreatedAt time.Time
}

func (Permission) TableName() string { return "auth_permissions" }

type UserRole struct {
	UserID    int
	RoleID    int
	CreatedAt time.Time
	ExpiresAt sql.Null[time.Time]
}

func (UserRole) TableName() string { return "auth_user_roles" }

type Token struct {
	ID         int
	UUID       uuid.UUID
	UserID     int
	Token      string
	Name       string
	LastUsedAt sql.Null[time.Time]
	ExpiresAt  sql.Null[time.Time]
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  gorm.DeletedAt

	Permissions []Permission `gorm:"-"`
}

func (*Token) TableName() string { return "auth_api_tokens" }

func (a *Token) PermissionList() []string {
	permissions := make([]string, 0)
	for _, permission := range a.Permissions {
		if !slices.Contains(permissions, permission.Resource+":"+permission.Action) {
			permissions = append(permissions, permission.Resource+":"+permission.Action)
		}
	}
	return permissions
}
