package adapter

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	httpAPI "github.com/go42-dev/go42/internal/api/http"
	"github.com/go42-dev/go42/internal/auth"
	"github.com/go42-dev/go42/internal/auth/domain"
	authMiddleware "github.com/go42-dev/go42/internal/auth/middleware"
	"github.com/go42-dev/go42/internal/auth/models"
	"github.com/go42-dev/go42/internal/tools"
)

//go:generate mockgen -source $GOFILE -package mocks -destination mocks/mocks.go

type serviceAccessor interface {
	SignUp(ctx context.Context, email string, password string) (*models.User, error)
	Login(ctx context.Context, email string, password string) (*domain.Tokens, error)
	Refresh(ctx context.Context, token string) (*domain.Tokens, error)
	Logout(ctx context.Context, refreshToken string) error
	CheckIPLimit(ctx context.Context, action domain.AuthenticationAction, ip string) error

	CreateUser(ctx context.Context, data *domain.CreateUserData) (*models.User, error)
	UpdateUser(ctx context.Context, uuid string, data *domain.UpdateUserData) error
	UpdateSelf(ctx context.Context, uuid string, data *domain.UpdateSelfData) error
	DeleteUser(ctx context.Context, uuid string) error
	ListUsers(ctx context.Context, limit, offset int) ([]*models.User, error)
	GetUserByID(ctx context.Context, id int) (*models.User, error)
	GetUserByUUID(ctx context.Context, uuid string) (*models.User, error)

	ValidateJWTToken(
		ctx context.Context,
		token string,
		expectedPurpose domain.JWTTokenPurpose,
	) (*domain.JWTClaims, error)
	ValidateAPIToken(ctx context.Context, token string) (*models.Token, error)
}

type Adapter struct {
	service serviceAccessor
}

func New(service serviceAccessor) *Adapter {
	return &Adapter{service: service}
}

func (a *Adapter) Register(g *echo.Group) {
	authGroup := g.Group("/auth")

	authGroup.POST("/signup", a.signup)
	authGroup.POST("/login", a.login)
	authGroup.POST("/refresh", a.refresh)
	authGroup.POST("/logout", a.logout)

	userGroup := g.Group("/users", authMiddleware.NewAuthMiddleware(a.service))

	userGroup.GET("/me", a.readSelf,
		authMiddleware.NewAccessMiddleware(domain.RBACPermissionUsersReadSelf))
	userGroup.PUT("/me", a.updateSelf,
		authMiddleware.NewAccessMiddleware(domain.RBACPermissionUsersUpdateSelf))

	userGroup.GET("", a.listUsers,
		authMiddleware.NewAccessMiddleware(domain.RBACPermissionUsersList))
	userGroup.GET("/:uuid", a.userByUUID,
		authMiddleware.NewAccessMiddleware(domain.RBACPermissionUsersReadOthers))
	userGroup.POST("", a.createUser,
		authMiddleware.NewAccessMiddleware(domain.RBACPermissionUsersCreate))
	userGroup.PUT("/:uuid", a.updateUser,
		authMiddleware.NewAccessMiddleware(domain.RBACPermissionUsersUpdate))
	userGroup.DELETE("/:uuid", a.deleteUser,
		authMiddleware.NewAccessMiddleware(domain.RBACPermissionUsersDelete))
}

type SignupRequest struct {
	Email    string `json:"email"    v:"required,email"`
	Password string `json:"password" v:"required,min=8,max=24"`
}

