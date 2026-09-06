package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	passwordvalidator "github.com/wagslane/go-password-validator"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/crypto/bcrypt"

	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/models"
	"github.com/go42-dev/go42/internal/metrics"
	outboxDomain "github.com/go42-dev/go42/internal/outbox/domain"
	"github.com/go42-dev/go42/internal/tools"
)

const (
	maxJWTTokenBytes    = 8192
	apiTokenPrefix      = "api_"
	apiTokenSecretBytes = 32
)

//go:generate mockgen -source $GOFILE -package mocks -destination mocks/mocks.go

type repository interface {
	WithTransaction(ctx context.Context, fn func(txCtx context.Context) error) error

	CreateUser(ctx context.Context, user *models.User) error
	UpdateUser(ctx context.Context, user *models.User) error
	DeleteUser(ctx context.Context, user *models.User) error
	ListUsers(ctx context.Context, limit, offset int) ([]*models.User, error)
	GetUserByID(ctx context.Context, id int) (*models.User, error)
	GetUserByUUID(ctx context.Context, uuid string) (*models.User, error)
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)

	AssignRoleToUser(ctx context.Context, userID int, role string) error

	GetToken(ctx context.Context, hashedToken string) (*models.Token, error)
	CreateSession(ctx context.Context, session *models.Session) error
	GetActiveSession(ctx context.Context, sessionID, userUUID string) (*models.Session, error)
	RotateSession(
		ctx context.Context,
		sessionID, userUUID, previousTokenID, nextTokenID string,
		expiresAt time.Time,
	) (bool, error)
	RevokeSession(ctx context.Context, sessionID, userUUID string) error
}

type cache interface {
	AllowRateLimit(
		ctx context.Context,
		key string,
		interval time.Duration,
		burst int,
		ttl time.Duration,
	) (bool, error)
}

type outboxService interface {
	NewOutboxMessage(ctx context.Context, topic string, msg *outboxDomain.Message) error
}

// jwtSecret holds the SHA256 hash of the secret and the secret itself.
// The SHA256 hash is used as the KID (Key ID) in JWT tokens to
// allow for key rotation.
type jwtSecret struct {
	sha256 string
	secret string
}

type Service struct {
	logger        *slog.Logger
	repository    repository
	cache         cache
	outboxService outboxService

	jwtSecrets []jwtSecret

	jwtIssuer   string
	jwtAudience []string

	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration

	minPasswordEntropyBits int

	rateLimiterEnabled     bool
	loginAccountRequests   int
	loginIPRequests        int
	loginWindow            time.Duration
	signupIPRequests       int
	signupWindow           time.Duration
	refreshSessionRequests int
	refreshWindow          time.Duration

	tokensUsedChan chan domain.TokenWasUsed
}

