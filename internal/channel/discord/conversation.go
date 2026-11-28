package discord

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	messageTypeDefault = 0
	messageTypeReply   = 19

	// Thread channel types; a forum is type 15.
	channelAnnouncementThread = 10
	channelPublicThread       = 11
	channelPrivateThread      = 12

	submitTimeout   = 30 * time.Second
	threadArchive   = 10080 // minutes: a week without messages
	maxThreadName   = 80
	maxKnownChannel = 20000

	ackFailed = "Sorry, we couldn't record that just now. Please try again in a few minutes."
	ackEmpty  = "Hi! Mention me together with what's going on, and I'll start a thread for it."
)

// channelInfo is what a channel lookup or THREAD_CREATE tells us.
type channelInfo struct {
	ID       string `json:"id"`
	Type     int    `json:"type"`
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
}

func (c channelInfo) thread() bool {
	return c.Type == channelAnnouncementThread || c.Type == channelPublicThread || c.Type == channelPrivateThread
}

type discordUser struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Bot        bool   `json:"bot"`
}

func (u discordUser) author() Author {
	name := u.GlobalName
	if name == "" {
		name = u.Username
	}
	return Author{ID: u.ID, Name: name, Handle: u.Username}
}

// gatewayMessage is the subset of a MESSAGE_CREATE we read.
type gatewayMessage struct {
	ID        string      `json:"id"`
	ChannelID string      `json:"channel_id"`
	GuildID   string      `json:"guild_id"`
	Type      int         `json:"type"`
	Content   string      `json:"content"`
	WebhookID string      `json:"webhook_id"`
	Author    discordUser `json:"author"`
	Member    *struct {
		Nick string `json:"nick"`
	} `json:"member"`
	Attachments []struct {
		URL string `json:"url"`
	} `json:"attachments"`
}

// handle decides what a message is, in this order:
//
//  1. A message in a thread that already belongs to a request: a follow-up.
//  2. The first message of a new post in a watched forum: a new request,
//     answered in the post.
//  3. A message that @mentions the bot: a new request, in a thread started
//     on that message (or in the thread it was posted in).
//
// Everything else, and anything from bots, webhooks, DMs or other servers,
// is ignored.
func (a *Adapter) handle(ctx context.Context, sink Sink, m gatewayMessage) {
	a.mu.Lock()
	botID := a.botID
	a.mu.Unlock()
	if botID != "" && m.Author.ID == botID {
		// Our own post (usually the answer): stop "typing" there.
		a.stopTyping(m.ChannelID)
		return
	}
	if m.GuildID == "" || (len(a.guilds) > 0 && !a.guilds[m.GuildID]) ||
		m.Author.Bot || m.WebhookID != "" || m.Author.ID == botID ||
		(m.Type != messageTypeDefault && m.Type != messageTypeReply) ||
		!isSnowflake(m.ID) || !isSnowflake(m.ChannelID) || !isSnowflake(m.Author.ID) {
		return
	}
	mentioned := botID != "" && (strings.Contains(m.Content, "<@"+botID+">") || strings.Contains(m.Content, "<@!"+botID+">"))
	f := a.feedback(m, botID)
	if f.Text == "" && f.Context["attachments"] == "" && !mentioned {
		return
	}

	// 1. Follow-up in one of our threads.
	f.Conversation = m.ChannelID
	if n, ok, err := sink.FollowUp(ctx, f); err != nil {
		log.Printf("discord: follow-up %s in %s: %v", m.ID, m.ChannelID, err)
		return
	} else if ok {
		a.startTyping(ctx, sink, m.ChannelID, n)
		return
	}
	if !mentioned && len(a.forums) == 0 {
		return
	}
	info := a.lookup(ctx, m.ChannelID)

	// 2. A new forum post: its first message has the post's (thread's) ID.
	if info.thread() && a.forums[info.ParentID] {
		if m.ID != m.ChannelID {
			return // an old post from before the bot watched the forum
		}
		f.Title = info.Name
		f.Context["title"] = info.Name
		if f.Text == "" && f.Context["attachments"] == "" {
			f.Text = info.Name
		}
		a.file(ctx, sink, f, m.ChannelID, m.Author.ID)
		return
	}

	// 3. An @mention.
	if !mentioned {
		return
	}
	if len(a.channels) > 0 && !a.channels[m.ChannelID] && !(info.thread() && a.channels[info.ParentID]) {
		return
	}
	if f.Text == "" && f.Context["attachments"] == "" {
		a.post(ctx, m.ChannelID, m.Author.ID, m.ID, ackEmpty)
		return
	}
	thread := m.ChannelID
	if !info.thread() {
		var err error
		if thread, err = a.startThread(ctx, m, threadName(f)); err != nil {
			// No thread (missing permission?): answer under the message.
			log.Printf("discord: starting a thread on %s: %v", m.ID, err)
			f.Conversation = ""
			f.ReplyRef = replyRef{Channel: m.ChannelID, User: m.Author.ID, Message: m.ID}.String()
			a.submit(ctx, sink, f, m.ChannelID, m.Author.ID, m.ID)
			return
		}
	}
	a.file(ctx, sink, f, thread, m.Author.ID)
}