func (a *Adapter) signup(ctx *echo.Context) error {
	if err := a.service.CheckIPLimit(
		ctx.Request().Context(),
		domain.AuthenticationActionSignup,
		ctx.RealIP(),
	); err != nil {
		return a.processError(ctx, err)
	}
	req := new(SignupRequest)

	if err := ctx.Bind(req); err != nil {
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}

	vErrs := tools.ValidateStruct(req)
	if vErrs != nil {
		return httpAPI.SendJSONError(
			ctx, http.StatusBadRequest, http.StatusText(http.StatusBadRequest),
			httpAPI.WithValidationErrors(vErrs...),
		)
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	user, err := a.service.SignUp(ctx.Request().Context(), req.Email, req.Password)
	if err != nil {
		return a.processError(ctx, err)
	}

	return ctx.JSON(http.StatusCreated, UserResponseFromModel(user))
}

type LoginRequest struct {
	Email    string `json:"email"    v:"required,email"`
	Password string `json:"password" v:"required,min=8,max=24"`
}

func (a *Adapter) login(ctx *echo.Context) error {
	if err := a.service.CheckIPLimit(
		ctx.Request().Context(),
		domain.AuthenticationActionLogin,
		ctx.RealIP(),
	); err != nil {
		return a.processError(ctx, err)
	}
	req := new(LoginRequest)

	if err := ctx.Bind(req); err != nil {
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}

	vErrs := tools.ValidateStruct(req)
	if vErrs != nil {
		return httpAPI.SendJSONError(
			ctx, http.StatusBadRequest, http.StatusText(http.StatusBadRequest),
			httpAPI.WithValidationErrors(vErrs...),
		)
	}

	tokens, err := a.service.Login(ctx.Request().Context(), req.Email, req.Password)
	if err != nil {
		return a.processError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, tokens)
}

type RefreshTokenRequest struct {
	Token string `json:"token" v:"required"`
}

func (a *Adapter) refresh(ctx *echo.Context) error {
	req := new(RefreshTokenRequest)

	if err := ctx.Bind(req); err != nil {
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}

	vErrs := tools.ValidateStruct(req)
	if vErrs != nil {
		return httpAPI.SendJSONError(
			ctx, http.StatusBadRequest, http.StatusText(http.StatusBadRequest),
			httpAPI.WithValidationErrors(vErrs...),
		)
	}

	tokens, err := a.service.Refresh(ctx.Request().Context(), req.Token)
	if err != nil {
		return a.processError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, tokens)
}

type LogoutTokenRequest struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token"          v:"required"`
}

func (a *Adapter) logout(ctx *echo.Context) error {
	req := new(LogoutTokenRequest)

	if err := ctx.Bind(req); err != nil {
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}

	vErrs := tools.ValidateStruct(req)
	if vErrs != nil {
		return httpAPI.SendJSONError(
			ctx, http.StatusBadRequest, http.StatusText(http.StatusBadRequest),
			httpAPI.WithValidationErrors(vErrs...),
		)
	}

	err := a.service.Logout(ctx.Request().Context(), req.RefreshToken)
	if err != nil {
		return a.processError(ctx, err)
	}

	return ctx.NoContent(http.StatusOK)
}

// ----

