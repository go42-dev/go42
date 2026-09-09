package gochan

import (
	"context"
	"log/slog"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"
)

const defaultOutputChannelBuffer = 999

type GoChan struct {
	logger  *slog.Logger
	channel *gochannel.GoChannel
}

func New(opts ...Option) *GoChan {
	engine := new(GoChan)

	for _, opt := range opts {
		opt(engine)
	}

	if engine.logger == nil {
		engine.logger = slog.New(slog.DiscardHandler)
	}

	goch := gochannel.NewGoChannel(
		gochannel.Config{
			OutputChannelBuffer: defaultOutputChannelBuffer,
			Persistent:          true,
			PreserveContext:     false,
		},
		watermill.NewSlogLogger(engine.logger),
	)

	engine.channel = goch
	return engine
}

func (g *GoChan) Publisher() message.Publisher {
	return g.channel
}

func (g *GoChan) Subscriber() message.Subscriber {
	return g.channel
}

func (g *GoChan) Shutdown(_ context.Context) error {
	return g.channel.Close()
}
