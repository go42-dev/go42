package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go42-dev/go42/tests/integration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type chatChannel struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

type chatChannelMessage struct {
	ID         int    `json:"id"`
	Content    string `json:"content"`
	SenderUUID string `json:"sender_uuid"`
}

type chatPrivateMessage struct {
	ID            int    `json:"id"`
	Content       string `json:"content"`
	SenderUUID    string `json:"sender_uuid"`
	RecipientUUID string `json:"recipient_uuid"`
}

type chatLiveUpdate struct {
	ID            int     `json:"id"`
	Type          string  `json:"type"`
	ChannelUUID   *string `json:"channel_uuid"`
	SenderUUID    string  `json:"sender_uuid"`
	RecipientUUID *string `json:"recipient_uuid"`
	Content       string  `json:"content"`
}

type chatLiveUpdatesResponse struct {
	Updates     []chatLiveUpdate `json:"updates"`
	NextAfterID int              `json:"next_after_id"`
}

var _ = Describe("Chat API Integration Tests", func() {
	It("requires authentication", func(ctx SpecContext) {
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			integration.HTTPServerAddress()+"/api/v1/chat/channels",
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		Expect(err).ToNot(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
	})

	It("supports channels, private messages, and live updates", func(ctx SpecContext) {
		admin := newOAPIUsers(credentials{apiKey: integration.HTTPAPIKey()})
		user1, token1 := signUpUser(ctx, admin)
		user2, token2 := signUpUser(ctx, admin)

		var channel chatChannel
		doChatJSON(ctx, http.MethodPost, "/chat/channels", token1, map[string]string{
			"name": "general",
		}, http.StatusCreated, &channel)
		Expect(channel.UUID).ToNot(BeEmpty())

		doChatJSON(ctx, http.MethodPost, "/chat/channels/"+channel.UUID+"/messages", token1, map[string]string{
			"content": "hello channel",
		}, http.StatusCreated, nil)

		var channelMessages []chatChannelMessage
		doChatJSON(ctx, http.MethodGet, "/chat/channels/"+channel.UUID+"/messages", token2, nil, http.StatusOK, &channelMessages)
		Expect(channelMessages).ToNot(BeEmpty())
		Expect(channelMessages[0].Content).To(Equal("hello channel"))
		Expect(channelMessages[0].SenderUUID).To(Equal(user1.UUID))

		doChatJSON(ctx, http.MethodPost, "/chat/private/"+user2.UUID+"/messages", token1, map[string]string{
			"content": "hello private",
		}, http.StatusCreated, nil)

		var privateMessages []chatPrivateMessage
		doChatJSON(ctx, http.MethodGet, "/chat/private/"+user1.UUID+"/messages", token2, nil, http.StatusOK, &privateMessages)
		Expect(privateMessages).ToNot(BeEmpty())
		Expect(privateMessages[0].Content).To(Equal("hello private"))
		Expect(privateMessages[0].SenderUUID).To(Equal(user1.UUID))
		Expect(privateMessages[0].RecipientUUID).To(Equal(user2.UUID))

		var updates chatLiveUpdatesResponse
		doChatJSON(ctx, http.MethodGet, "/chat/updates", token2, nil, http.StatusOK, &updates)
		Expect(updates.Updates).ToNot(BeEmpty())
		Expect(updates.NextAfterID).To(BeNumerically(">=", updates.Updates[0].ID))
	})
})

func doChatJSON(
	ctx context.Context,
	method string,
	path string,
	token string,
	body any,
	expectedStatus int,
	target any,
) {
	GinkgoHelper()
	var payload []byte
	if body != nil {
		data, err := json.Marshal(body)
		Expect(err).ToNot(HaveOccurred())
		payload = data
	}

	request, err := http.NewRequestWithContext(
		ctx,
		method,
		integration.HTTPServerAddress()+"/api/v1"+path,
		bytes.NewReader(payload),
	)
	Expect(err).ToNot(HaveOccurred())
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	Expect(err).ToNot(HaveOccurred())
	defer response.Body.Close()
	Expect(response.StatusCode).To(Equal(expectedStatus))
	if target != nil {
		Expect(json.NewDecoder(response.Body).Decode(target)).To(Succeed())
	}
}
