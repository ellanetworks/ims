package n5

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	// TS 29.500 §5.2.2.2: the User-Agent starts with the NF type (TS 29.510 NFType).
	DefaultUserAgent = "PCSCF"

	// TS 29.500 §5.2.6: a PING at most every 60 s on each connection.
	MinPingInterval = 60 * time.Second

	dialTimeout  = 5 * time.Second
	maxBody      = 1 << 20
	maxRedirects = 3
)

type Config struct {
	// PCF is the API root of the PCF, http://host[:port][/prefix] (TS 29.501 §4.4.1).
	PCF string
	// UserAgent defaults to DefaultUserAgent.
	UserAgent string
	// PingInterval is how long a connection may stay silent before a PING checks it (TS 29.500 §5.2.6).
	// It defaults to, and is at least, MinPingInterval.
	PingInterval time.Duration
}

// Client invokes Npcf_PolicyAuthorization on one PCF, over HTTP/2 without TLS (prior knowledge, RFC 9113 §3.3).
// Requests share one connection, and more open when the PCF's stream limit is reached (TS 29.500 §5.2.6).
type Client struct {
	root      *url.URL
	userAgent string
	http      *http.Client
	transport *http.Transport
	now       func() time.Time
}

func New(cfg Config) (*Client, error) {
	root, err := url.Parse(cfg.PCF)

	switch {
	case err != nil:
		return nil, fmt.Errorf("PCF URI: %w", err)
	case root.Scheme != "http" || root.Host == "" || root.User != nil || root.RawQuery != "" || root.Fragment != "":
		return nil, fmt.Errorf("PCF URI %q: want http://host[:port][/prefix]", cfg.PCF)
	}

	root.Path = strings.TrimSuffix(root.Path, "/")
	root.RawPath = ""

	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}

	cfg.PingInterval = max(cfg.PingInterval, MinPingInterval)

	var protocols http.Protocols

	protocols.SetUnencryptedHTTP2(true)

	t := &http.Transport{
		Protocols:   &protocols,
		HTTP2:       &http.HTTP2Config{SendPingTimeout: cfg.PingInterval},
		DialContext: (&net.Dialer{Timeout: dialTimeout}).DialContext,
	}

	return &Client{
		root:      root,
		userAgent: cfg.UserAgent,
		transport: t,
		http:      &http.Client{Transport: t, CheckRedirect: checkRedirect},
		now:       time.Now,
	}, nil
}

// RFC 9110 §15.4: follow 307 and 308, which repeat the request, and nothing that turns it into a GET. A 303 to
// a create is an answer, not a redirection (TS 29.514 §5.3.2.3.1).
func checkRedirect(req *http.Request, via []*http.Request) error {
	if s := req.Response.StatusCode; s != http.StatusTemporaryRedirect && s != http.StatusPermanentRedirect {
		return http.ErrUseLastResponse
	}

	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}

	return nil
}

// Close drops idle connections. Requests in progress go on.
func (c *Client) Close() {
	c.transport.CloseIdleConnections()
}

// Created is the result of a create. URI is the Individual Application Session Context, the target of later
// modifications and of the deletion. Context is the PCF's representation of it, absent when Existing.
type Created struct {
	URI      string
	Context  *AppSessionContext
	Existing bool
}

// Create provisions service information in a new application session context (TS 29.514 §4.2.2.2). When the
// PCF created the context but answered with a body that cannot be read, the error wraps ErrMalformedResponse
// and Created.URI is set, so that the context can be deleted.
func (c *Client) Create(ctx context.Context, asc *AppSessionContext) (Created, error) {
	if err := validateCreate(asc); err != nil {
		return Created{}, err
	}

	body, err := json.Marshal(asc)
	if err != nil {
		return Created{}, err
	}

	resp, err := c.do(ctx, OpCreate, http.MethodPost, c.root.String()+appSessionsPath, ContentJSON, body)
	if err != nil {
		return Created{}, err
	}

	switch resp.status {
	case http.StatusCreated, http.StatusSeeOther:
	default:
		return Created{}, resp.err()
	}

	uri, err := resp.location()
	if err != nil {
		return Created{}, resp.malformed(err)
	}

	out := Created{URI: uri, Existing: resp.status == http.StatusSeeOther}
	if out.Existing {
		return out, nil
	}

	out.Context = new(AppSessionContext)
	if err := resp.decode(out.Context); err != nil {
		out.Context = nil
		return out, err
	}

	return out, nil
}

// Modify patches the service information of an application session context (TS 29.514 §4.2.3.2). The PCF may
// answer with the updated context, or with nothing.
func (c *Client) Modify(ctx context.Context, uri string, p Patch) (*AppSessionContext, error) {
	target, err := c.resource(uri, "")
	if err != nil {
		return nil, err
	}

	body, _ := p.MarshalJSON()

	resp, err := c.do(ctx, OpModify, http.MethodPatch, target, ContentMergePatch, body)
	if err != nil {
		return nil, err
	}

	return resp.context()
}

// Delete ends an application session context (TS 29.514 §4.2.4.2). With ev, the PCF reports those events in
// the response.
func (c *Client) Delete(ctx context.Context, uri string, ev *EventsSubscReqData) (*AppSessionContext, error) {
	target, err := c.resource(uri, deleteOperation)
	if err != nil {
		return nil, err
	}

	var (
		body        []byte
		contentType string
	)

	if ev != nil {
		if body, err = json.Marshal(ev); err != nil {
			return nil, err
		}

		contentType = ContentJSON
	}

	resp, err := c.do(ctx, OpDelete, http.MethodPost, target, contentType, body)
	if err != nil {
		return nil, err
	}

	return resp.context()
}

