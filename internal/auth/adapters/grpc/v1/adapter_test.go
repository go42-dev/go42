package adapter

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/go42-dev/go42/api/gen/sdk/grpc/auth/v1"
	"github.com/go42-dev/go42/internal/auth/adapters/grpc/v1/mocks"
	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/models"
)

const (
	userTestUUID     = "123e4567-e89b-12d3-a456-426614174000"
	userTestEmail    = " Alice@Example.com "
	userTestPassword = "new user password"
)

type invalidPaginationTestCase struct {
	name   string
	limit  int32
	offset int32
}

type userStatusMappingTestCase struct {
	name       string
	status     string
	wantStatus pb.UserStatus
}

type grpcErrorTestCase struct {
	name        string
	err         error
	wantCode    codes.Code
	wantMessage string
}

func TestAdapterListUsersUsesDefaultLimit(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := mocks.NewMockserviceAccessor(ctrl)
	adapter := New(service)

	service.EXPECT().ListUsers(
		gomock.Any(),
		domain.UserListDefaultLimit,
		7,
	).Return(nil, nil)

	response, err := adapter.ListUsers(t.Context(), &pb.ListUsersRequest{Offset: 7})
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if response == nil {
		t.Fatal("ListUsers() response = nil")
	}
}

func TestAdapterListUsersAcceptsMaximumLimitAndMapsUsers(t *testing.T) {
	ctrl := gomock.NewController(t)
	service := mocks.NewMockserviceAccessor(ctrl)
	adapter := New(service)
	createdAt := time.Date(2026, time.September, 3, 12, 30, 0, 0, time.UTC)
	user := &models.User{
		UUID:      uuid.MustParse("123e4567-e89b-12d3-a456-426614174000"),
		Email:     "user@example.com",
		Status:    domain.UserStatusActive,
		IsSystem:  true,
		CreatedAt: createdAt,
		Roles: []models.Role{
			{
				Name: domain.RBACRoleUser,
				Permissions: []models.Permission{
					{Resource: "users", Action: "list"},
				},
			},
		},
	}
	service.EXPECT().ListUsers(
		gomock.Any(),
		domain.UserListMaximumLimit,
		11,
	).Return([]*models.User{user}, nil)

	response, err := adapter.ListUsers(t.Context(), &pb.ListUsersRequest{
		Limit:  domain.UserListMaximumLimit,
		Offset: 11,
	})
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if len(response.Users) != 1 {
		t.Fatalf("ListUsers() returned %d users, want 1", len(response.Users))
	}

	got := response.Users[0]
	if got.Uuid != user.UUID.String() || got.Email != user.Email || !got.IsSystem {
		t.Errorf("mapped user identity = %#v, want UUID, email, and system flag", got)
	}
	if got.Status != pb.UserStatus_USER_STATUS_ACTIVE {
		t.Errorf("mapped status = %s, want active", got.Status)
	}
	if !slices.Equal(got.Roles, []string{domain.RBACRoleUser}) {
		t.Errorf("mapped roles = %v, want user role", got.Roles)
	}
	if !slices.Equal(got.Permissions, []string{domain.RBACPermissionUsersList}) {
		t.Errorf("mapped permissions = %v, want users:list", got.Permissions)
	}
	if got.CreatedAt == nil || !got.CreatedAt.AsTime().Equal(createdAt) {
		t.Errorf("mapped creation time = %v, want %v", got.CreatedAt, createdAt)
	}
}

func TestAdapterListUsersRejectsInvalidPagination(t *testing.T) {
	testCases := []invalidPaginationTestCase{
		{name: "negative limit", limit: -1},
		{name: "limit above maximum", limit: domain.UserListMaximumLimit + 1},
		{name: "negative offset", offset: -1},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			adapter := New(mocks.NewMockserviceAccessor(ctrl))

			response, err := adapter.ListUsers(t.Context(), &pb.ListUsersRequest{
				Limit:  testCase.limit,
				Offset: testCase.offset,
			})
			if response != nil {
				t.Errorf("ListUsers() response = %#v, want nil", response)
			}
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("ListUsers() code = %s, want %s", status.Code(err), codes.InvalidArgument)
			}
		})
	}
}

