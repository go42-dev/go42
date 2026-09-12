package nats

import (
	"fmt"

	wnats "github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	natsgo "github.com/nats-io/nats.go"
)

// marshaler uses native NATS payloads and headers and scopes deduplication to each destination.
type marshaler struct {
	wnats.NATSMarshaler
}

func (m marshaler) Marshal(topic string, msg *message.Message) (*natsgo.Msg, error) {
	encoded, err := m.NATSMarshaler.Marshal(topic, msg)
	if err != nil {
		return nil, err
	}
	encoded.Header.Set(wnats.WatermillUUIDHdr, msg.UUID)
	// Deduplication belongs to a destination. A DLQ may share its source's stream.
	encoded.Header.Set(natsgo.MsgIdHdr, deduplicationID(topic, msg.UUID))
	return encoded, nil
}

func deduplicationID(topic, id string) string {
	return fmt.Sprintf("%d:%s:%s", len(topic), topic, id)
}
