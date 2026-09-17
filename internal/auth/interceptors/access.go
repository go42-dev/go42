package interceptors

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/go42-dev/go42/internal/api/grpc/interceptors"
	"github.com/go42-dev/go42/internal/auth"
)

type permissionsRegistryAccessor interface {
	PermissionsForMethod(method string) []string
}

func NewUnaryAccessInterceptor(r permissionsRegistryAccessor) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		if interceptors.DefaultSkipper(info.FullMethod) {
			return handler(ctx, req)
		}
		authInfo := auth.RetrieveAuthFromContext(ctx)
		if authInfo == nil {
			return nil, status.Errorf(codes.Unauthenticated, "unauthenticated request")
		}
		permissions := r.PermissionsForMethod(info.FullMethod)
		if len(permissions) == 0 {
			return nil, status.Errorf(codes.PermissionDenied, "no permissions found for method %s", info.FullMethod)
		}
		for _, permission := range permissions {
			if !authInfo.HasPermission(permission) {
				return nil, status.Errorf(
					codes.PermissionDenied,
					"permission %s is required for method %s", permission, info.FullMethod)
			}
		}
		return handler(ctx, req)
	}
}

func NewStreamAccessInterceptor(r permissionsRegistryAccessor) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if interceptors.DefaultSkipper(info.FullMethod) {
			return handler(srv, ss)
		}
		var (
			wrapper        *wrappedServerStream
			wrapperPresent bool
		)
		if wrapper, wrapperPresent = ss.(*wrappedServerStream); !wrapperPresent {
			return status.Errorf(codes.Unauthenticated, "unauthenticated request")
		}
		if wrapper == nil {
			return status.Errorf(codes.Unauthenticated, "unauthenticated request")
		}
		authInfo := auth.RetrieveAuthFromContext(wrapper.Context())
		if authInfo == nil {
			return status.Errorf(codes.Unauthenticated, "unauthenticated request")
		}
		permissions := r.PermissionsForMethod(info.FullMethod)
		if len(permissions) == 0 {
			return status.Errorf(codes.PermissionDenied, "no permissions found for method %s", info.FullMethod)
		}
		for _, permission := range permissions {
			if !authInfo.HasPermission(permission) {
				return status.Errorf(
					codes.PermissionDenied,
					"permission %s is required for method %s", permission, info.FullMethod)
			}
		}
		return handler(srv, ss)
	}
}
