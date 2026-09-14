package adapter

import (
	"context"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"

	httpAPI "github.com/go42-dev/go42/internal/api/http"
	"github.com/go42-dev/go42/internal/auth"
	authDomain "github.com/go42-dev/go42/internal/auth/domain"
	authMiddleware "github.com/go42-dev/go42/internal/auth/middleware"
	"github.com/go42-dev/go42/internal/auth/models"
	"github.com/go42-dev/go42/internal/chat/domain"
	chatModels "github.com/go42-dev/go42/internal/chat/models"
	"github.com/go42-dev/go42/internal/chat/repository"
)

//go:generate mockgen -source $GOFILE -package mocks -destination mocks/mocks.go

type chatServiceAccessor interface {
	CreateChannel(ctx context.Context, createdBy int, name string) (*chatModels.Channel, error)
	ListChannels(ctx context.Context, limit, offset int) ([]*chatModels.Channel, error)
	CreateChannelMessage(ctx context.Context, channelUUID string, senderID int, content string) (*chatModels.Message, error)
	ListChannelMessages(ctx context.Context, channelUUID string, afterID int, limit int) ([]repository.ChannelMessage, error)
	CreatePrivateMessage(
		ctx context.Context, senderID int, senderUUID string, recipientUUID string, content string,
	) (*chatModels.Message, error)
	ListPrivateMessages(ctx context.Context, userID int, peerUUID string, afterID int, limit int) ([]repository.PrivateMessage, error)
	ListLiveUpdates(ctx context.Context, userID int, afterID int, limit int) ([]repository.LiveUpdate, error)
}

type authServiceAccessor interface {
	GetUserByID(ctx context.Context, id int) (*models.User, error)
	GetUserByUUID(ctx context.Context, uuid string) (*models.User, error)
	ValidateJWTToken(
		ctx context.Context,
		token string,
		expectedPurpose authDomain.JWTTokenPurpose,
	) (*authDomain.JWTClaims, error)
	ValidateAPIToken(ctx context.Context, token string) (*models.Token, error)
}

type Adapter struct {
	service     chatServiceAccessor
	authService authServiceAccessor
}

func New(service chatServiceAccessor, authService authServiceAccessor) *Adapter {
	return &Adapter{service: service, authService: authService}
}

func (a *Adapter) Register(g *echo.Group) {
	chatGroup := g.Group("/chat", authMiddleware.NewAuthMiddleware(a.authService))
	chatGroup.GET("/channels", a.listChannels)
	chatGroup.POST("/channels", a.createChannel)
	chatGroup.GET("/channels/:uuid/messages", a.listChannelMessages)
	chatGroup.POST("/channels/:uuid/messages", a.createChannelMessage)
	chatGroup.GET("/private/:uuid/messages", a.listPrivateMessages)
	chatGroup.POST("/private/:uuid/messages", a.createPrivateMessage)
	chatGroup.GET("/updates", a.listLiveUpdates)
}

type createChannelRequest struct {
	Name string `json:"name" v:"required"`
}

