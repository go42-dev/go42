package interceptors_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"

	grpcAPI "github.com/go42-dev/go42/internal/api/grpc"
	"github.com/go42-dev/go42/internal/auth"
	"github.com/go42-dev/go42/internal/auth/domain"
	authInterceptors "github.com/go42-dev/go42/internal/auth/interceptors"
	authMocks "github.com/go42-dev/go42/internal/auth/mocks"
	"github.com/go42-dev/go42/internal/auth/models"
	"github.com/go42-dev/go42/internal/cache"
)

const (
	interceptorTestToken    = "api_kXqdf2uQ7hmOARp-pZrhA6_IsZSeKCmSEM4YFKBGIzA"
	interceptorTestUserUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	interceptorTestMethod   = "/auth.v1.AuthService/ListUsers"
)

func TestUnaryAuthInterceptor_APITokenUsesOwnerIdentityAndTokenPermissions(t *testing.T) {
	const rawToken = "api_kXqdf2uQ7hmOARp-pZrhA6_IsZSeKCmSEM4YFKBGIzA"
	user := &models.User{
		ID:     42,
		UUID:   uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		Status: domain.UserStatusActive,
		Roles: []models.Role{{
			Permissions: []models.Permission{{Resource: "users", Action: "delete"}},
		}},
	}
	apiToken := &models.Token{
		ID:     7,
		UUID:   uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		UserID: user.ID,
		Permissions: []models.Permission{{
			Resource: "users",
			Action:   "list",
		}},
	}

	ctrl := gomock.NewController(t)
	repository := authMocks.NewMockrepository(ctrl)
	repository.EXPECT().GetToken(gomock.Any(), sha256String(rawToken)).Return(apiToken, nil)
	repository.EXPECT().GetUserByID(gomock.Any(), user.ID).Return(user, nil)
	service := auth.NewService(
		repository,
		authMocks.NewMockoutboxService(ctrl),
		cache.NewNoop(),
	)

	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-api-key", rawToken),
	)
	interceptor := authInterceptors.NewUnaryAuthInterceptor(service)
	_, err := interceptor(
		ctx,
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/auth.v1.AuthService/ListUsers"},
		func(ctx context.Context, _ interface{}) (interface{}, error) {
			authInfo := auth.RetrieveAuthFromContext(ctx)
			if authInfo == nil {
				t.Fatal("authentication context is nil")
			}
			if authInfo.ID != user.ID || authInfo.UUID != user.UUID.String() {
				t.Errorf(
					"authentication identity = (%d, %q), want (%d, %q)",
					authInfo.ID, authInfo.UUID, user.ID, user.UUID.String(),
				)
			}
			if authInfo.Type != domain.AuthenticationTypeApiToken {
				t.Errorf("authentication type = %q, want %q", authInfo.Type, domain.AuthenticationTypeApiToken)
			}
			if !authInfo.HasPermission(domain.RBACPermissionUsersList) {
				t.Error("API-token permission is missing")
			}
			if authInfo.HasPermission(domain.RBACPermissionUsersDelete) {
				t.Error("owner role permission leaked into API-token permissions")
			}
			return nil, nil
		},
	)
	if err != nil {
		t.Fatalf("interceptor error = %v", err)
	}
}

func TestAuthAccessInterceptorsRequirePermissions(t *testing.T) {
	for _, transport := range []struct {
		name   string
		stream bool
	}{{name: "unary"}, {name: "stream", stream: true}} {
		t.Run(transport.name, func(t *testing.T) {
			for _, test := range []struct {
				name       string
				registered bool
				required   []string
				actions    []string
				wantCode   codes.Code
			}{
				{name: "unregistered method", actions: []string{"list"}, wantCode: codes.PermissionDenied},
				{
					name: "method without permissions", registered: true,
					actions: []string{"list"}, wantCode: codes.PermissionDenied,
				},
				{
					name: "token without permissions", registered: true, required: []string{"users:list"},
					wantCode: codes.PermissionDenied,
				},
				{
					name: "wrong token permission", registered: true, required: []string{"users:list"},
					actions: []string{"read_others"}, wantCode: codes.PermissionDenied,
				},
				{
					name: "owner permission cannot authorize token", registered: true, required: []string{"users:delete"},
					actions: []string{"list"}, wantCode: codes.PermissionDenied,
				},
				{
					name: "token permission authorizes request", registered: true, required: []string{"users:list"},
					actions: []string{"list"}, wantCode: codes.OK,
				},
				{
					name: "missing first permission", registered: true, required: []string{"users:list", "users:read_others"},
					actions: []string{"read_others"}, wantCode: codes.PermissionDenied,
				},
				{
					name: "missing second permission", registered: true, required: []string{"users:list", "users:read_others"},
					actions: []string{"list"}, wantCode: codes.PermissionDenied,
				},
				{
					name: "all permissions present", registered: true, required: []string{"users:list", "users:read_others"},
					actions: []string{"list", "read_others"}, wantCode: codes.OK,
				},
			} {
				t.Run(test.name, func(t *testing.T) {
					service, repository := newInterceptorTestService(t)
					expectInterceptorAuthentication(repository, test.actions...)
					registry := grpcAPI.NewPermissionRegistry()
					if test.registered {
						registry.Register(interceptorTestMethod, test.required...)
					}
					ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("x-api-key", interceptorTestToken))
					handlerCalls := 0

					err := runAuthAccessInterceptors(
						t, transport.stream, service, registry, ctx, interceptorTestMethod,
						func(handlerCtx context.Context) error {
							handlerCalls++
							authInfo := auth.RetrieveAuthFromContext(handlerCtx)
							require.NotNil(t, authInfo)
							assert.Equal(t, 42, authInfo.ID)
							assert.Equal(t, interceptorTestUserUUID, authInfo.UUID)
							assert.Equal(t, domain.AuthenticationType(domain.AuthenticationTypeApiToken), authInfo.Type)
							for _, action := range []string{"list", "read_others", "delete"} {
								assert.Equal(
									t,
									slices.Contains(test.actions, action),
									authInfo.HasPermission("users:"+action),
									action,
								)
							}
							md, ok := metadata.FromIncomingContext(handlerCtx)
							require.True(t, ok)
							assert.Equal(t, []string{interceptorTestToken}, md.Get("x-api-key"))
							assert.Equal(t, ctx.Done(), handlerCtx.Done())
							return nil
						},
					)

					if test.wantCode == codes.OK {
						require.NoError(t, err)
						assert.Equal(t, 1, handlerCalls)
					} else {
						require.Error(t, err)
						assert.Equal(t, test.wantCode, status.Code(err))
						assert.Zero(t, handlerCalls)
					}
					assert.Nil(t, auth.RetrieveAuthFromContext(ctx))
				})
			}
		})
	}
}

