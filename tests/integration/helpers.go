// #nosec

package integration

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
)

const (
	httpServerAddressEnvVarName = "HTTP_SERVER_ADDRESS"
	httpAPIKeyEnvVarName        = "HTTP_API_KEY"
	grpcServerAddressEnvVarName = "GRPC_SERVER_ADDRESS"
	grpcAPIKeyEnvVarName        = "GRPC_API_KEY"
)

const (
	defaultHttpServerAddress = "http://localhost:8080"
	defaultGrpcServerAddress = "localhost:50051"
	defaultGrpcAPIKey        = "api_kXqdf2uQ7hmOARp-pZrhA6_IsZSeKCmSEM4YFKBGIzA"
)

var (
	customHttpServerAddress string
	customGrpcServerAddress string
)

func init() {
	value, found := os.LookupEnv(httpServerAddressEnvVarName)
	if found {
		customHttpServerAddress = strings.TrimRight(value, "/")
	}
	value, found = os.LookupEnv(grpcServerAddressEnvVarName)
	if found {
		customGrpcServerAddress = strings.TrimRight(value, "/")
	}
}

func HTTPServerAddress() string {
	if customHttpServerAddress != "" {
		return customHttpServerAddress
	}
	return defaultHttpServerAddress
}

func HTTPAPIKey() string {
	return os.Getenv(httpAPIKeyEnvVarName)
}

func GRPCServerAddress() string {
	if customGrpcServerAddress != "" {
		return customGrpcServerAddress
	}
	return defaultGrpcServerAddress
}

func GRPCAPIKey() string {
	if apiKey := os.Getenv(grpcAPIKeyEnvVarName); apiKey != "" {
		return apiKey
	}
	return defaultGrpcAPIKey
}

// ---

const randomStringDefaultLength = 8

// GenerateRandomString returns a string with given prefix.
func GenerateRandomString(prefix string) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, randomStringDefaultLength)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return fmt.Sprintf("%s-%s", prefix, string(b))
}
