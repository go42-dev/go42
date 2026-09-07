package test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/ogen-go/ogen/ogenerrors"

	oapi "github.com/go42-dev/go42/api/gen/sdk/http/v1/auth/oapi-codegen"
	ogen "github.com/go42-dev/go42/api/gen/sdk/http/v1/auth/ogen"
	"github.com/go42-dev/go42/tests/integration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const fixturePassword = "TestPass123!"

// Keep the behavior specs shared while checking each generated client's decoded responses.
type usersClient interface {
	list(context.Context, int, int) usersResult
	create(context.Context, string) usersResult
	get(context.Context, uuid.UUID) usersResult
	update(context.Context, uuid.UUID, string) usersResult
	delete(context.Context, uuid.UUID) usersResult
}

type usersResult struct {
	status int
	user   User
	users  []User
}

type credentials struct {
	apiKey string
	token  string
}

func (c credentials) ApiKey(context.Context, ogen.OperationName, *ogen.Client) (ogen.ApiKey, error) {
	if c.apiKey == "" {
		return ogen.ApiKey{}, ogenerrors.ErrSkipClientSecurity
	}
	return ogen.ApiKey{APIKey: c.apiKey}, nil
}

func (c credentials) Jwt(context.Context, ogen.OperationName, *ogen.Client) (ogen.Jwt, error) {
	if c.token == "" {
		return ogen.Jwt{}, ogenerrors.ErrSkipClientSecurity
	}
	return ogen.Jwt{Token: c.token}, nil
}

func newOAPIClient(auth credentials) *oapi.ClientWithResponses {
	GinkgoHelper()
	client, err := oapi.NewClientWithResponses(
		integration.HTTPServerAddress()+"/api/v1",
		oapi.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}),
		oapi.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
			if auth.apiKey != "" {
				request.Header.Set("X-API-Key", auth.apiKey)
			}
			if auth.token != "" {
				request.Header.Set("Authorization", "Bearer "+auth.token)
			}
			return nil
		}),
	)
	Expect(err).ToNot(HaveOccurred())
	return client
}

type oapiUsers struct{ client *oapi.ClientWithResponses }

func newOAPIUsers(auth credentials) usersClient {
	return oapiUsers{client: newOAPIClient(auth)}
}

func (c oapiUsers) list(ctx context.Context, limit, offset int) usersResult {
	GinkgoHelper()
	return decodeOAPI(c.client.UsersListWithResponse(ctx, &oapi.UsersListParams{Limit: &limit, Offset: &offset}))
}

func (c oapiUsers) create(ctx context.Context, email string) usersResult {
	GinkgoHelper()
	return decodeOAPI(c.client.UsersCreateWithResponse(ctx, oapi.CreateUserRequest{
		Email: email, Password: fixturePassword,
	}))
}

func (c oapiUsers) get(ctx context.Context, id uuid.UUID) usersResult {
	GinkgoHelper()
	return decodeOAPI(c.client.UsersGetWithResponse(ctx, id))
}

func (c oapiUsers) update(ctx context.Context, id uuid.UUID, email string) usersResult {
	GinkgoHelper()
	return decodeOAPI(c.client.UsersUpdateWithResponse(ctx, id, oapi.UpdateUserRequest{Email: &email}))
}

func (c oapiUsers) delete(ctx context.Context, id uuid.UUID) usersResult {
	GinkgoHelper()
	return decodeOAPI(c.client.UsersDeleteWithResponse(ctx, id))
}

type oapiResponse interface {
	StatusCode() int
	GetBody() []byte
	GetApplicationproblemJSON400() *oapi.Error
	GetApplicationproblemJSON401() *oapi.Error
	GetApplicationproblemJSONDefault() *oapi.Error
}

func decodeOAPI(response oapiResponse, err error) usersResult {
	GinkgoHelper()
	Expect(err).ToNot(HaveOccurred())
	result := usersResult{status: response.StatusCode()}
	if result.status >= http.StatusBadRequest {
		var problem *oapi.Error
		switch result.status {
		case http.StatusBadRequest:
			problem = response.GetApplicationproblemJSON400()
		case http.StatusUnauthorized:
			problem = response.GetApplicationproblemJSON401()
		case http.StatusNotFound:
			notFound, ok := response.(interface{ GetApplicationproblemJSON404() *oapi.Error })
			Expect(ok).To(BeTrue(), "unexpected 404: %s", response.GetBody())
			problem = notFound.GetApplicationproblemJSON404()
		default:
			problem = response.GetApplicationproblemJSONDefault()
		}
		Expect(problem).ToNot(BeNil(), "undecoded error response: %s", response.GetBody())
		Expect(problem.Status).To(Equal(int32(result.status)))
		return result
	}

	switch response := response.(type) {
	case *oapi.UsersListResponse:
		Expect(response.JSON200).ToNot(BeNil(), "undecoded list response: %s", response.Body)
		for _, user := range *response.JSON200 {
			result.users = append(result.users, userFromOAPI(&user))
		}
	case *oapi.UsersCreateResponse:
		result.user = userFromOAPI(response.JSON201)
	case *oapi.UsersGetResponse:
		result.user = userFromOAPI(response.JSON200)
	case *oapi.UsersUpdateResponse, *oapi.UsersDeleteResponse:
		Expect(response.GetBody()).To(BeEmpty())
	default:
		Fail(fmt.Sprintf("unexpected oapi-codegen response: %T", response))
	}
	return result
}

