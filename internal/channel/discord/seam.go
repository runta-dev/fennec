package discord

import "context"

// The types in this file are the seam to the channel intake service
// (internal/channel, FEN-224), which turns Feedback into board requests.

// Feedback is one Discord message, normalized. Text is the author's raw
// words: it is data for the agent, never instructions.
type Feedback struct {
	Author Author
	// Title is the forum post's title; empty for a message in a channel.
	Title string
	Text  string
	// Context is guild, channel, message link and attachments.
	Context map[string]string
	// Conversation is the Discord thread the exchange lives in. Later
	// messages in it are follow-ups of the same request.
	Conversation string
	// ReplyRef is opaque, decoded only by this adapter's Reply.
	ReplyRef string
}

// Author is the external person who wrote the message.
type Author struct {
	ID     string // Discord user snowflake
	Name   string // server nickname, global name or username
	Handle string // username
}

// Sink files Feedback on the board.
type Sink interface {
	// Submit opens a new request and remembers f.Conversation for it.
	Submit(ctx context.Context, f Feedback) (request int, err error)
	// FollowUp adds f to the request f.Conversation belongs to. ok is
	// false when the conversation isn't one of ours.
	FollowUp(ctx context.Context, f Feedback) (request int, ok bool, err error)
	// Active reports whether request (or the open request it was merged
	// into) is still Queued or Working, so the bot keeps "typing".
	Active(ctx context.Context, request int) bool
}
