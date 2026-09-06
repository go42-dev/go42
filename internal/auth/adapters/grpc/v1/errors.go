package adapter

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/go42-dev/go42/internal/auth/domain"
)

func (a *Adapter) processError(err error) error {
	switch {
	case errors.Is(err, domain.ErrEntityNotFound):
		return status.Error(codes.NotFound, "not found")
	case errors.Is(err, domain.ErrUserAlreadyExists):
		return status.Error(codes.AlreadyExists, "user already exists")
	case errors.Is(err, domain.ErrInvalidCredentials):
		return status.Error(codes.InvalidArgument, "invalid credentials")
	case errors.Is(err, domain.ErrInvalidEmail):
		return status.Error(codes.InvalidArgument, "invalid email address")
	case errors.Is(err, domain.ErrPasswordWeak):
		return status.Error(codes.InvalidArgument, "password is too weak")
	case errors.Is(err, domain.ErrInvalidPagination):
		return status.Error(codes.InvalidArgument, "invalid pagination")
	case errors.Is(err, domain.ErrAuthenticationUnavailable):
		return status.Error(codes.Unavailable, "authentication unavailable")
	case errors.Is(err, domain.ErrInvalidToken):
		return status.Error(codes.Unauthenticated, "invalid token")
	default:
		return status.Error(codes.Internal, "internal error")
	}
}