func TestUserToProtoMapsStatuses(t *testing.T) {
	testCases := []userStatusMappingTestCase{
		{name: "active", status: domain.UserStatusActive, wantStatus: pb.UserStatus_USER_STATUS_ACTIVE},
		{name: "inactive", status: domain.UserStatusInactive, wantStatus: pb.UserStatus_USER_STATUS_INACTIVE},
		{name: "unknown", status: "unknown", wantStatus: pb.UserStatus_USER_STATUS_UNSPECIFIED},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := userToProto(&models.User{Status: testCase.status})
			if got.Status != testCase.wantStatus {
				t.Errorf("userToProto() status = %s, want %s", got.Status, testCase.wantStatus)
			}
		})
	}
}

func TestAdapterListUsersServiceErrors(t *testing.T) {
	for _, test := range []grpcErrorTestCase{
		{
			name: "invalid pagination", err: fmt.Errorf("private list details: %w", domain.ErrInvalidPagination),
			wantCode: codes.InvalidArgument, wantMessage: "invalid pagination",
		},
		{
			name: "unexpected failure", err: errors.New("private database failure"),
			wantCode: codes.Internal, wantMessage: "internal error",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := mocks.NewMockserviceAccessor(gomock.NewController(t))
			adapter := New(service)
			service.EXPECT().ListUsers(t.Context(), 20, 7).Return(nil, test.err)

			response, err := adapter.ListUsers(t.Context(), &pb.ListUsersRequest{Limit: 20, Offset: 7})

			assert.Nil(t, response)
			assertGRPCError(t, err, test.wantCode, test.wantMessage)
		})
	}
}

func TestAdapterGetUserByUUIDMapsUser(t *testing.T) {
	service := mocks.NewMockserviceAccessor(gomock.NewController(t))
	adapter := New(service)
	user, want := userResponseFixture()
	service.EXPECT().GetUserByUUID(t.Context(), userTestUUID).Return(user, nil)

	response, err := adapter.GetUserByUUID(t.Context(), &pb.GetUserByUUIDRequest{Uuid: userTestUUID})

	require.NoError(t, err)
	assert.True(t, proto.Equal(&pb.GetUserByUUIDResponse{User: want}, response), "unexpected response: %v", response)
}

func TestAdapterGetUserByUUIDServiceErrors(t *testing.T) {
	for _, test := range []grpcErrorTestCase{
		{
			name: "not found", err: fmt.Errorf("private lookup details: %w", domain.ErrEntityNotFound),
			wantCode: codes.NotFound, wantMessage: "not found",
		},
		{
			name: "unexpected failure", err: errors.New("private lookup failure"),
			wantCode: codes.Internal, wantMessage: "internal error",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := mocks.NewMockserviceAccessor(gomock.NewController(t))
			adapter := New(service)
			service.EXPECT().GetUserByUUID(t.Context(), userTestUUID).Return(nil, test.err)

			response, err := adapter.GetUserByUUID(t.Context(), &pb.GetUserByUUIDRequest{Uuid: userTestUUID})

			assert.Nil(t, response)
			assertGRPCError(t, err, test.wantCode, test.wantMessage)
		})
	}
}

func TestAdapterCreateUserMapsUser(t *testing.T) {
	service := mocks.NewMockserviceAccessor(gomock.NewController(t))
	adapter := New(service)
	user, want := userResponseFixture()
	service.EXPECT().CreateUser(t.Context(), &domain.CreateUserData{
		Email: userTestEmail, Password: userTestPassword,
	}).Return(user, nil)

	response, err := adapter.CreateUser(t.Context(), &pb.CreateUserRequest{
		Email: userTestEmail, Password: userTestPassword,
	})

	require.NoError(t, err)
	assert.True(t, proto.Equal(&pb.CreateUserResponse{User: want}, response), "unexpected response: %v", response)
}

