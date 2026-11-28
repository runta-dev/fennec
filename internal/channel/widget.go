package channel

import (
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// KindWidget is the web widget: a script sites embed, which posts to the
// intake server.
const KindWidget = "widget"

//go:embed widget.js
var widgetJS []byte

// Widget limits.
const (
	maxSubmission = 32 << 10
	maxText       = 5000 // runes
	maxName       = 40   // runes
)

// TurnstileVerifyURL is Cloudflare's siteverify endpoint; tests replace it.
var TurnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// Cloudflare's test secret that always passes; its answers name a dummy
// hostname, so the hostname check is skipped for it.
const turnstileTestSecret = "1x0000000000000000000000000000000AA"

// Widget is a web widget channel.
type Widget struct {
	Spec ChannelSpec
	SMTP *SMTP
	// Client verifies Turnstile tokens; nil uses a 10 s default.
	Client *http.Client
}

// Submission is what widget.js posts.
type Submission struct {
	Text  string `json:"text"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Token string `json:"token"`
	Page  string `json:"page,omitempty"`
}

// AllowedOrigin reports whether a page at origin may use the widget.
func (w *Widget) AllowedOrigin(origin string) bool {
	got, ok := parseOrigin(origin, false)
	if !ok {
		return false
	}
	for _, pattern := range w.Spec.Origins {
		if want, ok := parseOrigin(pattern, true); ok && want.matches(got) {
			return true
		}
	}
	return false
}

// Ingest checks the page's origin, the Turnstile token and the fields, and
// normalizes the submission.
func (w *Widget) Ingest(r *http.Request) (Feedback, error) {
	origin := r.Header.Get("Origin")
	if !w.AllowedOrigin(origin) {
		return Feedback{}, rejected("origin %q is not allowed", origin)
	}
	var sub Submission
	body := http.MaxBytesReader(nil, r.Body, maxSubmission)
	if err := json.NewDecoder(body).Decode(&sub); err != nil {
		return Feedback{}, rejected("bad body")
	}
	text := strings.TrimSpace(strings.ReplaceAll(sub.Text, "\r\n", "\n"))
	if text == "" || !utf8.ValidString(text) || strings.ContainsRune(text, 0) || utf8.RuneCountInString(text) > maxText {
		return Feedback{}, rejected("text must be 1 to %d characters", maxText)
	}
	name := oneLine(sub.Name, maxName)
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		name = ""
	}
	email := strings.TrimSpace(sub.Email)
	if email != "" {
		addr, err := mail.ParseAddress(email)
		if err != nil || addr.Address != email || len(email) > 254 || strings.ContainsAny(email, "\r\n") {
			return Feedback{}, rejected("invalid email")
		}
	}
	if err := w.verifyTurnstile(r.Context(), sub.Token, origin); err != nil {
		return Feedback{}, err
	}
	if name == "" {
		name = "Widget visitor"
	}
	ctx := map[string]string{}
	if page, err := url.Parse(sub.Page); err == nil && (page.Scheme == "https" || page.Scheme == "http") && w.AllowedOrigin(page.Scheme+"://"+page.Host) {
		ctx["page"] = page.String()
	}
	if ua := r.UserAgent(); ua != "" {
		ctx["browser"] = ua
	}
	if lang, _, _ := strings.Cut(r.Header.Get("Accept-Language"), ","); lang != "" {
		ctx["language"] = lang
	}
	return Feedback{Channel: w.Spec.ID, Kind: KindWidget, Author: name, Text: text, Context: ctx, ReplyRef: email, Received: time.Now().UTC()}, nil
}

func (w *Widget) verifyTurnstile(ctx context.Context, token, origin string) error {
	if token == "" || len(token) > 2048 {
		return rejected("missing challenge token")
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	form := url.Values{"secret": {w.Spec.Turnstile.Secret}, "response": {token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TurnstileVerifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("turnstile: %w", err)
	}
	defer resp.Body.Close()
	var answer struct {
		Success  bool     `json:"success"`
		Hostname string   `json:"hostname"`
		Errors   []string `json:"error-codes"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&answer); err != nil {
		return fmt.Errorf("turnstile: %w", err)
	}
	if !answer.Success {
		return rejected("challenge failed %v", answer.Errors)
	}
	if w.Spec.Turnstile.Secret != turnstileTestSecret {
		got, _ := parseOrigin(origin, false)
		if !strings.EqualFold(answer.Hostname, got.hostname()) {
			return rejected("challenge solved on %q, not %q", answer.Hostname, got.hostname())
		}
	}
	return nil
}

// Reply mails text to the address the author left.
func (w *Widget) Reply(ctx context.Context, ref, text string) error {
	if w.SMTP == nil || w.SMTP.Host == "" || w.SMTP.From == "" {
		return errors.New("widget replies need smtp in the channel config")
	}
	to, err := mail.ParseAddress(ref)
	if err != nil || strings.ContainsAny(ref, "\r\n") {
		return fmt.Errorf("bad reply address")
	}
	from, err := mail.ParseAddress(w.SMTP.From)
	if err != nil {
		return fmt.Errorf("smtp.from: %w", err)
	}
	return sendMail(ctx, *w.SMTP, from, to, "Re: your feedback", text)
}

func mailMessage(from, to *mail.Address, subject, text string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n",
		from.String(), to.String(), mime.QEncoding.Encode("utf-8", subject), time.Now().Format(time.RFC1123Z))
	enc := base64.StdEncoding.EncodeToString([]byte(text))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return []byte(b.String())
}

// sendMail delivers over STARTTLS (587, 25) or implicit TLS (465).
func sendMail(ctx context.Context, cfg SMTP, from, to *mail.Address, subject, text string) error {
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if port == 465 {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: cfg.Host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	} else {
		conn.SetDeadline(time.Now().Add(time.Minute))
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if port != 465 {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp server offers no STARTTLS")
		}
		if err = c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			return err
		}
	}
	if cfg.Username != "" {
		if err = c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return err
		}
	}
	if err = c.Mail(from.Address); err != nil {
		return err
	}
	if err = c.Rcpt(to.Address); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err = wc.Write(mailMessage(from, to, subject, text)); err != nil {
		return err
	}
	if err = wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// origin is a scheme and host[:port]; host may start with "*." in patterns.
type origin struct{ scheme, host string }

func parseOrigin(s string, pattern bool) (origin, bool) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return origin{}, false
	}
	host := strings.ToLower(u.Host)
	if strings.Contains(host, "*") && (!pattern || !strings.HasPrefix(host, "*.") || strings.Count(host, "*") != 1 || strings.Count(host, ".") < 2) {
		return origin{}, false
	}
	return origin{scheme: u.Scheme, host: host}, true
}

func (o origin) hostname() string {
	if h, _, err := net.SplitHostPort(o.host); err == nil {
		return h
	}
	return o.host
}

func (o origin) matches(got origin) bool {
	if o.scheme != got.scheme {
		return false
	}
	if rest, ok := strings.CutPrefix(o.host, "*."); ok {
		return strings.HasSuffix(got.host, "."+rest)
	}
	return o.host == got.host
}