func TestUnaryAccessInterceptorRequiresAuthentication(t *testing.T) {
	registry := grpcAPI.NewPermissionRegistry()
	registry.Register(interceptorTestMethod, domain.RBACPermissionUsersList)
	handlerCalls := 0

	response, err := authInterceptors.NewUnaryAccessInterceptor(registry)(
		t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: interceptorTestMethod},
		func(context.Context, interface{}) (interface{}, error) {
			handlerCalls++
			return nil, nil
		},
	)

	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	assert.Nil(t, response)
	assert.Zero(t, handlerCalls)
}

func TestStreamAccessInterceptorRequiresAuthenticatedStream(t *testing.T) {
	for _, test := range []struct {
		name      string
		authInCtx bool
	}{
		{name: "unauthenticated stream"},
		{name: "unwrapped stream with auth context", authInCtx: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := grpcAPI.NewPermissionRegistry()
			registry.Register(interceptorTestMethod, domain.RBACPermissionUsersList)
			ctx := t.Context()
			if test.authInCtx {
				authInfo := domain.ContextAuthInfo{ID: 42, UUID: interceptorTestUserUUID}
				authInfo.SetPermissions([]string{domain.RBACPermissionUsersList})
				ctx = auth.SetAuthToContext(ctx, authInfo)
			}
			handlerCalls := 0

			err := authInterceptors.NewStreamAccessInterceptor(registry)(
				nil, &interceptorTestStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: interceptorTestMethod},
				func(interface{}, grpc.ServerStream) error {
					handlerCalls++
					return nil
				},
			)

			require.Error(t, err)
			assert.Equal(t, codes.Unauthenticated, status.Code(err))
			assert.Zero(t, handlerCalls)
		})
	}
}

func TestAuthAccessInterceptorsRejectMissingCredentials(t *testing.T) {
	for _, transport := range []struct {
		name   string
		stream bool
	}{{name: "unary"}, {name: "stream", stream: true}} {
		t.Run(transport.name, func(t *testing.T) {
			for _, test := range []struct {
				name string
				md   metadata.MD
			}{
				{name: "missing metadata"},
				{name: "empty metadata", md: metadata.MD{}},
				{name: "missing API key", md: metadata.Pairs("x-request-id", "request-id")},
				{name: "empty API key", md: metadata.Pairs("x-api-key", "")},
			} {
				t.Run(test.name, func(t *testing.T) {
					service, _ := newInterceptorTestService(t)
					registry := grpcAPI.NewPermissionRegistry()
					registry.Register(interceptorTestMethod, domain.RBACPermissionUsersList)
					ctx := t.Context()
					if test.md != nil {
						ctx = metadata.NewIncomingContext(ctx, test.md)
					}
					handlerCalls := 0

					err := runAuthAccessInterceptors(
						t, transport.stream, service, registry, ctx, interceptorTestMethod,
						func(context.Context) error {
							handlerCalls++
							return nil
						},
					)

					require.Error(t, err)
					assert.Equal(t, codes.Unauthenticated, status.Code(err))
					assert.Zero(t, handlerCalls)
				})
			}
		})
	}
}

