package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	oapi "github.com/go42-dev/go42/api/gen/sdk/http/v1/auth/oapi-codegen"
	"github.com/go42-dev/go42/tests/integration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = BeforeSuite(func() {
	Expect(integration.HTTPAPIKey()).ToNot(BeEmpty(),
		"Set HTTP_API_KEY to a key with users:list, users:read_others, users:create, users:update, and users:delete. "+
			"See tests/integration/README.md for external-server setup.")
})

var _ = Describe("Administrative User Endpoints", func() {
	for _, clientCase := range []struct {
		name string
		new  func(credentials) usersClient
	}{
		{name: "ogen", new: newOgenUsers},
		{name: "oapi-codegen", new: newOAPIUsers},
	} {
		Describe(clientCase.name, Label(clientCase.name), func() {
			var admin usersClient

			BeforeEach(func() {
				admin = clientCase.new(credentials{apiKey: integration.HTTPAPIKey()})
			})

			It("lists users with pagination", Label("crud", "list"), func(ctx SpecContext) {
				var targets []User
				for range 3 {
					targets = append(targets, createUserFixture(ctx, admin))
				}

				const limit = 2
				seen := make(map[string]string)
				for offset := 0; ; offset += limit {
					result := admin.list(ctx, limit, offset)
					Expect(result.status).To(Equal(http.StatusOK))
					Expect(len(result.users)).To(BeNumerically("<=", limit))
					for _, user := range result.users {
						Expect(seen).ToNot(HaveKey(user.UUID), "pagination repeated a user")
						seen[user.UUID] = user.Email
					}
					if len(result.users) < limit {
						break
					}
				}
				for _, target := range targets {
					Expect(seen).To(HaveKeyWithValue(target.UUID, target.Email))
				}
			}, SpecTimeout(30*time.Second))

			It(
				"creates a user with a valid UUID and the requested email",
				Label("crud", "create"),
				func(ctx SpecContext) {
					createUserFixture(ctx, admin)
				},
			)

			It("gets the requested user", Label("crud", "get"), func(ctx SpecContext) {
				target := createUserFixture(ctx, admin)
				result := admin.get(ctx, userID(target))
				Expect(result.status).To(Equal(http.StatusOK))
				Expect(result.user).To(Equal(target))
			})

			It("updates a user and persists the changed email", Label("crud", "update"), func(ctx SpecContext) {
				target := createUserFixture(ctx, admin)
				email := uniqueEmail()
				Expect(admin.update(ctx, userID(target), email).status).To(Equal(http.StatusOK))

				result := admin.get(ctx, userID(target))
				Expect(result.status).To(Equal(http.StatusOK))
				Expect(result.user).To(Equal(User{UUID: target.UUID, Email: email}))
			})

			It("deletes a user and makes subsequent reads return 404", Label("crud", "delete"), func(ctx SpecContext) {
				target := createUserFixture(ctx, admin)
				Expect(admin.delete(ctx, userID(target)).status).To(Equal(http.StatusOK))
				Expect(admin.get(ctx, userID(target)).status).To(Equal(http.StatusNotFound))
			})

			DescribeTable("denies ordinary JWTs with 403", func(ctx SpecContext, operation string) {
				_, token := signUpUser(ctx, admin)
				target := createUserFixture(ctx, admin)
				ordinary := clientCase.new(credentials{token: token})
				result := userOperation(ctx, ordinary, operation, target)
				Expect(result.status).To(Equal(http.StatusForbidden))
				if operation == "update" || operation == "delete" {
					expectUnchangedUser(ctx, admin, target)
				}
			},
				Entry("list", "list"),
				Entry("create", "create"),
				Entry("get", "get"),
				Entry("update", "update"),
				Entry("delete", "delete"),
			)
		})
	}
})

var _ = Describe("HTTP authorization and validation", func() {
	var admin usersClient

	BeforeEach(func() {
		admin = newOAPIUsers(credentials{apiKey: integration.HTTPAPIKey()})
	})

	// Ogen requires credentials and typed UUIDs before sending a request. Raw HTTP
	// lets these cases reach the server with absent credentials or malformed input.
	DescribeTable("rejects requests without credentials with 401", func(ctx SpecContext, operation string) {
		target := createUserFixture(ctx, admin)
		method, path, body := httpUserOperation(operation, target)
		expectHTTPProblem(ctx, method, path, body, "", http.StatusUnauthorized)
		if operation == "update" || operation == "delete" {
			expectUnchangedUser(ctx, admin, target)
		}
	},
		Entry("list", "list"),
		Entry("create", "create"),
		Entry("get", "get"),
		Entry("update", "update"),
		Entry("delete", "delete"),
	)

	It("rejects an invalid UUID with 400 for the privileged API key", func(ctx SpecContext) {
		expectHTTPProblem(ctx, http.MethodGet, "/users/invalid-uuid", nil,
			integration.HTTPAPIKey(), http.StatusBadRequest)
	})
})

func uniqueEmail() string {
	return "integration-" + uuid.NewString() + "@example.com"
}

