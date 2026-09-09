package kafka

import (
	"errors"
	"fmt"

	"github.com/IBM/sarama"
	"github.com/xdg-go/scram"
)

type scramClient struct {
	hash         scram.HashGeneratorFcn
	conversation *scram.ClientConversation
}

func (s *scramClient) Begin(user, password, authzID string) error {
	client, err := s.hash.NewClient(user, password, authzID)
	if err != nil {
		return err
	}
	s.conversation = client.NewConversation()
	return nil
}

func (s *scramClient) Step(challenge string) (string, error) {
	return s.conversation.Step(challenge)
}

func (s *scramClient) Done() bool {
	return s.conversation.Done()
}

func WithSASL(mechanism, user, password string) Option {
	return func(engine *Kafka, publisher, subscriber *sarama.Config) {
		if len(mechanism) == 0 {
			if len(user) > 0 || len(password) > 0 {
				engine.configErr = errors.New("kafka SASL credentials require a mechanism")
			}
			return
		}
		if len(user) == 0 || len(password) == 0 {
			engine.configErr = errors.New("kafka SASL requires both user and password")
			return
		}
		var generator func() sarama.SCRAMClient
		switch sarama.SASLMechanism(mechanism) {
		case sarama.SASLTypePlaintext:
		case sarama.SASLTypeSCRAMSHA256:
			generator = func() sarama.SCRAMClient { return &scramClient{hash: scram.SHA256} }
		case sarama.SASLTypeSCRAMSHA512:
			generator = func() sarama.SCRAMClient { return &scramClient{hash: scram.SHA512} }
		default:
			engine.configErr = fmt.Errorf("unsupported Kafka SASL mechanism %q", mechanism)
			return
		}
		for _, config := range []*sarama.Config{publisher, subscriber} {
			config.Net.SASL.Enable = true
			config.Net.SASL.Handshake = true
			config.Net.SASL.Mechanism = sarama.SASLMechanism(mechanism)
			config.Net.SASL.User, config.Net.SASL.Password = user, password
			config.Net.SASL.SCRAMClientGeneratorFunc = generator
		}
	}
}