func TestAuthAccessInterceptorsBypassPublicMethods(t *testing.T) {
	for _, transport := range []struct {
		name   string
		stream bool
	}{{name: "unary"}, {name: "stream", stream: true}} {
		t.Run(transport.name, func(t *testing.T) {
			for _, test := range []struct {
				name   string
				method string
			}{
				{name: "health check", method: healthpb.Health_Check_FullMethodName},
				{name: "health list", method: healthpb.Health_List_FullMethodName},
				{name: "health watch", method: healthpb.Health_Watch_FullMethodName},
				{name: "reflection", method: reflectionpb.ServerReflection_ServerReflectionInfo_FullMethodName},
			} {
				t.Run(test.name, func(t *testing.T) {
					service, _ := newInterceptorTestService(t)
					registry := grpcAPI.NewPermissionRegistry()
					ctx := t.Context()
					handlerCalls := 0

					err := runAuthAccessInterceptors(
						t, transport.stream, service, registry, ctx, test.method,
						func(handlerCtx context.Context) error {
							handlerCalls++
							assert.Same(t, ctx, handlerCtx)
							assert.Nil(t, auth.RetrieveAuthFromContext(handlerCtx))
							return nil
						},
					)

					require.NoError(t, err)
					assert.Equal(t, 1, handlerCalls)
				})
			}
		})
	}
}

func TestAuthAccessInterceptorsPreserveHandlerErrors(t *testing.T) {
	for _, transport := range []struct {
		name   string
		stream bool
	}{{name: "unary"}, {name: "stream", stream: true}} {
		t.Run(transport.name, func(t *testing.T) {
			service, repository := newInterceptorTestService(t)
			expectInterceptorAuthentication(repository, "list")
			registry := grpcAPI.NewPermissionRegistry()
			registry.Register(interceptorTestMethod, domain.RBACPermissionUsersList)
			ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("x-api-key", interceptorTestToken))
			handlerError := errors.New("handler failure")
			handlerCalls := 0

			err := runAuthAccessInterceptors(
				t, transport.stream, service, registry, ctx, interceptorTestMethod,
				func(context.Context) error {
					handlerCalls++
					return handlerError
				},
			)

			require.ErrorIs(t, err, handlerError)
			assert.Equal(t, 1, handlerCalls)
		})
	}
}

func newInterceptorTestService(t *testing.T) (*auth.Service, *authMocks.Mockrepository) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repository := authMocks.NewMockrepository(ctrl)
	return auth.NewService(repository, authMocks.NewMockoutboxService(ctrl), cache.NewNoop()), repository
}

func expectInterceptorAuthentication(repository *authMocks.Mockrepository, actions ...string) {
	user := &models.User{
		ID: 42, UUID: uuid.MustParse(interceptorTestUserUUID), Status: domain.UserStatusActive,
		Roles: []models.Role{{Permissions: []models.Permission{{Resource: "users", Action: "delete"}}}},
	}
	token := &models.Token{
		ID: 7, UUID: uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"), UserID: user.ID,
	}
	for _, action := range actions {
		token.Permissions = append(token.Permissions, models.Permission{Resource: "users", Action: action})
	}
	gomock.InOrder(
		repository.EXPECT().GetToken(gomock.Any(), sha256String(interceptorTestToken)).Return(token, nil),
		repository.EXPECT().GetUserByID(gomock.Any(), user.ID).Return(user, nil),
	)
}

//nolint:contextcheck // Streaming RPCs pass the request context through grpc.ServerStream.
func runAuthAccessInterceptors(
	t *testing.T, stream bool, service *auth.Service, registry *grpcAPI.PermissionRegistry,
	ctx context.Context, method string, handler func(context.Context) error,
) error {
	t.Helper()
	if stream {
		server := &struct{ name string }{name: "server"}
		info := &grpc.StreamServerInfo{FullMethod: method}
		authenticate := authInterceptors.NewStreamAuthInterceptor(service)
		authorize := authInterceptors.NewStreamAccessInterceptor(registry)
		return authenticate(
			server, &interceptorTestStream{ctx: ctx}, info,
			func(srv interface{}, ss grpc.ServerStream) error {
				return authorize(
					srv, ss, info,
					func(handlerSrv interface{}, handlerStream grpc.ServerStream) error {
						assert.Same(t, server, handlerSrv)
						assert.Same(t, ss, handlerStream)
						return handler(handlerStream.Context())
					},
				)
			},
		)
	}
	request := &struct{ name string }{name: "request"}
	wantResponse := &struct{ name string }{name: "response"}
	info := &grpc.UnaryServerInfo{FullMethod: method}
	handlerCalled := false
	response, err := authInterceptors.NewUnaryAuthInterceptor(service)(
		ctx, request, info,
		func(authCtx context.Context, req interface{}) (interface{}, error) {
			return authInterceptors.NewUnaryAccessInterceptor(registry)(
				authCtx, req, info,
				func(handlerCtx context.Context, handlerRequest interface{}) (interface{}, error) {
					handlerCalled = true
					assert.Same(t, request, handlerRequest)
					return wantResponse, handler(handlerCtx)
				},
			)
		},
	)
	if handlerCalled {
		assert.Same(t, wantResponse, response)
	} else {
		assert.Nil(t, response)
	}
	return err
}

type interceptorTestStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *interceptorTestStream) Context() context.Context { return s.ctx }

func sha256String(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}
