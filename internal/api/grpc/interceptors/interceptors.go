package interceptors

import (
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
)

var DefaultSkipper = func(method string) bool {
	switch method {
	case reflectionpb.ServerReflection_ServerReflectionInfo_FullMethodName,
		healthpb.Health_List_FullMethodName,
		healthpb.Health_Check_FullMethodName,
		healthpb.Health_Watch_FullMethodName:
		return true
	default:
		return false
	}
}
