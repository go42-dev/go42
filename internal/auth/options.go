package auth

import (
	"log/slog"
	"time"
)

type Option func(*Service)

func WithLogger(logger *slog.Logger) Option {
	return func(s *Service) {
		s.logger = logger
	}
}

func WithJWTSecrets(secret []string) Option {
	return func(s *Service) {
		for _, secret := range secret {
			s.jwtSecrets = append(
				s.jwtSecrets,
				jwtSecret{
					sha256: strToSHA256(secret),
					secret: secret,
				},
			)
		}
	}
}

func WithJWTAccessTokenTTL(ttl time.Duration) Option {
	return func(s *Service) {
		s.accessTokenTTL = ttl
	}
}

func WithJWTRefreshTokenTTL(ttl time.Duration) Option {
	return func(s *Service) {
		s.refreshTokenTTL = ttl
	}
}

func WithJWTIssuer(issuer string) Option {
	return func(s *Service) {
		s.jwtIssuer = issuer
	}
}

func WithJWTAudience(audience []string) Option {
	return func(s *Service) {
		s.jwtAudience = audience
	}
}

func WithMinPasswordEntropyBits(bits int) Option {
	return func(s *Service) {
		s.minPasswordEntropyBits = bits
	}
}

func WithRateLimiterEnabled(enabled bool) Option {
	return func(s *Service) {
		s.rateLimiterEnabled = enabled
	}
}

func WithLoginAccountRequests(requests int) Option {
	return func(s *Service) {
		s.loginAccountRequests = requests
	}
}

func WithLoginIPRequests(requests int) Option {
	return func(s *Service) {
		s.loginIPRequests = requests
	}
}

func WithLoginWindow(window time.Duration) Option {
	return func(s *Service) {
		s.loginWindow = window
	}
}

func WithSignupIPRequests(requests int) Option {
	return func(s *Service) {
		s.signupIPRequests = requests
	}
}

func WithSignupWindow(window time.Duration) Option {
	return func(s *Service) {
		s.signupWindow = window
	}
}

func WithRefreshSessionRequests(requests int) Option {
	return func(s *Service) {
		s.refreshSessionRequests = requests
	}
}

func WithRefreshWindow(window time.Duration) Option {
	return func(s *Service) {
		s.refreshWindow = window
	}
}