func NewService(
	repository repository,
	outboxService outboxService,
	cache cache,
	opts ...Option,
) *Service {
	s := &Service{
		repository:             repository,
		outboxService:          outboxService,
		cache:                  cache,
		rateLimiterEnabled:     true,
		loginAccountRequests:   5,
		loginIPRequests:        20,
		loginWindow:            time.Minute,
		signupIPRequests:       5,
		signupWindow:           time.Hour,
		refreshSessionRequests: 30,
		refreshWindow:          time.Minute,
		jwtSecrets:             make([]jwtSecret, 0, 2),
		tokensUsedChan:         make(chan domain.TokenWasUsed, tools.BufferSize4096),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.logger == nil {
		s.logger = slog.New(slog.DiscardHandler)
	}
	return s
}

// CheckIPLimit is called before decoding HTTP credentials. The transport uses
// its configured trusted-proxy policy to supply the client IP.
func (s *Service) CheckIPLimit(ctx context.Context, action domain.AuthenticationAction, ip string) error {
	switch action {
	case domain.AuthenticationActionLogin:
		return s.limitAuthentication(ctx, "login_ip", ip, s.loginIPRequests, s.loginWindow)
	case domain.AuthenticationActionSignup:
		return s.limitAuthentication(ctx, "signup_ip", ip, s.signupIPRequests, s.signupWindow)
	default:
		return fmt.Errorf("%w: unknown authentication action", domain.ErrAuthenticationUnavailable)
	}
}

func (s *Service) limitAuthentication(
	ctx context.Context,
	scope, key string,
	requests int,
	window time.Duration,
) error {
	if !s.rateLimiterEnabled {
		return nil
	}
	if key == "" {
		return fmt.Errorf("%w: invalid authentication limiter configuration", domain.ErrAuthenticationUnavailable)
	}
	limiter := tools.NewRateLimiter(
		s.cache,
		"auth:"+scope,
		requests,
		requests,
		window,
		tools.WithRateLimitWindow(window),
	)
	allowed, err := limiter.Limit(ctx, key)
	if err != nil {
		return fmt.Errorf("%w: authentication rate limiter: %w", domain.ErrAuthenticationUnavailable, err)
	}
	if !allowed {
		return domain.ErrRateLimited
	}
	return nil
}

func (s *Service) SignUp(ctx context.Context, email string, password string) (*models.User, error) {
	startTime := time.Now()
	defer func() {
		metrics.Histogram("auth_operation_duration_seconds", map[string]interface{}{
			"operation": "signup",
		}).Update(time.Since(startTime).Seconds())
	}()

	var (
		err error
	)
	err = tools.TraceReturnErr(
		ctx, "auth.service", "signup.checkpwd",
		func(ctx context.Context) error {
			if err := s.CheckPasswordStrength(password); err != nil {
				return domain.ErrPasswordWeak
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	user := &models.User{
		UUID:              uuid.New(),
		Email:             normalizeEmail(email),
		Status:            domain.UserStatusActive,
		CredentialVersion: 1,
	}

	err = tools.TraceReturnErr(
		ctx, "auth.service", "signup.setpswd",
		func(ctx context.Context) error {
			if err := user.SetPassword(password); err != nil {
				return fmt.Errorf("failed to set password: %w", err)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	err = s.repository.WithTransaction(ctx, func(txCtx context.Context) error {
		if err := s.repository.CreateUser(txCtx, user); err != nil {
			return fmt.Errorf("failed to create user: %w", err)
		}
		if err := s.repository.AssignRoleToUser(txCtx, user.ID, domain.RBACRoleUser); err != nil {
			return fmt.Errorf("failed to assign user role: %w", err)
		}
		event := outboxDomain.Message{
			AggregateID:   user.ID,
			AggregateType: domain.EventTypeAuthSignUp,
		}
		if err := s.sendEvent(txCtx, domain.TopicNameAuthEvents, event); err != nil {
			s.logger.ErrorContext(
				ctx, "failed to send event: %w",
				slog.String("topic", domain.TopicNameAuthEvents),
				slog.Any("event", event),
				slog.Any("error", err),
			)
			// assuming events are non-critical, do not fail transaction
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() && span.IsRecording() {
		span.AddEvent("user_signup_completed",
			trace.WithAttributes(
				attribute.Int("user.id", user.ID),
				attribute.String("user.uuid", user.UUID.String()),
			),
		)
		span.SetStatus(codes.Ok, "user signed up")
	}

	metrics.Counter("auth_users_created_total", map[string]interface{}{
		"method": "signup",
	}).Inc()

	return user, nil
}

func (s *Service) Login(ctx context.Context, email string, password string) (*domain.Tokens, error) {
	startTime := time.Now()
	defer func() {
		metrics.Histogram("auth_operation_duration_seconds", map[string]interface{}{
			"operation": "login",
		}).Update(time.Since(startTime).Seconds())
	}()

	email = normalizeEmail(email)
	if err := s.limitAuthentication(ctx, "account", email, s.loginAccountRequests, s.loginWindow); err != nil {
		return nil, err
	}
	user, err := s.repository.GetUserByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, domain.ErrEntityNotFound) {
			return nil, fmt.Errorf("%w: user lookup: %w", domain.ErrAuthenticationUnavailable, err)
		}
		compareDummyPassword(password)
		metrics.Counter("auth_login_attempts_total", map[string]interface{}{
			"result": "user_not_found",
		}).Inc()
		return nil, domain.ErrInvalidCredentials
	}

	if !user.IsActive() {
		compareDummyPassword(password)
		metrics.Counter("auth_login_attempts_total", map[string]interface{}{
			"result": "user_inactive",
		}).Inc()
		return nil, domain.ErrInvalidCredentials
	}

	err = tools.TraceReturnErr(
		ctx, "auth.service", "login.CompareHashAndPassword",
		func(ctx context.Context) error {
			if bcrypt.CompareHashAndPassword([]byte(user.Password.V), []byte(password)) != nil {
				return domain.ErrInvalidCredentials
			}
			return nil
		})
	if err != nil {
		metrics.Counter("auth_login_attempts_total", map[string]interface{}{
			"result": "invalid_password",
		}).Inc()
		return nil, err
	}

	tokens, err := tools.TraceReturnTWithErr[*domain.Tokens](
		ctx, "auth.service", "login.generate_tokens",
		func(ctx context.Context) (*domain.Tokens, error) {
			return s.createSession(ctx, user)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}

	event := outboxDomain.Message{
		AggregateID:   user.ID,
		AggregateType: domain.EventTypeAuthLogin,
	}
	if err := s.sendEvent(ctx, domain.TopicNameAuthEvents, event); err != nil {
		s.logger.ErrorContext(
			ctx, "failed to send event: %w",
			slog.String("topic", domain.TopicNameAuthEvents),
			slog.Any("event", event),
			slog.Any("error", err),
		)
	}

	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() && span.IsRecording() {
		span.AddEvent("user_login_completed",
			trace.WithAttributes(
				attribute.Int("user.id", user.ID),
				attribute.String("user.uuid", user.UUID.String()),
			),
		)
		span.SetStatus(codes.Ok, "user logged in")
	}

	metrics.Counter("auth_login_attempts_total", map[string]interface{}{
		"result": "success",
	}).Inc()

	return tokens, nil
}

func (s *Service) createSession(ctx context.Context, user *models.User) (*domain.Tokens, error) {
	session := &models.Session{
		ID:                uuid.New(),
		UserID:            user.ID,
		CredentialVersion: user.CredentialVersion,
		RefreshTokenID:    uuid.New(),
		ExpiresAt:         time.Now().UTC().Add(s.refreshTokenTTL).Truncate(time.Second),
	}
	tokens, err := s.generateTokens(
		user.UUID.String(),
		session.ID.String(),
		session.RefreshTokenID.String(),
		session.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	if err := s.repository.CreateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("%w: create session: %w", domain.ErrAuthenticationUnavailable, err)
	}
	return tokens, nil
}

func (s *Service) Refresh(ctx context.Context, token string) (*domain.Tokens, error) {
	return tools.TraceReturnTWithErr[*domain.Tokens](
		ctx, "auth.service", "refresh",
		func(ctx context.Context) (*domain.Tokens, error) { return s.refresh(ctx, token) },
	)
}

func (s *Service) refresh(ctx context.Context, token string) (_ *domain.Tokens, resultErr error) {
	started := time.Now()
	replayed := false
	defer func() {
		metrics.Histogram("auth_operation_duration_seconds", map[string]any{"operation": "refresh"}).
			Update(time.Since(started).Seconds())
		result := tokenOperationResult(resultErr)
		if replayed && result == "invalid_token" {
			result = "replayed_or_inactive"
		}
		metrics.Counter("auth_token_refresh_total", map[string]any{"result": result}).Inc()
	}()

	// Parse independently of active-session validation: a consumed refresh token
	// must reach the compare-and-swap so its family can be revoked on reuse.
	claims, err := s.parseJWTToken(token, domain.JWTTokenPurposeRefresh)
	if err != nil {
		return nil, err
	}
	if err := s.limitAuthentication(
		ctx, "refresh", claims.SessionID, s.refreshSessionRequests, s.refreshWindow,
	); err != nil {
		return nil, err
	}

	nextTokenID := uuid.NewString()
	expiresAt := time.Now().UTC().Add(s.refreshTokenTTL).Truncate(time.Second)
	tokens, err := s.generateTokens(claims.Subject, claims.SessionID, nextTokenID, expiresAt)
	if err != nil {
		return nil, err
	}
	rotated, err := s.repository.RotateSession(ctx, claims.SessionID, claims.Subject, claims.ID, nextTokenID, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("%w: rotate session: %w", domain.ErrAuthenticationUnavailable, err)
	}
	if !rotated {
		replayed = true
		// This write commits even though the request returns an authentication
		// error. Every descendant access/refresh token shares this session ID.
		if err := s.repository.RevokeSession(ctx, claims.SessionID, claims.Subject); err != nil {
			return nil, fmt.Errorf("%w: revoke reused session: %w", domain.ErrAuthenticationUnavailable, err)
		}
		return nil, domain.ErrInvalidToken
	}
	return tokens, nil
}

func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	// A refresh token authenticates logout even after its access token expires.
	// Already-revoked and consumed tokens can still identify their own session.
	claims, err := s.parseJWTToken(refreshToken, domain.JWTTokenPurposeRefresh)
	if err != nil {
		return err
	}
	if err := s.repository.RevokeSession(ctx, claims.SessionID, claims.Subject); err != nil {
		return fmt.Errorf("%w: revoke session: %w", domain.ErrAuthenticationUnavailable, err)
	}
	user, err := s.repository.GetUserByUUID(ctx, claims.Subject)
	if err == nil {
		err = s.sendEvent(ctx, domain.TopicNameAuthEvents, outboxDomain.Message{
			AggregateID: user.ID, AggregateType: domain.EventTypeAuthLogout,
		})
	}
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to record logout event", slog.Any("error", err))
	}
	return nil
}

func (s *Service) ValidateJWTToken(
	ctx context.Context,
	token string,
	expectedPurpose domain.JWTTokenPurpose,
) (*domain.JWTClaims, error) {
	return tools.TraceReturnTWithErr[*domain.JWTClaims](
		ctx, "auth.service", "validate_jwt_token",
		func(ctx context.Context) (*domain.JWTClaims, error) {
			return s.validateJWTToken(ctx, token, expectedPurpose)
		},
	)
}

func (s *Service) validateJWTToken(
	ctx context.Context,
	token string,
	expectedPurpose domain.JWTTokenPurpose,
) (_ *domain.JWTClaims, resultErr error) {
	started := time.Now()
	defer func() {
		metrics.Histogram("auth_jwt_validation_duration_seconds", nil).Update(time.Since(started).Seconds())
		metrics.Counter("auth_jwt_validations_total", map[string]any{"result": tokenOperationResult(resultErr)}).Inc()
	}()
	claims, err := s.parseJWTToken(token, expectedPurpose)
	if err != nil {
		return nil, err
	}
	session, err := s.repository.GetActiveSession(ctx, claims.SessionID, claims.Subject)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidToken) || errors.Is(err, domain.ErrEntityNotFound) {
			return nil, domain.ErrInvalidToken
		}
		return nil, fmt.Errorf("%w: session lookup: %w", domain.ErrAuthenticationUnavailable, err)
	}
	if session == nil ||
		(expectedPurpose == domain.JWTTokenPurposeRefresh && session.RefreshTokenID.String() != claims.ID) {
		return nil, domain.ErrInvalidToken
	}
	return claims, nil
}

func tokenOperationResult(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, domain.ErrAuthenticationUnavailable):
		return "unavailable"
	case errors.Is(err, domain.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, jwt.ErrTokenExpired):
		return "expired"
	default:
		return "invalid_token"
	}
}

func (s *Service) parseJWTToken(token string, expectedPurpose domain.JWTTokenPurpose) (*domain.JWTClaims, error) {
	if expectedPurpose != domain.JWTTokenPurposeAccess && expectedPurpose != domain.JWTTokenPurposeRefresh {
		return nil, domain.ErrInvalidToken
	}
	if len(token) > maxJWTTokenBytes || !canonicalJWT(token) {
		return nil, domain.ErrInvalidToken
	}
	if err := s.checkJWTConfiguration(); err != nil {
		return nil, err
	}
	parsed, err := jwt.ParseWithClaims(token, &domain.JWTClaims{}, func(token *jwt.Token) (any, error) {
		claims, ok := token.Claims.(*domain.JWTClaims)
		if !ok {
			return nil, domain.ErrInvalidToken
		}
		for _, secret := range s.jwtSecrets {
			if claims.KID == secret.sha256 {
				return []byte(secret.secret), nil
			}
		}
		return nil, domain.ErrInvalidToken
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(s.jwtIssuer),
		jwt.WithAudience(s.jwtAudience...),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithStrictDecoding(),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrInvalidToken, err)
	}
	claims, ok := parsed.Claims.(*domain.JWTClaims)
	if !ok || !parsed.Valid || claims.TokenUse != expectedPurpose || claims.IssuedAt == nil ||
		uuid.Validate(
			claims.Subject,
		) != nil || uuid.Validate(claims.SessionID) != nil || uuid.Validate(claims.ID) != nil {
		return nil, domain.ErrInvalidToken
	}
	return claims, nil
}

func canonicalJWT(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(part)
		// Strict decoding alone still accepts CR/LF. Round-tripping rejects
		// every alternative spelling, including embedded line breaks.
		if part == "" || err != nil || base64.RawURLEncoding.EncodeToString(decoded) != part {
			return false
		}
	}
	return true
}

func (s *Service) checkJWTConfiguration() error {
	if len(s.jwtSecrets) == 0 || s.jwtIssuer == "" || len(s.jwtAudience) == 0 || s.accessTokenTTL <= 0 ||
		s.refreshTokenTTL <= 0 {
		return fmt.Errorf("%w: incomplete JWT configuration", domain.ErrAuthenticationUnavailable)
	}
	for _, secret := range s.jwtSecrets {
		if secret.secret == "" {
			return fmt.Errorf("%w: empty JWT secret", domain.ErrAuthenticationUnavailable)
		}
	}
	if slices.Contains(s.jwtAudience, "") {
		return fmt.Errorf("%w: empty JWT audience", domain.ErrAuthenticationUnavailable)
	}
	return nil
}

func (s *Service) generateTokens(
	userUUID, sessionID, refreshTokenID string,
	refreshExpiresAt time.Time,
) (*domain.Tokens, error) {
	if err := s.checkJWTConfiguration(); err != nil {
		return nil, err
	}
	now := time.Now()
	accessToken, err := s.signJWT(
		userUUID,
		sessionID,
		uuid.NewString(),
		domain.JWTTokenPurposeAccess,
		now,
		now.Add(s.accessTokenTTL),
	)
	if err != nil {
		return nil, err
	}
	refreshToken, err := s.signJWT(
		userUUID,
		sessionID,
		refreshTokenID,
		domain.JWTTokenPurposeRefresh,
		now,
		refreshExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	return &domain.Tokens{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int(s.accessTokenTTL.Seconds()),
	}, nil
}

func (s *Service) signJWT(
	userUUID, sessionID, tokenID string,
	purpose domain.JWTTokenPurpose,
	now, expiresAt time.Time,
) (string, error) {
	secret := s.jwtSecrets[len(s.jwtSecrets)-1]
	return jwt.NewWithClaims(jwt.SigningMethodHS256, domain.JWTClaims{
		ID:        tokenID,
		Subject:   userUUID,
		Issuer:    s.jwtIssuer,
		Audience:  s.jwtAudience,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
		KID:       secret.sha256,
		TokenUse:  purpose,
		SessionID: sessionID,
	}).SignedString([]byte(secret.secret))
}

func (s *Service) CreateUser(ctx context.Context, data *domain.CreateUserData) (*models.User, error) {
	if err := s.CheckPasswordStrength(data.Password); err != nil {
		return nil, domain.ErrPasswordWeak
	}

	user := &models.User{
		UUID:              uuid.New(),
		Email:             normalizeEmail(data.Email),
		Status:            domain.UserStatusActive,
		CredentialVersion: 1,
	}

	if err := user.SetPassword(data.Password); err != nil {
		return nil, fmt.Errorf("failed to set password: %w", err)
	}

	err := s.repository.WithTransaction(ctx, func(txCtx context.Context) error {
		err := s.repository.CreateUser(txCtx, user)
		if err != nil {
			return fmt.Errorf("failed to create user: %w", err)
		}
		if err := s.repository.AssignRoleToUser(txCtx, user.ID, domain.RBACRoleUser); err != nil {
			return fmt.Errorf("failed to assign user role: %w", err)
		}
		event := outboxDomain.Message{
			AggregateID:   user.ID,
			AggregateType: domain.EventTypeUserCreate,
		}
		err = s.sendEvent(txCtx, domain.TopicNameAuthEvents, event)
		if err != nil {
			return fmt.Errorf("failed to send event: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return user, nil
}

func (s *Service) UpdateUser(ctx context.Context, uuid string, data *domain.UpdateUserData) error {
	return s.updateUser(ctx, uuid, data, nil)
}

func (s *Service) UpdateSelf(ctx context.Context, uuid string, data *domain.UpdateSelfData) error {
	if data.Email == nil && data.Password == nil {
		return nil
	}
	return s.updateUser(ctx, uuid, &data.UpdateUserData, &data.CurrentPassword)
}

func (s *Service) updateUser(
	ctx context.Context,
	uuid string,
	data *domain.UpdateUserData,
	currentPassword *string,
) error {
	err := s.repository.WithTransaction(ctx, func(txCtx context.Context) error {
		user, err := s.repository.GetUserByUUID(txCtx, uuid)
		if err != nil {
			return fmt.Errorf("failed to get user: %w", err)
		}
		if currentPassword != nil {
			if err := s.limitAuthentication(
				txCtx,
				"account",
				normalizeEmail(user.Email),
				s.loginAccountRequests,
				s.loginWindow,
			); err != nil {
				return err
			}
			if !user.IsActive() || *currentPassword == "" ||
				bcrypt.CompareHashAndPassword([]byte(user.Password.V), []byte(*currentPassword)) != nil {
				return domain.ErrInvalidCredentials
			}
		}

		var doUpdate bool

		if data.Email != nil {
			if normalizeEmail(*data.Email) != user.Email {
				doUpdate = true
				user.Email = normalizeEmail(*data.Email)
			}
		}
		if data.Password != nil {
			doUpdate = true
			if err := s.CheckPasswordStrength(*data.Password); err != nil {
				return domain.ErrPasswordWeak
			}
			if err := user.SetPassword(*data.Password); err != nil {
				return fmt.Errorf("failed to set password: %w", err)
			}
		}

		if !doUpdate {
			return nil
		}

		if err := s.repository.UpdateUser(txCtx, user); err != nil {
			return fmt.Errorf("failed to update user: %w", err)
		}

		event := outboxDomain.Message{
			AggregateID:   user.ID,
			AggregateType: domain.EventTypeUserUpdate,
		}
		if err := s.sendEvent(txCtx, domain.TopicNameAuthEvents, event); err != nil {
			s.logger.ErrorContext(
				txCtx, "failed to send event: %w",
				slog.String("topic", domain.TopicNameAuthEvents),
				slog.Any("event", event),
				slog.Any("error", err),
			)
			// assuming events are non-critical, do not fail transaction
		}

		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) DeleteUser(ctx context.Context, uuid string) error {
	err := s.repository.WithTransaction(ctx, func(txCtx context.Context) error {
		var err error
		user, err := s.repository.GetUserByUUID(txCtx, uuid)
		if err != nil {
			return fmt.Errorf("failed to get user by id: %w", err)
		}
		err = s.repository.DeleteUser(txCtx, user)
		if err != nil {
			return fmt.Errorf("failed to delete user: %w", err)
		}
		event := outboxDomain.Message{
			AggregateID:   user.ID,
			AggregateType: domain.EventTypeUserDelete,
		}
		err = s.sendEvent(txCtx, domain.TopicNameAuthEvents, event)
		if err != nil {
			return fmt.Errorf("failed to send event: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) ListUsers(ctx context.Context, limit, offset int) ([]*models.User, error) {
	return s.repository.ListUsers(ctx, limit, offset)
}

func (s *Service) GetUserByID(ctx context.Context, id int) (*models.User, error) {
	return s.repository.GetUserByID(ctx, id)
}

func (s *Service) GetUserByUUID(ctx context.Context, uuid string) (*models.User, error) {
	return s.repository.GetUserByUUID(ctx, uuid)
}

// ----

func (s *Service) ValidateAPIToken(ctx context.Context, token string) (*models.Token, error) {
	return tools.TraceReturnTWithErr[*models.Token](
		ctx, "auth.service", "validate_api_token",
		func(ctx context.Context) (*models.Token, error) {
			return s.validateAPIToken(ctx, token)
		})
}

func (s *Service) validateAPIToken(ctx context.Context, token string) (*models.Token, error) {
	if err := validateAPITokenFormat(token); err != nil {
		return nil, fmt.Errorf("%w: api token format: %w", domain.ErrInvalidToken, err)
	}

	apiToken, err := s.repository.GetToken(ctx, strToSHA256(token))
	if err != nil {
		if errors.Is(err, domain.ErrEntityNotFound) {
			return nil, fmt.Errorf("%w: api token lookup: %w", domain.ErrInvalidToken, err)
		}
		return nil, fmt.Errorf("%w: api token lookup: %w", domain.ErrAuthenticationUnavailable, err)
	}

	if apiToken.ExpiresAt.Valid && apiToken.ExpiresAt.V.Before(time.Now()) {
		return nil, fmt.Errorf("%w: expired api token", domain.ErrInvalidToken)
	}

	select {
	case s.tokensUsedChan <- domain.TokenWasUsed{ID: apiToken.ID, When: time.Now()}:
	default:
		// if channel is full, we discard payload and record warning
		s.logger.WarnContext(ctx, "auth.tokensUsedChan overflow")
	}

	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() && span.IsRecording() {
		span.AddEvent("api_token_validated",
			trace.WithAttributes(
				attribute.Int("token.id", apiToken.ID),
				attribute.Int("token.user_id", apiToken.UserID),
			),
		)
		span.SetStatus(codes.Ok, "api token validated")
	}

	return apiToken, nil
}

func (s *Service) RecentlyUsedTokensChan() <-chan domain.TokenWasUsed {
	return s.tokensUsedChan
}

// ----

func (s *Service) sendEvent(ctx context.Context, topic string, outboxMessage outboxDomain.Message) error {
	err := s.outboxService.NewOutboxMessage(ctx, topic, &outboxMessage)
	if err != nil {
		return fmt.Errorf("failed to send outbox message: %w", err)
	}
	return nil
}

func (s *Service) CheckPasswordStrength(password string) error {
	if len(password) > 72 {
		return domain.ErrPasswordWeak
	}
	return passwordvalidator.Validate(password, float64(s.minPasswordEntropyBits))
}

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// Keep unknown-account password checks comparable to normal bcrypt checks.
// This is a fixed dummy hash, not a credential for any user.
func compareDummyPassword(password string) {
	_ = bcrypt.CompareHashAndPassword(
		[]byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"),
		[]byte(password),
	)
}

func strToSHA256(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func validateAPITokenFormat(token string) error {
	secret, ok := strings.CutPrefix(token, apiTokenPrefix)
	if !ok {
		return fmt.Errorf("missing %q prefix", apiTokenPrefix)
	}

	expectedLength := base64.RawURLEncoding.EncodedLen(apiTokenSecretBytes)
	if len(secret) != expectedLength {
		return fmt.Errorf("invalid secret length: got %d, want %d", len(secret), expectedLength)
	}

	decoded, err := base64.RawURLEncoding.Strict().DecodeString(secret)
	if err != nil {
		return fmt.Errorf("invalid base64url secret: %w", err)
	}
	if len(decoded) != apiTokenSecretBytes {
		return fmt.Errorf("invalid decoded secret length: got %d, want %d", len(decoded), apiTokenSecretBytes)
	}

	return nil
}