// file opens a request for a conversation in thread and says so there.
func (a *Adapter) file(ctx context.Context, sink Sink, f Feedback, thread, user string) {
	f.Conversation = thread
	f.ReplyRef = replyRef{Channel: thread, User: user}.String()
	a.submit(ctx, sink, f, thread, user, "")
}

// submit files f. Instead of a placeholder message, the bot shows that it is
// typing in where until the request is answered (FEN-242); only a failure is
// said in words.
func (a *Adapter) submit(ctx context.Context, sink Sink, f Feedback, where, user, replyTo string) {
	a.typeOnce(ctx, where)
	sctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	n, err := sink.Submit(sctx, f)
	if err != nil {
		log.Printf("discord: filing feedback from %s failed: %v", user, err)
		a.post(ctx, where, user, replyTo, ackFailed)
		return
	}
	a.startTyping(ctx, sink, where, n)
}

// feedback normalizes m; the bot's own mention is dropped from the text.
func (a *Adapter) feedback(m gatewayMessage, botID string) Feedback {
	text := m.Content
	if botID != "" {
		text = strings.NewReplacer("<@"+botID+">", "", "<@!"+botID+">", "").Replace(text)
	}
	au := m.Author.author()
	if m.Member != nil && m.Member.Nick != "" {
		au.Name = m.Member.Nick
	}
	ctx := map[string]string{
		"guild_id":   m.GuildID,
		"channel_id": m.ChannelID,
		"message_id": m.ID,
		"link":       "https://discord.com/channels/" + m.GuildID + "/" + m.ChannelID + "/" + m.ID,
	}
	var files []string
	for _, at := range m.Attachments {
		files = append(files, at.URL)
	}
	if len(files) > 0 {
		ctx["attachments"] = strings.Join(files, "\n")
	}
	return Feedback{Author: au, Text: strings.TrimSpace(text), Context: ctx}
}

func threadName(f Feedback) string {
	name := ""
	for _, line := range strings.Split(f.Text, "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			name = line
			break
		}
	}
	if name == "" {
		name = "Feedback from " + f.Author.Name
	}
	if utf8.RuneCountInString(name) > maxThreadName {
		name = string([]rune(name)[:maxThreadName-1]) + "…"
	}
	return name
}

// startThread starts a public thread on m and returns its ID (Discord gives
// it the message's ID).
func (a *Adapter) startThread(ctx context.Context, m gatewayMessage, name string) (string, error) {
	var out channelInfo
	err := a.call(ctx, http.MethodPost, "/channels/"+m.ChannelID+"/messages/"+m.ID+"/threads",
		map[string]any{"name": name, "auto_archive_duration": threadArchive}, &out)
	if err != nil {
		return "", err
	}
	if out.ID == "" {
		out.ID = m.ID
	}
	out.Type, out.ParentID, out.Name = channelPublicThread, m.ChannelID, name
	a.remember(out)
	return out.ID, nil
}

// lookup says what a channel is, from THREAD_CREATE events or the API. A
// failed lookup reads as a plain channel.
func (a *Adapter) lookup(ctx context.Context, id string) channelInfo {
	a.mu.Lock()
	c, ok := a.known[id]
	a.mu.Unlock()
	if ok {
		return c
	}
	if err := a.call(ctx, http.MethodGet, "/channels/"+id, nil, &c); err != nil {
		log.Printf("discord: looking up channel %s: %v", id, err)
		return channelInfo{ID: id}
	}
	a.remember(c)
	return c
}

func (a *Adapter) remember(c channelInfo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.known) >= maxKnownChannel {
		a.known = map[string]channelInfo{}
	}
	a.known[c.ID] = c
}

// post says text in channel, mentioning user, optionally as a reply to a
// message. Best effort: failures are logged.
func (a *Adapter) post(ctx context.Context, channel, user, replyTo, text string) {
	msg := map[string]any{
		"content":          "<@" + user + "> " + text,
		"allowed_mentions": map[string]any{"parse": []string{}, "users": []string{user}},
	}
	if replyTo != "" {
		msg["message_reference"] = map[string]any{"message_id": replyTo, "channel_id": channel, "fail_if_not_exists": false}
	}
	if err := a.call(ctx, http.MethodPost, "/channels/"+channel+"/messages", msg, nil); err != nil {
		log.Printf("discord: posting in %s: %v", channel, err)
	}
}