func (a *Adapter) createChannel(ctx *echo.Context) error {
	authInfo := auth.RetrieveAuthFromContext(ctx.Request().Context())
	if authInfo == nil {
		return httpAPI.SendJSONError(ctx, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
	}

	req := new(createChannelRequest)
	if err := httpAPI.BindAndValidate(ctx, req); err != nil {
		return err
	}

	channel, err := a.service.CreateChannel(ctx.Request().Context(), authInfo.ID, req.Name)
	if err != nil {
		return a.processError(ctx, err)
	}
	return ctx.JSON(http.StatusCreated, channelResponseFromModel(channel))
}

func (a *Adapter) listChannels(ctx *echo.Context) error {
	limit, offset, err := parsePagination(ctx)
	if err != nil {
		return a.processError(ctx, err)
	}
	channels, err := a.service.ListChannels(ctx.Request().Context(), limit, offset)
	if err != nil {
		return a.processError(ctx, err)
	}
	response := make([]channelResponse, len(channels))
	for i := range channels {
		response[i] = channelResponseFromModel(channels[i])
	}
	return ctx.JSON(http.StatusOK, response)
}

type createMessageRequest struct {
	Content string `json:"content" v:"required"`
}

func (a *Adapter) createChannelMessage(ctx *echo.Context) error {
	authInfo := auth.RetrieveAuthFromContext(ctx.Request().Context())
	if authInfo == nil {
		return httpAPI.SendJSONError(ctx, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
	}

	req := new(createMessageRequest)
	if err := httpAPI.BindAndValidate(ctx, req); err != nil {
		return err
	}

	message, err := a.service.CreateChannelMessage(
		ctx.Request().Context(), ctx.Param("uuid"), authInfo.ID, req.Content,
	)
	if err != nil {
		return a.processError(ctx, err)
	}
	return ctx.JSON(http.StatusCreated, messageResponseFromModel(message))
}

func (a *Adapter) listChannelMessages(ctx *echo.Context) error {
	afterID, limit, err := parseAfterAndLimit(ctx)
	if err != nil {
		return a.processError(ctx, err)
	}
	messages, err := a.service.ListChannelMessages(ctx.Request().Context(), ctx.Param("uuid"), afterID, limit)
	if err != nil {
		return a.processError(ctx, err)
	}
	response := make([]channelMessageResponse, len(messages))
	for i := range messages {
		response[i] = channelMessageResponseFromModel(messages[i])
	}
	return ctx.JSON(http.StatusOK, response)
}

func (a *Adapter) createPrivateMessage(ctx *echo.Context) error {
	authInfo := auth.RetrieveAuthFromContext(ctx.Request().Context())
	if authInfo == nil {
		return httpAPI.SendJSONError(ctx, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
	}

	req := new(createMessageRequest)
	if err := httpAPI.BindAndValidate(ctx, req); err != nil {
		return err
	}

	message, err := a.service.CreatePrivateMessage(
		ctx.Request().Context(), authInfo.ID, authInfo.UUID, ctx.Param("uuid"), req.Content,
	)
	if err != nil {
		return a.processError(ctx, err)
	}
	return ctx.JSON(http.StatusCreated, messageResponseFromModel(message))
}

func (a *Adapter) listPrivateMessages(ctx *echo.Context) error {
	authInfo := auth.RetrieveAuthFromContext(ctx.Request().Context())
	if authInfo == nil {
		return httpAPI.SendJSONError(ctx, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
	}
	afterID, limit, err := parseAfterAndLimit(ctx)
	if err != nil {
		return a.processError(ctx, err)
	}
	messages, err := a.service.ListPrivateMessages(
		ctx.Request().Context(), authInfo.ID, ctx.Param("uuid"), afterID, limit,
	)
	if err != nil {
		return a.processError(ctx, err)
	}
	response := make([]privateMessageResponse, len(messages))
	for i := range messages {
		response[i] = privateMessageResponseFromModel(messages[i])
	}
	return ctx.JSON(http.StatusOK, response)
}

func (a *Adapter) listLiveUpdates(ctx *echo.Context) error {
	authInfo := auth.RetrieveAuthFromContext(ctx.Request().Context())
	if authInfo == nil {
		return httpAPI.SendJSONError(ctx, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
	}
	afterID, limit, err := parseAfterAndLimit(ctx)
	if err != nil {
		return a.processError(ctx, err)
	}
	updates, err := a.service.ListLiveUpdates(ctx.Request().Context(), authInfo.ID, afterID, limit)
	if err != nil {
		return a.processError(ctx, err)
	}

	response := make([]liveUpdateResponse, len(updates))
	nextAfterID := afterID
	for i := range updates {
		response[i] = liveUpdateResponseFromModel(updates[i])
		if updates[i].ID > nextAfterID {
			nextAfterID = updates[i].ID
		}
	}

	return ctx.JSON(http.StatusOK, liveUpdatesResponse{
		Updates:     response,
		NextAfterID: nextAfterID,
	})
}

func parsePagination(ctx *echo.Context) (int, int, error) {
	limit := 50
	offset := 0
	query := ctx.QueryParams()
	if query.Has("limit") {
		v, err := strconv.Atoi(query.Get("limit"))
		if err != nil {
			return 0, 0, domain.ErrInvalidInput
		}
		limit = v
	}
	if query.Has("offset") {
		v, err := strconv.Atoi(query.Get("offset"))
		if err != nil {
			return 0, 0, domain.ErrInvalidInput
		}
		offset = v
	}
	return limit, offset, nil
}

func parseAfterAndLimit(ctx *echo.Context) (int, int, error) {
	afterID := 0
	limit := 50
	query := ctx.QueryParams()
	if query.Has("after_id") {
		v, err := strconv.Atoi(query.Get("after_id"))
		if err != nil {
			return 0, 0, domain.ErrInvalidInput
		}
		afterID = v
	}
	if query.Has("limit") {
		v, err := strconv.Atoi(query.Get("limit"))
		if err != nil {
			return 0, 0, domain.ErrInvalidInput
		}
		limit = v
	}
	return afterID, limit, nil
}