func TestAdapterCreateUserServiceErrors(t *testing.T) {
	for _, test := range []grpcErrorTestCase{
		{
			name: "duplicate email", err: fmt.Errorf("private create details: %w", domain.ErrUserAlreadyExists),
			wantCode: codes.AlreadyExists, wantMessage: "user already exists",
		},
		{
			name: "unexpected failure", err: errors.New("private create failure"),
			wantCode: codes.Internal, wantMessage: "internal error",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := mocks.NewMockserviceAccessor(gomock.NewController(t))
			adapter := New(service)
			service.EXPECT().CreateUser(t.Context(), &domain.CreateUserData{
				Email: userTestEmail, Password: userTestPassword,
			}).Return(nil, test.err)

			response, err := adapter.CreateUser(t.Context(), &pb.CreateUserRequest{
				Email: userTestEmail, Password: userTestPassword,
			})

			assert.Nil(t, response)
			assertGRPCError(t, err, test.wantCode, test.wantMessage)
		})
	}
}

func TestAdapterUpdateUserPreservesOptionalFields(t *testing.T) {
	email, password, empty := userTestEmail, userTestPassword, ""
	for _, test := range []struct {
		name     string
		email    *string
		password *string
	}{
		{name: "omitted fields"},
		{name: "email only", email: &email},
		{name: "password only", password: &password},
		{name: "both fields", email: &email, password: &password},
		{name: "empty email", email: &empty},
		{name: "empty password", password: &empty},
		{name: "both fields empty", email: &empty, password: &empty},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := mocks.NewMockserviceAccessor(gomock.NewController(t))
			adapter := New(service)
			service.EXPECT().UpdateUser(t.Context(), userTestUUID, &domain.UpdateUserData{
				Email: test.email, Password: test.password,
			}).Return(nil)

			response, err := adapter.UpdateUser(t.Context(), &pb.UpdateUserRequest{
				Uuid: userTestUUID, Email: test.email, Password: test.password,
			})

			require.NoError(t, err)
			assert.NotNil(t, response)
		})
	}
}

func TestAdapterUpdateUserServiceErrors(t *testing.T) {
	for _, test := range []grpcErrorTestCase{
		{
			name: "weak password", err: fmt.Errorf("private update details: %w", domain.ErrPasswordWeak),
			wantCode: codes.InvalidArgument, wantMessage: "password is too weak",
		},
		{
			name: "unexpected failure", err: errors.New("private update failure"),
			wantCode: codes.Internal, wantMessage: "internal error",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := mocks.NewMockserviceAccessor(gomock.NewController(t))
			adapter := New(service)
			email, password := userTestEmail, userTestPassword
			service.EXPECT().UpdateUser(t.Context(), userTestUUID, &domain.UpdateUserData{
				Email: &email, Password: &password,
			}).Return(test.err)

			response, err := adapter.UpdateUser(t.Context(), &pb.UpdateUserRequest{
				Uuid: userTestUUID, Email: &email, Password: &password,
			})

			assert.Nil(t, response)
			assertGRPCError(t, err, test.wantCode, test.wantMessage)
		})
	}
}

func TestAdapterDeleteUser(t *testing.T) {
	service := mocks.NewMockserviceAccessor(gomock.NewController(t))
	adapter := New(service)
	service.EXPECT().DeleteUser(t.Context(), userTestUUID).Return(nil)

	response, err := adapter.DeleteUser(t.Context(), &pb.DeleteUserRequest{Uuid: userTestUUID})

	require.NoError(t, err)
	assert.NotNil(t, response)
}

