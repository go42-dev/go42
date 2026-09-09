package nats

import (
	"testing"

	wnats "github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestGobPayloadRemainsCompatibleWithStableDestinationScopedID(t *testing.T) {
	entry := message.NewMessage("event-id", []byte(`{"event":"created"}`))
	entry.Metadata.Set("request_id", "request-id")
	legacy, err := (wnats.GobMarshaler{}).Marshal("auth", entry)
	require.NoError(t, err)
	encoded, err := (gobMarshaler{}).Marshal("auth", entry)
	require.NoError(t, err)
	decoded, err := (wnats.GobMarshaler{}).Unmarshal(encoded)
	require.NoError(t, err)
	require.Equal(t, entry.UUID, decoded.UUID)
	require.Equal(t, entry.Payload, decoded.Payload)
	require.Equal(t, entry.Metadata, decoded.Metadata)
	oldDecoded, err := (gobMarshaler{}).Unmarshal(legacy)
	require.NoError(t, err)
	require.Equal(t, entry.UUID, oldDecoded.UUID)

	retry, err := (gobMarshaler{}).Marshal("auth", entry)
	require.NoError(t, err)
	deadLetter, err := (gobMarshaler{}).Marshal("auth_dlq", entry)
	require.NoError(t, err)
	require.NotEmpty(t, encoded.Header.Get(natsgo.MsgIdHdr))
	require.Equal(t, encoded.Header.Get(natsgo.MsgIdHdr), retry.Header.Get(natsgo.MsgIdHdr))
	require.NotEqual(t, encoded.Header.Get(natsgo.MsgIdHdr), deadLetter.Header.Get(natsgo.MsgIdHdr))
}
