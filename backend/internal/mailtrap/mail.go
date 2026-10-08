package mailtrap

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	mailtrapsdk "github.com/mailtrap/mailtrap-go"
)

// Address is one email endpoint.
type Address struct {
	Email string
	Name  string
}

func (a Address) String() string {
	if a.Name == "" {
		return a.Email
	}
	return fmt.Sprintf("%s <%s>", a.Name, a.Email)
}

// Message is one email to send. Text, HTML, or both — a gift note should always
// carry a text part, because a plain-text fallback is what a recipient with
// images/HTML blocked still reads.
type Message struct {
	To      []Address
	Cc      []Address
	Bcc     []Address
	Subject string
	Text    string
	HTML    string
	// Category overrides Config.Category for this send. Empty = the default.
	Category string
	// ReplyTo overrides Config.ReplyTo. Empty = the configured value.
	ReplyTo Address
	// OrderID is stored as a Mailtrap custom variable and as the X-Order-Id
	// header, so support can find the exact email behind a complaint in
	// https://mailtrap.io/sending/email_logs without guessing from the subject.
	OrderID string
	// CustomVariables are merged on top of OrderID.
	CustomVariables map[string]any
	// MessageID, when set, is sent as the Message-ID header. That dedupes the
	// message at the RECEIVING mail host; it is not a Mailtrap idempotency key
	// (the send API has none), so it never replaces a send-once marker such as
	// db.Quote.GiftNotifiedAt.
	MessageID string
	// Attachments are sent with the message. An attachment with a ContentID is
	// inline and is referenced from the HTML body as cid:<ContentID>.
	Attachments []Attachment
}

// Attachment is a file sent with a Message.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
	// ContentID, when set, makes the attachment inline (cid:<ContentID>).
	ContentID string
}

// Kind classifies a transport failure so the caller can decide between retrying
// and giving up. Getting this wrong is how a shop either spams someone twice or
// silently never tells them about their gift.
type Kind int

const (
	// KindUnknown is anything unclassified.
	KindUnknown Kind = iota
	// KindTemporary may succeed later: rate limit, 5xx, timeout.
	KindTemporary
	// KindPermanent will fail the same way forever: bad token, unverified
	// sender, malformed recipient, empty body.
	KindPermanent
	// KindDisabled means the transport is off; nothing was attempted.
	KindDisabled
)