func userFromOAPI(user *oapi.User) User {
	GinkgoHelper()
	Expect(user).ToNot(BeNil(), "expected a decoded User response")
	Expect(user.Uuid).ToNot(BeNil())
	Expect(user.Email).ToNot(BeNil())
	return User{UUID: *user.Uuid, Email: *user.Email}
}

type ogenUsers struct{ client *ogen.Client }

func newOgenUsers(auth credentials) usersClient {
	GinkgoHelper()
	client, err := ogen.NewClient(
		integration.HTTPServerAddress()+"/api/v1", auth,
		ogen.WithClient(&http.Client{Timeout: 5 * time.Second}),
	)
	Expect(err).ToNot(HaveOccurred())
	return ogenUsers{client: client}
}

func (c ogenUsers) list(ctx context.Context, limit, offset int) usersResult {
	GinkgoHelper()
	response, err := c.client.UsersList(ctx, ogen.UsersListParams{
		Limit: ogen.NewOptInt(limit), Offset: ogen.NewOptInt(offset),
	})
	return decodeOgen(response, err, http.StatusOK)
}

func (c ogenUsers) create(ctx context.Context, email string) usersResult {
	GinkgoHelper()
	response, err := c.client.UsersCreate(ctx, &ogen.CreateUserRequest{Email: email, Password: fixturePassword})
	return decodeOgen(response, err, http.StatusCreated)
}

func (c ogenUsers) get(ctx context.Context, id uuid.UUID) usersResult {
	GinkgoHelper()
	response, err := c.client.UsersGet(ctx, ogen.UsersGetParams{UUID: id})
	return decodeOgen(response, err, http.StatusOK)
}

func (c ogenUsers) update(ctx context.Context, id uuid.UUID, email string) usersResult {
	GinkgoHelper()
	response, err := c.client.UsersUpdate(ctx, &ogen.UpdateUserRequest{Email: ogen.NewOptString(email)},
		ogen.UsersUpdateParams{UUID: id})
	return decodeOgen(response, err, http.StatusOK)
}

func (c ogenUsers) delete(ctx context.Context, id uuid.UUID) usersResult {
	GinkgoHelper()
	response, err := c.client.UsersDelete(ctx, ogen.UsersDeleteParams{UUID: id})
	return decodeOgen(response, err, http.StatusOK)
}

func decodeOgen(response any, err error, successStatus int) usersResult {
	GinkgoHelper()
	var unexpected *ogen.UnexpectedResponseStatusCode
	if errors.As(err, &unexpected) {
		Expect(unexpected.Response.Status).To(Equal(int32(unexpected.StatusCode)))
		return usersResult{status: unexpected.StatusCode}
	}
	Expect(err).ToNot(HaveOccurred())
	result := usersResult{status: successStatus}
	var problem *ogen.Error
	switch response := response.(type) {
	case *ogen.User:
		if response == nil {
			Fail("ogen returned a nil User response")
			return usersResult{}
		}
		result.user = userFromOgen(*response)
	case *ogen.UsersListOKApplicationJSON:
		if response == nil {
			Fail("ogen returned a nil users list response")
			return usersResult{}
		}
		for _, user := range *response {
			result.users = append(result.users, userFromOgen(user))
		}
	case *ogen.UsersUpdateOK, *ogen.UsersDeleteOK:
	case *ogen.UsersGetNotFound:
		result.status, problem = http.StatusNotFound, (*ogen.Error)(response)
	case *ogen.UsersDeleteNotFound:
		result.status, problem = http.StatusNotFound, (*ogen.Error)(response)
	case *ogen.UsersListUnauthorized:
		result.status, problem = http.StatusUnauthorized, (*ogen.Error)(response)
	case *ogen.UsersCreateUnauthorized:
		result.status, problem = http.StatusUnauthorized, (*ogen.Error)(response)
	case *ogen.UsersGetUnauthorized:
		result.status, problem = http.StatusUnauthorized, (*ogen.Error)(response)
	case *ogen.UsersUpdateUnauthorized:
		result.status, problem = http.StatusUnauthorized, (*ogen.Error)(response)
	case *ogen.UsersDeleteUnauthorized:
		result.status, problem = http.StatusUnauthorized, (*ogen.Error)(response)
	default:
		Fail(fmt.Sprintf("unexpected ogen response: %T (%+v)", response, response))
	}
	if problem != nil {
		Expect(problem.Status).To(Equal(int32(result.status)))
	}
	return result
}

func userFromOgen(user ogen.User) User {
	GinkgoHelper()
	Expect(user.UUID.Set).To(BeTrue())
	Expect(user.Email.Set).To(BeTrue())
	return User{UUID: user.UUID.Value, Email: user.Email.Value}
}
