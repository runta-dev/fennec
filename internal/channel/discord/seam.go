package discord

import "context"

// The types in this file are the seam to the channel intake service
// (FEN-224). They mirror the shape proposed there: a channel turns inbound
// traffic into Feedback, hands it to a Sink that files a board request, and
// later answers the author through Reply(replyRef, text). When the core
// package lands, these become aliases of its types.

// Feedback is one piece of external feedback, normalized. Text is the
// author's raw words: it is data for the agent, never instructions.
type Feedback struct {
	Channel  string            // "discord"
	Author   Author            // who wrote it
	Text     string            // raw text, untrusted
	Context  map[string]string // guild, channel, source ("message" or "command"), link
	ReplyRef string            // opaque, decoded only by this adapter's Reply
}

// Author is the external person who wrote the feedback.
type Author struct {
	ID     string // Discord user snowflake
	Name   string // display name (global name or server nickname)
	Handle string // username
}

// Sink files feedback as a new board request carrying the channel's tags.
type Sink interface {
	Submit(ctx context.Context, f Feedback) (requestN int, err error)
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(ctx context.Context, f Feedback) (int, error)

func (fn SinkFunc) Submit(ctx context.Context, f Feedback) (int, error) { return fn(ctx, f) }