func (k Kind) String() string {
	switch k {
	case KindTemporary:
		return "temporary"
	case KindPermanent:
		return "permanent"
	case KindDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// Retryable reports whether another attempt makes sense.
func (k Kind) Retryable() bool { return k == KindTemporary }

// Client sends email through the Mailtrap Email API.
type Client struct {
	cfg Config
	api *mailtrapsdk.Client
}

// New builds the transport from cfg. A config without a token or a from-address
// is NOT an error: it produces a client that reports Enabled() == false and whose
// Send returns ErrDisabled, so a development database with no credentials simply
// does not send. A config that asks for the sandbox without a sandbox id, or that
// carries a malformed from-address, IS an error — those are typos, and a typo in
// a mail config is discovered far too late when it surfaces as "nobody got email".
func New(cfg Config) (*Client, error) {
	if cfg.UseSandbox && cfg.SandboxID == 0 {
		return nil, ErrSandboxID
	}
	if !cfg.Enabled() {
		return &Client{cfg: cfg}, nil
	}
	from := strings.TrimSpace(cfg.FromEmail)
	if !looksLikeAddress(from) {
		return nil, fmt.Errorf("mailtrap: %s=%q is not an email address", EnvFromEmail, cfg.FromEmail)
	}
	cfg.FromEmail = from

	opts := []mailtrapsdk.Option{
		// A bounded client: a hung provider must not stall the fulfillment
		// tracker that calls us.
		mailtrapsdk.WithHTTPClient(&http.Client{Timeout: cfg.timeout()}),
	}
	if ep := strings.TrimSpace(cfg.APIEndpoint); ep != "" {
		u, uerr := url.Parse(ep)
		if uerr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%w (got %q)", ErrEndpoint, ep)
		}
		host := mailtrapsdk.HostSend
		if cfg.UseSandbox {
			host = mailtrapsdk.HostSandbox
		}
		opts = append(opts, mailtrapsdk.WithBaseURL(host, ep))
	}
	if cfg.UseSandbox {
		opts = append(opts, mailtrapsdk.WithSandbox(true), mailtrapsdk.WithSandboxID(cfg.SandboxID))
	}
	api, err := mailtrapsdk.NewClient(cfg.APIToken, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, api: api}, nil
}

// Enabled reports whether Send would attempt anything.
func (c *Client) Enabled() bool { return c != nil && c.api != nil && c.cfg.Enabled() }

// Sandbox reports whether sends are captured instead of delivered — worth
// showing in an admin banner, since it explains "the logs say sent, the user
// says nothing arrived".
func (c *Client) Sandbox() bool { return c != nil && c.cfg.UseSandbox }

// Config returns a copy of the setup with the token masked. Safe to log.
func (c *Client) Config() Config {
	out := c.cfg
	if out.APIToken != "" {
		out.APIToken = "***"
	}
	return out
}

// Send delivers one message and returns Mailtrap's message IDs (one per
// recipient group). Every failure is wrapped by Classify, so the caller can
// branch on Kind without knowing the SDK's error types.
func (c *Client) Send(ctx context.Context, m Message) ([]string, error) {
	if c == nil || !c.Enabled() {
		return nil, ErrDisabled
	}
	if err := m.validate(); err != nil {
		return nil, err
	}

	req := &mailtrapsdk.SendRequest{
		From:     mailtrapsdk.Address{Email: c.cfg.FromEmail, Name: c.cfg.FromName},
		To:       toSDK(m.To),
		Cc:       toSDK(m.Cc),
		Bcc:      toSDK(m.Bcc),
		Subject:  m.Subject,
		Text:     m.Text,
		HTML:     m.HTML,
		Category: m.category(c.cfg.Category),
		Headers:  m.headers(),
	}
	if reply := m.replyTo(c.cfg.ReplyTo); reply != "" {
		req.ReplyTo = &mailtrapsdk.Address{Email: reply}
	}
	if vars := m.variables(); len(vars) > 0 {
		req.CustomVariables = vars
	}
	for _, a := range m.Attachments {
		sa := mailtrapsdk.Attachment{
			Content:  base64.StdEncoding.EncodeToString(a.Data),
			Type:     a.ContentType,
			Filename: a.Filename,
		}
		if a.ContentID != "" {
			sa.Disposition = mailtrapsdk.DispositionInline
			sa.ContentID = a.ContentID
		} else {
			sa.Disposition = mailtrapsdk.DispositionAttachment
		}
		req.Attachments = append(req.Attachments, sa)
	}

	resp, _, err := c.api.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, Classify(err))
	}
	if resp == nil {
		return nil, errors.New("mailtrap: empty response from the send API")
	}
	ids := append([]string(nil), resp.MessageIDs...)
	if !resp.Success && len(ids) == 0 {
		// 200 with success:false is Mailtrap's "accepted nothing" shape on some
		// streams; surfacing it beats letting a silent no-send look like a
		// delivered note.
		return ids, errors.New("mailtrap: the send API accepted the request but delivered nothing")
	}
	return ids, nil
}

