package nats

import (
	"testing"

	wnats "github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestMarshalerPreservesPayloadAndNativeHeaders(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{name: "JSON", payload: []byte(`{"event":"created"}`)},
		{name: "invalid JSON", payload: []byte(`{"event":`)},
		{name: "binary", payload: []byte{0, 0xff, 0x80, '\n'}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := message.NewMessage("event-id", test.payload)
			entry.Metadata.Set("request_id", "request-id")
			entry.Metadata.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
			entry.Metadata.Set("tracestate", "vendor=value")
			encoded, err := (marshaler{}).Marshal("auth", entry)
			require.NoError(t, err)
			require.Equal(t, "auth", encoded.Subject)
			require.Equal(t, test.payload, encoded.Data)
			require.Equal(t, entry.UUID, encoded.Header.Get(wnats.WatermillUUIDHdr))
			for key, value := range entry.Metadata {
				require.Equal(t, value, encoded.Header.Get(key))
			}
			decoded, err := new(wnats.NATSMarshaler).Unmarshal(encoded)
			require.NoError(t, err)
			require.Equal(t, entry.UUID, decoded.UUID)
			require.Equal(t, entry.Payload, decoded.Payload)
			require.Equal(t, entry.Metadata, decoded.Metadata)
		})
	}
}

func TestMarshalerKeepsStableMessageIDsPerDestination(t *testing.T) {
	entry := message.NewMessage("event-id", []byte(`{"event":"created"}`))
	entry.Metadata.Set(wnats.WatermillUUIDHdr, "stale-uuid")
	entry.Metadata.Set(natsgo.MsgIdHdr, "stale-deduplication-id")
	encoded, err := (marshaler{}).Marshal("auth", entry)
	require.NoError(t, err)
	require.Equal(t, entry.UUID, encoded.Header.Get(wnats.WatermillUUIDHdr))
	require.NotEqual(t, "stale-deduplication-id", encoded.Header.Get(natsgo.MsgIdHdr))

	retry, err := (marshaler{}).Marshal("auth", entry)
	require.NoError(t, err)
	deadLetter, err := (marshaler{}).Marshal("auth_dlq", entry)
	require.NoError(t, err)
	other, err := (marshaler{}).Marshal("auth", message.NewMessage("other-event-id", entry.Payload))
	require.NoError(t, err)
	require.NotEmpty(t, encoded.Header.Get(natsgo.MsgIdHdr))
	require.Equal(t, encoded.Header.Get(natsgo.MsgIdHdr), retry.Header.Get(natsgo.MsgIdHdr))
	require.NotEqual(t, encoded.Header.Get(natsgo.MsgIdHdr), deadLetter.Header.Get(natsgo.MsgIdHdr))
	require.NotEqual(t, encoded.Header.Get(natsgo.MsgIdHdr), other.Header.Get(natsgo.MsgIdHdr))
}
