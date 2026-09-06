package adapter

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/go42-dev/go42/api/gen/sdk/grpc/auth/v1"
	"github.com/go42-dev/go42/internal/auth/domain"
	"github.com/go42-dev/go42/internal/auth/models"
)

var adapterPermissionMapping = map[string]string{
	"/auth.v1.AuthService/ListUsers":     domain.RBACPermissionUsersList,
	"/auth.v1.AuthService/GetUserByUUID": domain.RBACPermissionUsersReadOthers,
	"/auth.v1.AuthService/CreateUser":    domain.RBACPermissionUsersCreate,
	"/auth.v1.AuthService/UpdateUser":    domain.RBACPermissionUsersUpdate,
	"/auth.v1.AuthService/DeleteUser":    domain.RBACPermissionUsersDelete,
}

//go:generate mockgen -source $GOFILE -package mocks -destination mocks/mocks.go

type serviceAccessor interface {
	CreateUser(ctx context.Context, data *domain.CreateUserData) (*models.User, error)
	UpdateUser(ctx context.Context, uuid string, data *domain.UpdateUserData) error
	DeleteUser(ctx context.Context, uuid string) error
	ListUsers(ctx context.Context, limit, offset int) ([]*models.User, error)
	GetUserByUUID(ctx context.Context, uuid string) (*models.User, error)
}

type permissionRegistry interface {
	Register(method string, permissions ...string)
}

type Adapter struct {
	pb.UnimplementedAuthServiceServer
	service            serviceAccessor
	permissionRegistry permissionRegistry
}

func New(svc serviceAccessor, opts ...Option) *Adapter {
	a := &Adapter{service: svc}
	for _, opt := range opts {
		opt(a)
	}
	if a.permissionRegistry != nil {
		for method, permission := range adapterPermissionMapping {
			a.permissionRegistry.Register(method, permission)
		}
	}
	return a
}

func (a *Adapter) Register(grpcServer *grpc.Server) {
	pb.RegisterAuthServiceServer(grpcServer, a)
}

func (a *Adapter) ListUsers(ctx context.Context, req *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
	limit := int(req.Limit)
	if limit == 0 {
		limit = domain.UserListDefaultLimit
	}
	offset := int(req.Offset)
	if limit < 0 || limit > domain.UserListMaximumLimit || offset < 0 {
		return nil, a.processError(domain.ErrInvalidPagination)
	}

	users, err := a.service.ListUsers(ctx, limit, offset)
	if err != nil {
		return nil, a.processError(err)
	}

	pbUsers := make([]*pb.User, 0, len(users))
	for _, user := range users {
		pbUsers = append(pbUsers, userToProto(user))
	}

	return &pb.ListUsersResponse{
		Users: pbUsers,
	}, nil
}

func (a *Adapter) GetUserByUUID(
	ctx context.Context, req *pb.GetUserByUUIDRequest,
) (*pb.GetUserByUUIDResponse, error) {
	user, err := a.service.GetUserByUUID(ctx, req.Uuid)
	if err != nil {
		return nil, a.processError(err)
	}
	return &pb.GetUserByUUIDResponse{
		User: userToProto(user),
	}, nil
}

func (a *Adapter) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.CreateUserResponse, error) {
	data := &domain.CreateUserData{
		Email:    req.Email,
		Password: req.Password,
	}

	user, err := a.service.CreateUser(ctx, data)
	if err != nil {
		return nil, a.processError(err)
	}

	return &pb.CreateUserResponse{
		User: userToProto(user),
	}, nil
}

func (a *Adapter) UpdateUser(ctx context.Context, req *pb.UpdateUserRequest) (*pb.UpdateUserResponse, error) {
	data := &domain.UpdateUserData{Email: req.Email, Password: req.Password}

	err := a.service.UpdateUser(ctx, req.Uuid, data)
	if err != nil {
		return nil, a.processError(err)
	}

	return &pb.UpdateUserResponse{}, nil
}

func (a *Adapter) DeleteUser(ctx context.Context, req *pb.DeleteUserRequest) (*pb.DeleteUserResponse, error) {
	err := a.service.DeleteUser(ctx, req.Uuid)
	if err != nil {
		return nil, a.processError(err)
	}
	return &pb.DeleteUserResponse{}, nil
}

func userToProto(user *models.User) *pb.User {
	var status pb.UserStatus
	switch user.Status {
	case domain.UserStatusActive:
		status = pb.UserStatus_USER_STATUS_ACTIVE
	case domain.UserStatusInactive:
		status = pb.UserStatus_USER_STATUS_INACTIVE
	default:
		status = pb.UserStatus_USER_STATUS_UNSPECIFIED
	}
	return &pb.User{
		Uuid:        user.UUID.String(),
		Email:       user.Email,
		Status:      status,
		Roles:       user.RoleList(),
		Permissions: user.PermissionList(),
		IsSystem:    user.IsSystem,
		CreatedAt:   timestamppb.New(user.CreatedAt),
	}
}
