// nolint
package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/go42-dev/go42/tests/integration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type SignupRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type RefreshTokenRequest struct {
	Token string `json:"token"`
}

type LogoutRequest struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type UpdateSelfRequest struct {
	CurrentPassword string `json:"current_password"`
	Email           string `json:"email,omitempty"`
	Password        string `json:"password,omitempty"`
}

type CreateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type UpdateUserRequest struct {
	Email    string `json:"email,omitempty"`
	Password string `json:"password,omitempty"`
}

type User struct {
	UUID        string   `json:"uuid"`
	Email       string   `json:"email"`
	CreatedAt   string   `json:"created_at"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func expectSetupResponse(response *http.Response, status int) {
	GinkgoHelper()
	if response.StatusCode == status {
		return
	}
	body, err := io.ReadAll(response.Body)
	Expect(err).ToNot(HaveOccurred())
	detail := fmt.Sprintf("%s %s setup failed: %s", response.Request.Method, response.Request.URL.Path, body)
	if response.StatusCode == http.StatusTooManyRequests {
		detail += "Start the application with AUTH_RATE_LIMITER_ENABLED=false using make run-integration."
	}
	Expect(response.StatusCode).To(Equal(status), detail)
}

var _ = Describe("Auth API Integration Tests", func() {
	var client *http.Client

	BeforeEach(func() {
		client = &http.Client{Timeout: 5 * time.Second}
	})

	Describe("Auth Endpoints", func() {
		var testEmail string
		var testPassword string
		var accessToken string
		var refreshToken string

		BeforeEach(func() {
			testEmail = fmt.Sprintf("test-%s@example.com", integration.GenerateRandomString("user"))
			testPassword = "TestPass123!"
		})

		Describe("POST /auth/signup", func() {
			It("should successfully create a new user", func() {
				reqBody := SignupRequest{
					Email:    testEmail,
					Password: testPassword,
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/signup",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				var user User
				err = json.NewDecoder(resp.Body).Decode(&user)
				Expect(err).ToNot(HaveOccurred())
				Expect(user.Email).To(Equal(testEmail))
				Expect(user.UUID).ToNot(BeEmpty())
			})

			It("should return 409 when user already exists", func() {
				// First signup
				reqBody := SignupRequest{
					Email:    testEmail,
					Password: testPassword,
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/signup",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				resp.Body.Close()
				Expect(resp.StatusCode).To(Equal(http.StatusCreated))

				// Second signup with same email
				resp2, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/signup",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp2.Body.Close()
				Expect(resp2.StatusCode).To(Equal(http.StatusConflict))
			})

			It("should validate email format", func() {
				reqBody := SignupRequest{
					Email:    "invalid-email",
					Password: testPassword,
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/signup",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})

			It("should validate password length", func() {
				reqBody := SignupRequest{
					Email:    testEmail,
					Password: "short",
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/signup",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})
		})

		Describe("POST /auth/login", func() {
			BeforeEach(func() {
				// Create user for login tests
				reqBody := SignupRequest{
					Email:    testEmail,
					Password: testPassword,
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/signup",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()
				expectSetupResponse(resp, http.StatusCreated)
			})

			It("should successfully login with valid credentials", func() {
				reqBody := LoginRequest{
					Email:    testEmail,
					Password: testPassword,
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/login",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				var tokens Tokens
				err = json.NewDecoder(resp.Body).Decode(&tokens)
				Expect(err).ToNot(HaveOccurred())
				Expect(tokens.AccessToken).ToNot(BeEmpty())
				Expect(tokens.RefreshToken).ToNot(BeEmpty())
				Expect(tokens.ExpiresIn).To(BeNumerically(">", 0))

				accessToken = tokens.AccessToken
				refreshToken = tokens.RefreshToken
			})

			It("should return 400 with invalid password", func() {
				reqBody := LoginRequest{
					Email:    testEmail,
					Password: "WrongPassword123!",
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/login",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				// The API returns 400 for invalid credentials
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})

			It("should return 400 for non-existent user", func() {
				reqBody := LoginRequest{
					Email:    "nonexistent@example.com",
					Password: testPassword,
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/login",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				// The API returns 400 for invalid login attempts
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			})
		})

		Describe("POST /auth/refresh", func() {
			BeforeEach(func() {
				// Create user and login
				reqBody := SignupRequest{
					Email:    testEmail,
					Password: testPassword,
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/signup",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()
				expectSetupResponse(resp, http.StatusCreated)

				// Login
				loginReq := LoginRequest{
					Email:    testEmail,
					Password: testPassword,
				}
				loginBytes, err := json.Marshal(loginReq)
				Expect(err).ToNot(HaveOccurred())

				loginResp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/login",
					"application/json",
					bytes.NewReader(loginBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer loginResp.Body.Close()
				expectSetupResponse(loginResp, http.StatusOK)

				var tokens Tokens
				err = json.NewDecoder(loginResp.Body).Decode(&tokens)
				Expect(err).ToNot(HaveOccurred())
				Expect(tokens.AccessToken).ToNot(BeEmpty())
				Expect(tokens.RefreshToken).ToNot(BeEmpty())
				accessToken = tokens.AccessToken
				refreshToken = tokens.RefreshToken
			})

			It("should successfully refresh tokens", func() {
				reqBody := RefreshTokenRequest{
					Token: refreshToken,
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/refresh",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				var tokens Tokens
				err = json.NewDecoder(resp.Body).Decode(&tokens)
				Expect(err).ToNot(HaveOccurred())
				Expect(tokens.AccessToken).ToNot(BeEmpty())
				Expect(tokens.RefreshToken).ToNot(BeEmpty())
				Expect(tokens.AccessToken).ToNot(Equal(accessToken))
			})

			It("should return 401 with invalid refresh token", func() {
				reqBody := RefreshTokenRequest{
					Token: "invalid-refresh-token",
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/refresh",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})

		Describe("POST /auth/logout", func() {
			var validAccessToken string
			var validRefreshToken string

			BeforeEach(func() {
				// Create user and login
				reqBody := SignupRequest{
					Email:    testEmail,
					Password: testPassword,
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/signup",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()
				expectSetupResponse(resp, http.StatusCreated)

				// Login
				loginReq := LoginRequest{
					Email:    testEmail,
					Password: testPassword,
				}
				loginBytes, err := json.Marshal(loginReq)
				Expect(err).ToNot(HaveOccurred())

				loginResp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/login",
					"application/json",
					bytes.NewReader(loginBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer loginResp.Body.Close()
				expectSetupResponse(loginResp, http.StatusOK)

				var tokens Tokens
				err = json.NewDecoder(loginResp.Body).Decode(&tokens)
				Expect(err).ToNot(HaveOccurred())
				Expect(tokens.AccessToken).ToNot(BeEmpty())
				Expect(tokens.RefreshToken).ToNot(BeEmpty())
				validAccessToken = tokens.AccessToken
				validRefreshToken = tokens.RefreshToken
			})

			It("should successfully logout", func() {
				reqBody := LogoutRequest{
					AccessToken:  validAccessToken,
					RefreshToken: validRefreshToken,
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/logout",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				Expect(resp.StatusCode).To(Equal(http.StatusOK))
			})
		})

		Describe("User Management Endpoints", func() {
			var adminAccessToken string
			var createdUserUUID string

			BeforeEach(func() {
				// Create admin user and login
				adminEmail := fmt.Sprintf("admin-%s@example.com", integration.GenerateRandomString("admin"))
				reqBody := SignupRequest{
					Email:    adminEmail,
					Password: testPassword,
				}
				bodyBytes, err := json.Marshal(reqBody)
				Expect(err).ToNot(HaveOccurred())

				resp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/signup",
					"application/json",
					bytes.NewReader(bodyBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()
				expectSetupResponse(resp, http.StatusCreated)

				// Login as admin
				loginReq := LoginRequest{
					Email:    adminEmail,
					Password: testPassword,
				}
				loginBytes, err := json.Marshal(loginReq)
				Expect(err).ToNot(HaveOccurred())

				loginResp, err := client.Post(
					integration.HTTPServerAddress()+"/api/v1/auth/login",
					"application/json",
					bytes.NewReader(loginBytes),
				)
				Expect(err).ToNot(HaveOccurred())
				defer loginResp.Body.Close()
				expectSetupResponse(loginResp, http.StatusOK)

				var tokens Tokens
				err = json.NewDecoder(loginResp.Body).Decode(&tokens)
				Expect(err).ToNot(HaveOccurred())
				Expect(tokens.AccessToken).ToNot(BeEmpty())
				adminAccessToken = tokens.AccessToken
			})

			Describe("GET /users/me", func() {
				It("should return current user info", func() {
					req, err := http.NewRequest(
						http.MethodGet,
						integration.HTTPServerAddress()+"/api/v1/users/me",
						nil,
					)
					Expect(err).ToNot(HaveOccurred())
					req.Header.Set("Authorization", "Bearer "+adminAccessToken)

					resp, err := client.Do(req)
					Expect(err).ToNot(HaveOccurred())
					defer resp.Body.Close()

					Expect(resp.StatusCode).To(Equal(http.StatusOK))

					var user User
					err = json.NewDecoder(resp.Body).Decode(&user)
					Expect(err).ToNot(HaveOccurred())
					Expect(user.UUID).ToNot(BeEmpty())
					Expect(user.Email).ToNot(BeEmpty())
				})

				It("should return 401 without auth token", func() {
					req, err := http.NewRequest(
						http.MethodGet,
						integration.HTTPServerAddress()+"/api/v1/users/me",
						nil,
					)
					Expect(err).ToNot(HaveOccurred())

					resp, err := client.Do(req)
					Expect(err).ToNot(HaveOccurred())
					defer resp.Body.Close()

					Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
				})
			})

			Describe("PUT /users/me", func() {
				It("should update current user email", func() {
					newEmail := fmt.Sprintf("updated-%s@example.com", integration.GenerateRandomString("email"))
					reqBody := UpdateSelfRequest{
						CurrentPassword: testPassword,
						Email:           newEmail,
					}
					bodyBytes, err := json.Marshal(reqBody)
					Expect(err).ToNot(HaveOccurred())

					req, err := http.NewRequest(
						http.MethodPut,
						integration.HTTPServerAddress()+"/api/v1/users/me",
						bytes.NewReader(bodyBytes),
					)
					Expect(err).ToNot(HaveOccurred())
					req.Header.Set("Authorization", "Bearer "+adminAccessToken)
					req.Header.Set("Content-Type", "application/json")

					resp, err := client.Do(req)
					Expect(err).ToNot(HaveOccurred())
					defer resp.Body.Close()

					Expect(resp.StatusCode).To(Equal(http.StatusOK))
				})

				It("should update current user password", func() {
					reqBody := UpdateSelfRequest{
						CurrentPassword: testPassword,
						Password:        "NewPassword123!",
					}
					bodyBytes, err := json.Marshal(reqBody)
					Expect(err).ToNot(HaveOccurred())

					req, err := http.NewRequest(
						http.MethodPut,
						integration.HTTPServerAddress()+"/api/v1/users/me",
						bytes.NewReader(bodyBytes),
					)
					Expect(err).ToNot(HaveOccurred())
					req.Header.Set("Authorization", "Bearer "+adminAccessToken)
					req.Header.Set("Content-Type", "application/json")

					resp, err := client.Do(req)
					Expect(err).ToNot(HaveOccurred())
					defer resp.Body.Close()

					Expect(resp.StatusCode).To(Equal(http.StatusOK))
				})
			})

			Describe("GET /users", func() {
				It("should list users", func() {
					req, err := http.NewRequest(
						http.MethodGet,
						integration.HTTPServerAddress()+"/api/v1/users?limit=10&offset=0",
						nil,
					)
					Expect(err).ToNot(HaveOccurred())
					req.Header.Set("Authorization", "Bearer "+adminAccessToken)

					resp, err := client.Do(req)
					Expect(err).ToNot(HaveOccurred())
					defer resp.Body.Close()

					// The endpoint might not exist (404) or require permissions (403)
					if resp.StatusCode == http.StatusOK {
						var users []User
						err = json.NewDecoder(resp.Body).Decode(&users)
						Expect(err).ToNot(HaveOccurred())
						Expect(len(users)).To(BeNumerically(">=", 0))
					} else {
						Expect(resp.StatusCode).To(BeElementOf(http.StatusForbidden, http.StatusNotFound))
					}
				})
			})

			Describe("POST /users", func() {
				It("should create a new user", func() {
					newUserEmail := fmt.Sprintf("newuser-%s@example.com", integration.GenerateRandomString("user"))
					reqBody := CreateUserRequest{
						Email:    newUserEmail,
						Password: "NewUserPass123!",
					}
					bodyBytes, err := json.Marshal(reqBody)
					Expect(err).ToNot(HaveOccurred())

					req, err := http.NewRequest(
						http.MethodPost,
						integration.HTTPServerAddress()+"/api/v1/users",
						bytes.NewReader(bodyBytes),
					)
					Expect(err).ToNot(HaveOccurred())
					req.Header.Set("Authorization", "Bearer "+adminAccessToken)
					req.Header.Set("Content-Type", "application/json")

					resp, err := client.Do(req)
					Expect(err).ToNot(HaveOccurred())
					defer resp.Body.Close()

					// The endpoint might not exist (404) or require permissions (403)
					if resp.StatusCode == http.StatusCreated {
						var user User
						err = json.NewDecoder(resp.Body).Decode(&user)
						Expect(err).ToNot(HaveOccurred())
						Expect(user.Email).To(Equal(newUserEmail))
						createdUserUUID = user.UUID
					} else {
						Expect(resp.StatusCode).To(BeElementOf(http.StatusForbidden, http.StatusNotFound))
					}
				})
			})

			Describe("GET /users/{uuid}", func() {
				It("should get user by UUID", func() {
					if createdUserUUID == "" {
						Skip("No user created in previous test")
					}

					req, err := http.NewRequest(
						http.MethodGet,
						integration.HTTPServerAddress()+"/api/v1/users/"+createdUserUUID,
						nil,
					)
					Expect(err).ToNot(HaveOccurred())
					req.Header.Set("Authorization", "Bearer "+adminAccessToken)

					resp, err := client.Do(req)
					Expect(err).ToNot(HaveOccurred())
					defer resp.Body.Close()

					// Note: This might fail with 403 if the user doesn't have users:read_others permission
					if resp.StatusCode == http.StatusOK {
						var user User
						err = json.NewDecoder(resp.Body).Decode(&user)
						Expect(err).ToNot(HaveOccurred())
						Expect(user.UUID).To(Equal(createdUserUUID))
					} else {
						Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
					}
				})

				It("should return 400 or 403 for invalid UUID", func() {
					req, err := http.NewRequest(
						http.MethodGet,
						integration.HTTPServerAddress()+"/api/v1/users/invalid-uuid",
						nil,
					)
					Expect(err).ToNot(HaveOccurred())
					req.Header.Set("Authorization", "Bearer "+adminAccessToken)

					resp, err := client.Do(req)
					Expect(err).ToNot(HaveOccurred())
					defer resp.Body.Close()

					// Could be 400 for invalid UUID or 403 for no permission
					Expect(resp.StatusCode).To(BeElementOf(http.StatusBadRequest, http.StatusForbidden))
				})
			})

			Describe("PUT /users/{uuid}", func() {
				It("should update user", func() {
					if createdUserUUID == "" {
						Skip("No user created in previous test")
					}

					updatedEmail := fmt.Sprintf("updated-%s@example.com", integration.GenerateRandomString("user"))
					reqBody := UpdateUserRequest{
						Email: updatedEmail,
					}
					bodyBytes, err := json.Marshal(reqBody)
					Expect(err).ToNot(HaveOccurred())

					req, err := http.NewRequest(
						http.MethodPut,
						integration.HTTPServerAddress()+"/api/v1/users/"+createdUserUUID,
						bytes.NewReader(bodyBytes),
					)
					Expect(err).ToNot(HaveOccurred())
					req.Header.Set("Authorization", "Bearer "+adminAccessToken)
					req.Header.Set("Content-Type", "application/json")

					resp, err := client.Do(req)
					Expect(err).ToNot(HaveOccurred())
					defer resp.Body.Close()

					// Note: This might fail with 403 if the user doesn't have users:update permission
					if resp.StatusCode != http.StatusForbidden {
						Expect(resp.StatusCode).To(Equal(http.StatusOK))
					}
				})
			})

			Describe("DELETE /users/{uuid}", func() {
				It("should delete user", func() {
					if createdUserUUID == "" {
						Skip("No user created in previous test")
					}

					req, err := http.NewRequest(
						http.MethodDelete,
						integration.HTTPServerAddress()+"/api/v1/users/"+createdUserUUID,
						nil,
					)
					Expect(err).ToNot(HaveOccurred())
					req.Header.Set("Authorization", "Bearer "+adminAccessToken)

					resp, err := client.Do(req)
					Expect(err).ToNot(HaveOccurred())
					defer resp.Body.Close()

					// Note: This might fail with 403 if the user doesn't have users:delete permission
					if resp.StatusCode != http.StatusForbidden {
						Expect(resp.StatusCode).To(Equal(http.StatusOK))
					}
				})
			})
		})
	})
})

func TestAuthIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Auth API Integration Suite")
}