func TestAdapterDeleteUserServiceErrors(t *testing.T) {
	for _, test := range []grpcErrorTestCase{
		{
			name: "not found", err: fmt.Errorf("private delete details: %w", domain.ErrEntityNotFound),
			wantCode: codes.NotFound, wantMessage: "not found",
		},
		{
			name: "unexpected failure", err: errors.New("private delete failure"),
			wantCode: codes.Internal, wantMessage: "internal error",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := mocks.NewMockserviceAccessor(gomock.NewController(t))
			adapter := New(service)
			service.EXPECT().DeleteUser(t.Context(), userTestUUID).Return(test.err)

			response, err := adapter.DeleteUser(t.Context(), &pb.DeleteUserRequest{Uuid: userTestUUID})

			assert.Nil(t, response)
			assertGRPCError(t, err, test.wantCode, test.wantMessage)
		})
	}
}

func TestAdapterProcessError(t *testing.T) {
	for _, test := range []grpcErrorTestCase{
		{name: "not found", err: domain.ErrEntityNotFound, wantCode: codes.NotFound, wantMessage: "not found"},
		{
			name: "duplicate user", err: domain.ErrUserAlreadyExists,
			wantCode: codes.AlreadyExists, wantMessage: "user already exists",
		},
		{
			name: "invalid credentials", err: domain.ErrInvalidCredentials,
			wantCode: codes.InvalidArgument, wantMessage: "invalid credentials",
		},
		{
			name: "invalid email", err: domain.ErrInvalidEmail,
			wantCode: codes.InvalidArgument, wantMessage: "invalid email address",
		},
		{
			name: "weak password", err: domain.ErrPasswordWeak,
			wantCode: codes.InvalidArgument, wantMessage: "password is too weak",
		},
		{
			name: "invalid pagination", err: domain.ErrInvalidPagination,
			wantCode: codes.InvalidArgument, wantMessage: "invalid pagination",
		},
		{
			name: "authentication unavailable", err: domain.ErrAuthenticationUnavailable,
			wantCode: codes.Unavailable, wantMessage: "authentication unavailable",
		},
		{
			name: "invalid token", err: domain.ErrInvalidToken,
			wantCode: codes.Unauthenticated, wantMessage: "invalid token",
		},
		{
			name: "unexpected failure", err: errors.New("private database failure"),
			wantCode: codes.Internal, wantMessage: "internal error",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, variant := range []struct {
				name string
				err  error
			}{
				{name: "direct", err: test.err},
				{name: "wrapped", err: fmt.Errorf("private service details: %w", test.err)},
			} {
				t.Run(variant.name, func(t *testing.T) {
					adapter := New(mocks.NewMockserviceAccessor(gomock.NewController(t)))

					err := adapter.processError(variant.err)

					assertGRPCError(t, err, test.wantCode, test.wantMessage)
				})
			}
		})
	}
}

func userResponseFixture() (*models.User, *pb.User) {
	createdAt := time.Date(2026, time.September, 8, 12, 30, 0, 0, time.UTC)
	return &models.User{
		ID:        101,
		UUID:      uuid.MustParse(userTestUUID),
		Email:     "alice@example.com",
		Password:  sql.Null[string]{V: "private password hash", Valid: true},
		Status:    domain.UserStatusActive,
		CreatedAt: createdAt,
		Roles: []models.Role{{
			Name: "auditor",
			Permissions: []models.Permission{
				{Resource: "users", Action: "list"},
				{Resource: "users", Action: "read_others"},
			},
		}},
	}, &pb.User{
		Uuid:        userTestUUID,
		Email:       "alice@example.com",
		Status:      pb.UserStatus_USER_STATUS_ACTIVE,
		Roles:       []string{"auditor"},
		Permissions: []string{"users:list", "users:read_others"},
		CreatedAt:   timestamppb.New(createdAt),
	}
}

func assertGRPCError(t *testing.T, err error, code codes.Code, message string) {
	t.Helper()
	require.Error(t, err)
	grpcStatus, ok := status.FromError(err)
	require.True(t, ok, "expected gRPC status, got %v", err)
	assert.Equal(t, code, grpcStatus.Code())
	assert.Equal(t, message, grpcStatus.Message())
	assert.Empty(t, grpcStatus.Details())
}