func (a *Adapter) readSelf(ctx *echo.Context) error {
	authInfo := auth.RetrieveAuthFromContext(ctx.Request().Context())
	if authInfo == nil {
		return httpAPI.SendJSONError(ctx,
			http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
	}

	user, err := a.service.GetUserByID(ctx.Request().Context(), authInfo.ID)
	if err != nil {
		return a.processError(ctx, err)
	}

	return ctx.JSON(http.StatusOK, UserResponseFromModel(user))
}

type UpdateSelfRequest struct {
	Email           string `json:"email"            v:"omitempty,email"`
	Password        string `json:"password"         v:"omitempty,min=8,max=24"`
	CurrentPassword string `json:"current_password" v:"required_with=Email Password"`
}

func (a *Adapter) updateSelf(ctx *echo.Context) error {
	if err := a.service.CheckIPLimit(
		ctx.Request().Context(),
		domain.AuthenticationActionLogin,
		ctx.RealIP(),
	); err != nil {
		return a.processError(ctx, err)
	}
	authInfo := auth.RetrieveAuthFromContext(ctx.Request().Context())
	if authInfo == nil {
		return httpAPI.SendJSONError(ctx,
			http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
	}

	req := new(UpdateSelfRequest)

	if err := ctx.Bind(req); err != nil {
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}

	vErrs := tools.ValidateStruct(req)
	if vErrs != nil {
		return httpAPI.SendJSONError(
			ctx, http.StatusBadRequest, http.StatusText(http.StatusBadRequest),
			httpAPI.WithValidationErrors(vErrs...),
		)
	}

	updateData := &domain.UpdateSelfData{CurrentPassword: req.CurrentPassword}

	if req.Email != "" {
		email := strings.ToLower(strings.TrimSpace(req.Email))
		updateData.Email = &email
	}
	if req.Password != "" {
		password := req.Password
		updateData.Password = &password
	}

	err := a.service.UpdateSelf(ctx.Request().Context(), authInfo.UUID, updateData)
	if err != nil {
		return a.processError(ctx, err)
	}

	return ctx.NoContent(http.StatusOK)
}

// ----

func (a *Adapter) listUsers(ctx *echo.Context) error {
	limit := 0
	if value := ctx.QueryParam("limit"); len(value) > 0 {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil {
			return a.processError(ctx, domain.ErrInvalidPagination)
		}
	}
	offset := 0
	if value := ctx.QueryParam("offset"); len(value) > 0 {
		var err error
		offset, err = strconv.Atoi(value)
		if err != nil {
			return a.processError(ctx, domain.ErrInvalidPagination)
		}
	}
	if limit == 0 {
		limit = domain.UserListDefaultLimit
	}
	if limit < 0 || limit > domain.UserListMaximumLimit || offset < 0 {
		return a.processError(ctx, domain.ErrInvalidPagination)
	}
	r, err := a.service.ListUsers(ctx.Request().Context(), limit, offset)
	if err != nil {
		return a.processError(ctx, err)
	}
	resp := make([]UserResponse, len(r))
	for i, user := range r {
		resp[i] = UserResponseFromModel(user)
	}
	return ctx.JSON(http.StatusOK, resp)
}

func (a *Adapter) userByUUID(ctx *echo.Context) error {
	userUUID := ctx.Param("uuid")
	if err := uuid.Validate(userUUID); err != nil {
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}
	r, err := a.service.GetUserByUUID(ctx.Request().Context(), userUUID)
	if err != nil {
		return a.processError(ctx, err)
	}
	return ctx.JSON(http.StatusOK, UserResponseFromModel(r))
}

type CreateUserRequest struct {
	Email    string `json:"email"    v:"required,email"`
	Password string `json:"password" v:"required,min=8,max=24"`
}

func (a *Adapter) createUser(ctx *echo.Context) error {
	req := new(CreateUserRequest)

	if err := ctx.Bind(req); err != nil {
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}

	vErrs := tools.ValidateStruct(req)
	if vErrs != nil {
		return httpAPI.SendJSONError(
			ctx, http.StatusBadRequest, http.StatusText(http.StatusBadRequest),
			httpAPI.WithValidationErrors(vErrs...),
		)
	}

	data := new(domain.CreateUserData)

	data.Email = strings.ToLower(strings.TrimSpace(req.Email))
	data.Password = req.Password

	user, err := a.service.CreateUser(ctx.Request().Context(), data)
	if err != nil {
		return a.processError(ctx, err)
	}

	return ctx.JSON(http.StatusCreated, UserResponseFromModel(user))
}

type UpdateUserRequest struct {
	Email    string `json:"email"    v:"omitempty,email"`
	Password string `json:"password" v:"omitempty,min=8,max=24"`
}

func (a *Adapter) updateUser(ctx *echo.Context) error {
	userUUID := ctx.Param("uuid")
	if err := uuid.Validate(userUUID); err != nil {
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}

	req := new(UpdateUserRequest)

	if err := ctx.Bind(req); err != nil {
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}

	vErrs := tools.ValidateStruct(req)
	if vErrs != nil {
		return httpAPI.SendJSONError(
			ctx, http.StatusBadRequest, http.StatusText(http.StatusBadRequest),
			httpAPI.WithValidationErrors(vErrs...),
		)
	}

	data := new(domain.UpdateUserData)

	if req.Email != "" {
		email := strings.ToLower(strings.TrimSpace(req.Email))
		data.Email = &email
	}
	if req.Password != "" {
		password := req.Password
		data.Password = &password
	}

	err := a.service.UpdateUser(ctx.Request().Context(), userUUID, data)
	if err != nil {
		return a.processError(ctx, err)
	}

	return ctx.NoContent(http.StatusOK)
}

func (a *Adapter) deleteUser(ctx *echo.Context) error {
	userUUID := ctx.Param("uuid")
	if err := uuid.Validate(userUUID); err != nil {
		return httpAPI.SendJSONError(ctx,
			http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}
	if err := a.service.DeleteUser(ctx.Request().Context(), userUUID); err != nil {
		return a.processError(ctx, err)
	}
	return ctx.NoContent(http.StatusOK)
}
