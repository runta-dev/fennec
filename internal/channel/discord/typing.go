package discord

import (
	"context"
	"log"
	"net/http"
	"time"
)

// Typing (FEN-242): once a message files a request or follows one up, the bot
// shows "Fennec is typing…" in the thread instead of posting a placeholder.
// Discord shows the indicator for about 10 seconds, or until the bot posts,
// so it is sent again every typingEvery while the request is still being
// handled. It stops when the bot posts in the thread (the answer from
// `channel reply`, seen over the Gateway), when the request is no longer
// Queued or Working, or after typingMax.
var (
	typingEvery = 8 * time.Second
	typingMax   = 2 * time.Hour
)

// typeOnce shows the indicator once. Best effort.
func (a *Adapter) typeOnce(ctx context.Context, channel string) {
	if err := a.call(ctx, http.MethodPost, "/channels/"+channel+"/typing", nil, nil); err != nil {
		log.Printf("discord: typing in %s: %v", channel, err)
	}
}

// typingLoop is one thread's running indicator loop.
type typingLoop struct{ cancel context.CancelFunc }

// startTyping keeps the indicator on in thread while request is active,
// replacing any loop already running there. It doesn't block.
func (a *Adapter) startTyping(ctx context.Context, sink Sink, thread string, request int) {
	tctx, cancel := context.WithTimeout(ctx, typingMax)
	loop := &typingLoop{cancel}
	a.mu.Lock()
	if old := a.typing[thread]; old != nil {
		old.cancel()
	}
	a.typing[thread] = loop
	a.mu.Unlock()
	go func() {
		defer a.endTyping(thread, loop)
		for sink.Active(tctx, request) {
			a.typeOnce(tctx, thread)
			if !sleep(tctx, typingEvery) {
				return
			}
		}
	}()
}

// stopTyping ends the indicator loop in thread, if one runs.
func (a *Adapter) stopTyping(thread string) {
	a.mu.Lock()
	loop := a.typing[thread]
	a.mu.Unlock()
	if loop != nil {
		a.endTyping(thread, loop)
	}
}

// endTyping stops loop and forgets it, unless a newer loop replaced it.
func (a *Adapter) endTyping(thread string, loop *typingLoop) {
	loop.cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.typing[thread] == loop {
		delete(a.typing, thread)
	}
}