// SendRetried calls Send, retrying ONLY on a rate limit that asks for a short
// wait. Timeouts and 5xx are deliberately not retried here: a request whose
// response was lost may already have been accepted, and the only safe dedupe is
// the caller's own send-once marker (db.Quote.GiftNotifiedAt). A worker that
// re-runs the fulfillment step later gets its retry for free.
func (c *Client) SendRetried(ctx context.Context, m Message, maxAttempts int) ([]string, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var last error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ids, err := c.Send(ctx, m)
		if err == nil {
			return ids, nil
		}
		last = err
		if Classify(err) != KindTemporary || attempt == maxAttempts {
			return nil, last
		}
		wait := RateLimitWait(err)
		if wait <= 0 {
			return nil, last
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, last
}

// RateLimitWait is the delay the provider asked for, or 0 when it did not say.
func RateLimitWait(err error) time.Duration {
	var rle *mailtrapsdk.RateLimitError
	if errors.As(err, &rle) {
		return rle.RetryAfter
	}
	return 0
}

// Classify maps any error (including wrapped ones) to a Kind.
func Classify(err error) Kind {
	switch {
	case err == nil:
		return KindUnknown
	case errors.Is(err, ErrDisabled), errors.Is(err, ErrNoRecipient), errors.Is(err, ErrSandboxID):
		return KindPermanent
	}
	var rle *mailtrapsdk.RateLimitError
	if errors.As(err, &rle) {
		return KindTemporary
	}
	var ve *mailtrapsdk.ValidationError
	if errors.As(err, &ve) {
		return KindPermanent
	}
	var ue *mailtrapsdk.UnauthorizedError
	if errors.As(err, &ue) {
		return KindPermanent
	}
	var fe *mailtrapsdk.ForbiddenError
	if errors.As(err, &fe) {
		return KindPermanent
	}
	var apiErr *mailtrapsdk.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode >= 500 {
			return KindTemporary
		}
		return KindPermanent
	}
	// A dial error, a TLS failure or a timeout is not a decoded API error: leave
	// it KindUnknown (not retryable by SendRetried) because the request may or
	// may not have been accepted — see the note on SendRetried.
	return KindUnknown
}

func (m Message) validate() error {
	if len(m.To) == 0 {
		return ErrNoRecipient
	}
	for _, a := range m.To {
		if !looksLikeAddress(a.Email) {
			return fmt.Errorf("mailtrap: recipient %q is not an email address", a.Email)
		}
	}
	if strings.TrimSpace(m.Subject) == "" {
		return errors.New("mailtrap: subject is required")
	}
	if strings.TrimSpace(m.Text) == "" && strings.TrimSpace(m.HTML) == "" {
		return errors.New("mailtrap: a message needs a text or an html body")
	}
	return nil
}

func (m Message) category(cfg string) string {
	if c := strings.TrimSpace(m.Category); c != "" {
		return c
	}
	if cfg == "" {
		return DefaultCategory
	}
	return cfg
}

func (m Message) replyTo(cfg string) string {
	if e := strings.TrimSpace(m.ReplyTo.Email); e != "" {
		return e
	}
	return strings.TrimSpace(cfg)
}

func (m Message) headers() map[string]string {
	h := map[string]string{}
	if m.OrderID != "" {
		h["X-Order-Id"] = m.OrderID
	}
	if m.MessageID != "" {
		id := m.MessageID
		if !strings.HasPrefix(id, "<") {
			id = "<" + id + ">"
		}
		h["Message-ID"] = id
	}
	return h
}

func (m Message) variables() map[string]any {
	out := make(map[string]any, len(m.CustomVariables)+1)
	for k, v := range m.CustomVariables {
		out[k] = v
	}
	if m.OrderID != "" {
		out["order_id"] = m.OrderID
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toSDK(list []Address) []mailtrapsdk.Address {
	if len(list) == 0 {
		return nil
	}
	out := make([]mailtrapsdk.Address, 0, len(list))
	for _, a := range list {
		out = append(out, mailtrapsdk.Address{Email: strings.TrimSpace(a.Email), Name: a.Name})
	}
	return out
}

// looksLikeAddress is a shape check, not an RFC 5322 parser: Mailtrap rejects
// the rest, and the shop's own validEmail stays the authority for delivery
// addresses. This exists so a typo in MAILTRAP_FROM_EMAIL fails at startup.
func looksLikeAddress(v string) bool {
	v = strings.TrimSpace(v)
	at := strings.Index(v, "@")
	if at <= 0 || at != strings.LastIndex(v, "@") || at == len(v)-1 {
		return false
	}
	if strings.ContainsAny(v, " \t\n<>\"'") {
		return false
	}
	dot := strings.LastIndex(v[at+1:], ".")
	return dot > 0 && dot < len(v)-at-2
}