func userID(user User) uuid.UUID {
	GinkgoHelper()
	id, err := uuid.Parse(user.UUID)
	Expect(err).ToNot(HaveOccurred())
	Expect(id).ToNot(Equal(uuid.Nil))
	return id
}

func createUserFixture(ctx context.Context, admin usersClient) User {
	GinkgoHelper()
	email := uniqueEmail()
	result := admin.create(ctx, email)
	Expect(
		result.status,
	).To(Equal(http.StatusCreated), "administrative fixture creation failed; check HTTP_API_KEY permissions")
	deferUserCleanup(ctx, admin, result.user)
	Expect(result.user.Email).To(Equal(email))
	return result.user
}

func deferUserCleanup(ctx context.Context, admin usersClient, user User) {
	GinkgoHelper()
	id := userID(user)
	// The spec's context may be canceled before cleanup runs.
	ctx = context.WithoutCancel(ctx)
	DeferCleanup(func() {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		result := admin.get(ctx, id)
		if result.status == http.StatusNotFound {
			return // The delete spec already removed its fixture.
		}
		Expect(result.status).To(Equal(http.StatusOK))
		Expect(admin.delete(ctx, id).status).To(Equal(http.StatusOK))
	})
}

func signUpUser(ctx context.Context, admin usersClient) (User, string) {
	GinkgoHelper()
	client := newOAPIClient(credentials{})
	email := uniqueEmail()
	signup, err := client.SignupWithResponse(ctx, oapi.SignUpRequest{Email: email, Password: fixturePassword})
	Expect(err).ToNot(HaveOccurred())
	if signup == nil {
		Fail("signup returned no response")
		return User{}, ""
	}
	Expect(signup.StatusCode()).To(Equal(http.StatusCreated), "signup setup failed: %s", signup.Body)
	user := userFromOAPI(signup.JSON201)
	deferUserCleanup(ctx, admin, user)
	Expect(user.Email).To(Equal(email))

	login, err := client.LoginWithResponse(ctx, oapi.LoginRequest{Email: email, Password: fixturePassword})
	Expect(err).ToNot(HaveOccurred())
	if login == nil {
		Fail("login returned no response")
		return User{}, ""
	}
	Expect(login.StatusCode()).To(Equal(http.StatusOK), "login setup failed: %s", login.Body)
	Expect(login.JSON200).ToNot(BeNil())
	Expect(login.JSON200.AccessToken).ToNot(BeNil())
	Expect(*login.JSON200.AccessToken).ToNot(BeEmpty())
	return user, *login.JSON200.AccessToken
}

func expectUnchangedUser(ctx context.Context, admin usersClient, target User) {
	GinkgoHelper()
	result := admin.get(ctx, userID(target))
	Expect(result.status).To(Equal(http.StatusOK))
	Expect(result.user).To(Equal(target))
}

func userOperation(ctx context.Context, client usersClient, operation string, target User) usersResult {
	GinkgoHelper()
	switch operation {
	case "list":
		return client.list(ctx, 10, 0)
	case "create":
		return client.create(ctx, uniqueEmail())
	case "get":
		return client.get(ctx, userID(target))
	case "update":
		return client.update(ctx, userID(target), uniqueEmail())
	case "delete":
		return client.delete(ctx, userID(target))
	default:
		Fail("unknown user operation: " + operation)
		return usersResult{}
	}
}

func httpUserOperation(operation string, target User) (method, path string, body any) {
	GinkgoHelper()
	switch operation {
	case "list":
		return http.MethodGet, "/users?limit=10&offset=0", nil
	case "create":
		return http.MethodPost, "/users", oapi.CreateUserRequest{Email: uniqueEmail(), Password: fixturePassword}
	case "get":
		return http.MethodGet, "/users/" + target.UUID, nil
	case "update":
		email := uniqueEmail()
		return http.MethodPut, "/users/" + target.UUID, oapi.UpdateUserRequest{Email: &email}
	case "delete":
		return http.MethodDelete, "/users/" + target.UUID, nil
	default:
		Fail("unknown user operation: " + operation)
		return "", "", nil
	}
}

func expectHTTPProblem(ctx context.Context, method, path string, body any, apiKey string, status int) {
	GinkgoHelper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		Expect(err).ToNot(HaveOccurred())
	}
	request, err := http.NewRequestWithContext(ctx, method,
		integration.HTTPServerAddress()+"/api/v1"+path, bytes.NewReader(payload))
	Expect(err).ToNot(HaveOccurred())
	if request == nil {
		Fail("could not create HTTP request")
		return
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		request.Header.Set("X-API-Key", apiKey)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	Expect(err).ToNot(HaveOccurred())
	if response == nil {
		Fail("HTTP request returned no response")
		return
	}
	defer func() {
		Expect(response.Body.Close()).To(Succeed())
	}()
	data, err := io.ReadAll(response.Body)
	Expect(err).ToNot(HaveOccurred())
	Expect(response.StatusCode).To(Equal(status), "%s %s: %s", method, path, data)
	Expect(response.Header.Get("Content-Type")).To(ContainSubstring("application/problem+json"))
	var problem oapi.Error
	Expect(json.Unmarshal(data, &problem)).To(Succeed())
	Expect(problem.Status).To(Equal(int32(status)))
}
