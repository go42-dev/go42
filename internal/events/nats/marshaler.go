package nats

import (
	"fmt"

	wnats "github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	natsgo "github.com/nats-io/nats.go"
)

// gobMarshaler keeps the existing wire payload and adds broker deduplication headers.
type gobMarshaler struct {
	wnats.GobMarshaler
}

func (g gobMarshaler) Marshal(topic string, msg *message.Message) (*natsgo.Msg, error) {
	encoded, err := g.GobMarshaler.Marshal(topic, msg)
	if err != nil {
		return nil, err
	}
	encoded.Header = natsgo.Header{}
	encoded.Header.Set(wnats.WatermillUUIDHdr, msg.UUID)
	// Deduplication belongs to a destination. A DLQ may share its source's stream.
	encoded.Header.Set(natsgo.MsgIdHdr, deduplicationID(topic, msg.UUID))
	return encoded, nil
}

func deduplicationID(topic, id string) string {
	return fmt.Sprintf("%d:%s:%s", len(topic), topic, id)
}
