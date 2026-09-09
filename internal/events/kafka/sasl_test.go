package kafka

import (
	"testing"

	"github.com/IBM/sarama"
	"github.com/stretchr/testify/require"
	"github.com/xdg-go/scram"
)

type scramTestCase struct {
	mechanism string
	hash      scram.HashGeneratorFcn
}

func TestSCRAMAuthenticatesAndRejectsWrongPasswords(t *testing.T) {
	for _, test := range []scramTestCase{{"SCRAM-SHA-256", scram.SHA256}, {"SCRAM-SHA-512", scram.SHA512}} {
		t.Run(test.mechanism, func(t *testing.T) {
			stored, err := test.hash.NewClient("user", "password", "")
			require.NoError(t, err)
			credentials, err := stored.GetStoredCredentialsWithError(scram.KeyFactors{Salt: "test-salt", Iters: 4096})
			require.NoError(t, err)
			server, err := test.hash.NewServer(
				func(string) (scram.StoredCredentials, error) { return credentials, nil },
			)
			require.NoError(t, err)
			for _, password := range []string{"password", "incorrect"} {
				publisher, subscriber := sarama.NewConfig(), sarama.NewConfig()
				WithSASL(test.mechanism, "user", password)(&Kafka{}, publisher, subscriber)
				client := publisher.Net.SASL.SCRAMClientGeneratorFunc()
				require.NotSame(t, client, subscriber.Net.SASL.SCRAMClientGeneratorFunc())
				require.NoError(t, client.Begin("user", password, ""))
				conversation := server.NewConversation()
				request, err := client.Step("")
				require.NoError(t, err)
				challenge, err := conversation.Step(request)
				require.NoError(t, err)
				request, err = client.Step(challenge)
				require.NoError(t, err)
				challenge, err = conversation.Step(request)
				if password == "incorrect" {
					require.Error(t, err)
					continue
				}
				require.NoError(t, err)
				_, err = client.Step(challenge)
				require.NoError(t, err)
				require.True(t, client.Done())
				require.True(t, conversation.Valid())
			}
		})
	}
}
