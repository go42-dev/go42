// nolint
package test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/go42-dev/go42/api/gen/sdk/grpc/auth/v1"
	"github.com/go42-dev/go42/tests/integration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const grpcRequestTimeout = 10 * time.Second

var _ = Describe("Auth gRPC Integration Tests", func() {
	var (
		conn      *grpc.ClientConn
		client    pb.AuthServiceClient
		ctx       context.Context
		cancelCtx context.CancelFunc
	)

	BeforeEach(func() {
		var err error
		conn, err = grpc.NewClient(
			integration.GRPCServerAddress(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithDisableServiceConfig(),
		)
		Expect(err).NotTo(HaveOccurred())
		client = pb.NewAuthServiceClient(conn)
		apiKey := integration.GRPCAPIKey()
		Expect(apiKey).NotTo(BeEmpty(), "GRPC_API_KEY must contain the integration API key")
		ctx, cancelCtx = context.WithTimeout(
			metadata.AppendToOutgoingContext(context.Background(), "x-api-key", apiKey),
			grpcRequestTimeout,
		)
	})

	AfterEach(func() {
		if cancelCtx != nil {
			cancelCtx()
		}
		if conn != nil {
			conn.Close()
		}
	})

	Describe("User Management Service", func() {
		Describe("ListUsers", func() {
			It("should reject a request without authentication", func() {
				unauthenticatedCtx, cancel := context.WithTimeout(context.Background(), grpcRequestTimeout)
				defer cancel()

				_, err := client.ListUsers(unauthenticatedCtx, &pb.ListUsersRequest{})
				Expect(err).To(HaveOccurred())

				st, ok := status.FromError(err)
				Expect(ok).To(BeTrue())
				Expect(st.Code()).To(Equal(codes.Unauthenticated))
			})

			It("should list users", func() {
				req := &pb.ListUsersRequest{
					Limit:  10,
					Offset: 0,
				}

				resp, err := client.ListUsers(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp).NotTo(BeNil())
				Expect(resp.Users).NotTo(BeNil())
			})
		})

		Describe("GetUserByUUID", func() {
			It("should retrieve an existing user", func() {
				email := fmt.Sprintf("read-%s@example.com", integration.GenerateRandomString("user"))
				created, err := client.CreateUser(ctx, &pb.CreateUserRequest{
					Email: email, Password: "TestPass123!",
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(created.User).NotTo(BeNil())
				defer func() {
					_, cleanupErr := client.DeleteUser(ctx, &pb.DeleteUserRequest{Uuid: created.User.Uuid})
					Expect(cleanupErr).NotTo(HaveOccurred())
				}()

				response, err := client.GetUserByUUID(ctx, &pb.GetUserByUUIDRequest{Uuid: created.User.Uuid})
				Expect(err).NotTo(HaveOccurred())
				Expect(response.User).NotTo(BeNil())
				Expect(response.User.Uuid).To(Equal(created.User.Uuid))
				Expect(response.User.Email).To(Equal(email))
			})

			It("should return InvalidArgument for invalid UUID", func() {
				req := &pb.GetUserByUUIDRequest{
					Uuid: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
				}

				_, err := client.GetUserByUUID(ctx, req)
				Expect(err).To(HaveOccurred())

				st, ok := status.FromError(err)
				Expect(ok).To(BeTrue())
				Expect(st.Code()).To(Equal(codes.InvalidArgument))
			})

			It("should return NotFound for non-existent user", func() {
				req := &pb.GetUserByUUIDRequest{
					Uuid: "123e4567-e89b-12d3-a456-426614174000", // Valid UUID format but doesn't exist
				}

				_, err := client.GetUserByUUID(ctx, req)
				Expect(err).To(HaveOccurred())

				st, ok := status.FromError(err)
				Expect(ok).To(BeTrue())
				Expect(st.Code()).To(Equal(codes.NotFound))
			})
		})

		Describe("CreateUser", func() {
			var createdUserUUID string

			AfterEach(func() {
				if createdUserUUID != "" {
					// Cleanup
					req := &pb.DeleteUserRequest{
						Uuid: createdUserUUID,
					}
					_, err := client.DeleteUser(ctx, req)
					Expect(err).NotTo(HaveOccurred())
					createdUserUUID = ""
				}
			})

			It("should create a new user", func() {
				newEmail := fmt.Sprintf("new-%s@example.com", integration.GenerateRandomString("user"))
				req := &pb.CreateUserRequest{
					Email:    newEmail,
					Password: "TestPass123!",
				}

				resp, err := client.CreateUser(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp).NotTo(BeNil())
				Expect(resp.User).NotTo(BeNil())
				Expect(resp.User.Email).To(Equal(newEmail))
				createdUserUUID = resp.User.Uuid
			})

			It("should validate email format", func() {
				req := &pb.CreateUserRequest{
					Email:    "invalid-email",
					Password: "TestPass123!",
				}

				_, err := client.CreateUser(ctx, req)
				Expect(err).To(HaveOccurred())

				st, ok := status.FromError(err)
				Expect(ok).To(BeTrue())
				Expect(st.Code()).To(Equal(codes.InvalidArgument))
			})

			It("should validate password length", func() {
				req := &pb.CreateUserRequest{
					Email:    fmt.Sprintf("test-%s@example.com", integration.GenerateRandomString("user")),
					Password: "short",
				}

				_, err := client.CreateUser(ctx, req)
				Expect(err).To(HaveOccurred())

				st, ok := status.FromError(err)
				Expect(ok).To(BeTrue())
				Expect(st.Code()).To(Equal(codes.InvalidArgument))
			})
		})

		Describe("UpdateUser", func() {
			var createdUserUUID string

			BeforeEach(func() {
				// Create a user to update
				newEmail := fmt.Sprintf("update-test-%s@example.com", integration.GenerateRandomString("user"))
				req := &pb.CreateUserRequest{
					Email:    newEmail,
					Password: "TestPass123!",
				}

				resp, err := client.CreateUser(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp).NotTo(BeNil())
				Expect(resp.User).NotTo(BeNil())
				createdUserUUID = resp.User.Uuid
			})

			AfterEach(func() {
				if createdUserUUID != "" {
					// Cleanup
					req := &pb.DeleteUserRequest{
						Uuid: createdUserUUID,
					}
					_, err := client.DeleteUser(ctx, req)
					Expect(err).NotTo(HaveOccurred())
				}
			})

			It("should update user email", func() {
				newEmail := fmt.Sprintf("updated-%s@example.com", integration.GenerateRandomString("user"))
				req := &pb.UpdateUserRequest{
					Uuid:  createdUserUUID,
					Email: &newEmail,
				}

				resp, err := client.UpdateUser(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp).NotTo(BeNil())
			})

			It("should return InvalidArgument for invalid UUID", func() {
				newEmail := fmt.Sprintf("updated-%s@example.com", integration.GenerateRandomString("user"))
				req := &pb.UpdateUserRequest{
					Uuid:  "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
					Email: &newEmail,
				}

				_, err := client.UpdateUser(ctx, req)
				Expect(err).To(HaveOccurred())

				st, ok := status.FromError(err)
				Expect(ok).To(BeTrue())
				Expect(st.Code()).To(Equal(codes.InvalidArgument))
			})

			It("should validate email format when provided", func() {
				invalidEmail := "invalid-email"
				req := &pb.UpdateUserRequest{
					Uuid:  createdUserUUID,
					Email: &invalidEmail,
				}

				_, err := client.UpdateUser(ctx, req)
				Expect(err).To(HaveOccurred())

				st, ok := status.FromError(err)
				Expect(ok).To(BeTrue())
				Expect(st.Code()).To(Equal(codes.InvalidArgument))
			})
		})

		Describe("DeleteUser", func() {
			It("should delete an existing user", func() {
				// Create a user to delete
				newEmail := fmt.Sprintf("delete-test-%s@example.com", integration.GenerateRandomString("user"))
				createReq := &pb.CreateUserRequest{
					Email:    newEmail,
					Password: "TestPass123!",
				}

				createResp, err := client.CreateUser(ctx, createReq)
				Expect(err).NotTo(HaveOccurred())
				Expect(createResp).NotTo(BeNil())
				Expect(createResp.User).NotTo(BeNil())

				// Delete the user
				deleteReq := &pb.DeleteUserRequest{
					Uuid: createResp.User.Uuid,
				}

				resp, err := client.DeleteUser(ctx, deleteReq)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp).NotTo(BeNil())

				// Verify deletion
				getReq := &pb.GetUserByUUIDRequest{
					Uuid: createResp.User.Uuid,
				}
				_, err = client.GetUserByUUID(ctx, getReq)
				Expect(err).To(HaveOccurred())

				st, ok := status.FromError(err)
				Expect(ok).To(BeTrue())
				Expect(st.Code()).To(Equal(codes.NotFound))
			})

			It("should return InvalidArgument for invalid UUID", func() {
				req := &pb.DeleteUserRequest{
					Uuid: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
				}

				_, err := client.DeleteUser(ctx, req)
				Expect(err).To(HaveOccurred())

				st, ok := status.FromError(err)
				Expect(ok).To(BeTrue())
				Expect(st.Code()).To(Equal(codes.InvalidArgument))
			})

			It("should return NotFound for non-existent user", func() {
				req := &pb.DeleteUserRequest{
					Uuid: "123e4567-e89b-12d3-a456-426614174000",
				}

				_, err := client.DeleteUser(ctx, req)
				Expect(err).To(HaveOccurred())

				st, ok := status.FromError(err)
				Expect(ok).To(BeTrue())
				Expect(st.Code()).To(Equal(codes.NotFound))
			})
		})
	})
})

func TestAuthGRPCIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Auth gRPC Integration Suite")
}