// TS 29.514 §5.6.2.3: notifUri and suppFeat are required, and exactly one UE address.
func validateCreate(asc *AppSessionContext) error {
	if asc == nil || asc.AscReqData == nil {
		return errors.New("create: no ascReqData")
	}

	r := asc.AscReqData

	switch {
	case r.NotifURI == "":
		return errors.New("create: no notifUri")
	case r.SuppFeat == "" || !r.SuppFeat.Valid():
		return fmt.Errorf("create: invalid suppFeat %q", string(r.SuppFeat))
	case r.UEIPv4.IsValid() == r.UEIPv6.IsValid():
		return errors.New("create: want one of ueIpv4 and ueIpv6")
	case r.UEIPv4.IsValid() && !r.UEIPv4.Is4(), r.UEIPv6.IsValid() && (!r.UEIPv6.Is6() || r.UEIPv6.Is4In6() || r.UEIPv6.Zone() != ""):
		return errors.New("create: UE address of the wrong family")
	}

	if r.EvSubsc != nil && len(r.EvSubsc.Events) == 0 {
		return errors.New("create: events subscription without events")
	}

	return nil
}

// resource returns the URI of an application session context, or of one of its custom operations. A relative
// URI is taken from the API root's authority.
func (c *Client) resource(uri, operation string) (string, error) {
	u, err := c.root.Parse(uri)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return "", fmt.Errorf("invalid application session context URI %q", uri)
	}

	if operation != "" {
		u = u.JoinPath(operation)
	}

	return u.String(), nil
}

type response struct {
	op     Op
	status int
	header http.Header
	body   []byte
	url    *url.URL
	now    time.Time
}

func (c *Client) do(ctx context.Context, op Op, method, target, contentType string, body []byte) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	req.Header.Set("Accept", ContentJSON+", "+ContentProblem)
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, transportError(op, err)
	}

	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err == nil && len(b) > maxBody {
		err = fmt.Errorf("response body over %d bytes", maxBody)
	}

	r := &response{op: op, status: resp.StatusCode, header: resp.Header, body: b, url: resp.Request.URL, now: c.now()}

	if err != nil {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil, r.malformed(err)
		}

		return nil, &Error{Op: op, Status: resp.StatusCode, Err: err}
	}

	return r, nil
}

// context reads the response to a modification or a deletion: 200 with the context, or 204.
func (r *response) context() (*AppSessionContext, error) {
	switch r.status {
	case http.StatusOK:
		asc := new(AppSessionContext)
		if err := r.decode(asc); err != nil {
			return nil, err
		}

		return asc, nil
	case http.StatusNoContent:
		return nil, nil
	}

	return nil, r.err()
}

// err is the error for a status the operation does not succeed with (TS 29.500 §5.2.7, TS 29.514 §5.7). A
// success status the operation does not define is a malformed response.
func (r *response) err() error {
	if r.status >= 200 && r.status < 300 {
		return r.malformed(fmt.Errorf("unexpected status %d", r.status))
	}

	e := &Error{Op: r.op, Status: r.status, RetryAfter: retryAfter(r.header, r.now)}

	if len(r.body) > 0 && mediaType(r.header) != "" {
		var p ProblemDetails
		if json.Unmarshal(r.body, &p) == nil {
			e.Problem = &p
		}
	}

	if r.status >= 300 && r.status < 400 {
		e.Err = fmt.Errorf("unexpected redirection to %q", r.header.Get("Location"))
	}

	return e
}

func (r *response) malformed(err error) error {
	return &Error{Op: r.op, Status: r.status, Err: fmt.Errorf("%w: %w", ErrMalformedResponse, err)}
}

// location returns the Individual Application Session Context the Location header points to. TS 29.514
// §5.3.2.3.1 also allows the URI of its events subscription, which stands for the same context.
func (r *response) location() (string, error) {
	loc := r.header.Get("Location")
	if loc == "" {
		return "", errors.New("no Location")
	}

	u, err := r.url.Parse(loc)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return "", fmt.Errorf("invalid Location %q", loc)
	}

	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/"+eventsSubscription)
	u.RawPath = ""

	// TS 29.514 §5.3.3.2: {apiRoot}/npcf-policyauthorization/v1/app-sessions/{appSessionId}
	if dir, id := path.Split(u.Path); id == "" || !strings.HasSuffix(dir, appSessionsPath+"/") {
		return "", fmt.Errorf("location %q is not an application session context", loc)
	}

	return u.String(), nil
}

// decode reads the JSON body into v (TS 29.514 §5.2.2.2).
func (r *response) decode(v any) error {
	if len(r.body) == 0 {
		return r.malformed(errors.New("no body"))
	}

	if t := mediaType(r.header); t != ContentJSON {
		return r.malformed(fmt.Errorf("content type %q", r.header.Get("Content-Type")))
	}

	if err := json.Unmarshal(r.body, v); err != nil {
		return r.malformed(err)
	}

	return nil
}

// mediaType returns the JSON media type of the body, if it has one.
func mediaType(h http.Header) string {
	t, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil {
		return ""
	}

	switch t {
	case ContentJSON, ContentProblem:
		return t
	}

	return ""
}
