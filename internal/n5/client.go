package n5

import (
	"bytes"
	"context"
	"crypto/tls"
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

	"github.com/ellanetworks/ims/internal/sbitls"
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
	// PCF is the API root of the PCF, http://host[:port][/prefix] or https://host[:port][/prefix] (TS 29.501
	// §4.4.1).
	PCF string
	// TLS are the credentials of the client over https, and must be nil over http.
	TLS *sbitls.Credentials
	// UserAgent defaults to DefaultUserAgent.
	UserAgent string
	// PingInterval is how long a connection may stay silent before a PING checks it (TS 29.500 §5.2.6).
	// It defaults to, and is at least, MinPingInterval.
	PingInterval time.Duration
	// Observe, if set, is called after each request with how long it took, and with the status of the response
	// or, when none came, the error.
	Observe func(status int, err error, elapsed time.Duration)
}

// Client invokes Npcf_PolicyAuthorization on one PCF, over HTTP/2: with prior knowledge for http (RFC 9113 §3.3),
// and over mutually authenticated TLS for https (TS 33.501 §13.1.0). Requests share one connection, and more open
// when the PCF's stream limit is reached (TS 29.500 §5.2.6).
type Client struct {
	root      *url.URL
	userAgent string
	tls       *sbitls.Credentials
	observe   func(status int, err error, elapsed time.Duration)
	http      *http.Client
	transport *http.Transport
	now       func() time.Time
}

func New(cfg Config) (*Client, error) {
	root, err := url.Parse(cfg.PCF)

	switch {
	case err != nil:
		return nil, fmt.Errorf("PCF URI: %w", err)
	case root.Scheme != "http" && root.Scheme != "https" || root.Host == "" || root.User != nil || root.RawQuery != "" ||
		root.Fragment != "":
		return nil, fmt.Errorf("PCF URI %q: want http[s]://host[:port][/prefix]", cfg.PCF)
	case root.Scheme == "https" && cfg.TLS == nil:
		return nil, fmt.Errorf("PCF URI %q: https without TLS credentials", cfg.PCF)
	case root.Scheme == "http" && cfg.TLS != nil:
		return nil, fmt.Errorf("PCF URI %q: TLS credentials for http", cfg.PCF)
	}

	root.Path = strings.TrimSuffix(root.Path, "/")
	root.RawPath = ""

	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}

	cfg.PingInterval = max(cfg.PingInterval, MinPingInterval)

	c := &Client{root: root, userAgent: cfg.UserAgent, tls: cfg.TLS, observe: cfg.Observe, now: time.Now}

	var protocols http.Protocols

	c.transport = &http.Transport{
		Protocols: &protocols,
		HTTP2:     &http.HTTP2Config{SendPingTimeout: cfg.PingInterval},
	}

	if c.tls == nil {
		protocols.SetUnencryptedHTTP2(true)

		c.transport.DialContext = (&net.Dialer{Timeout: dialTimeout}).DialContext
	} else {
		// HTTP/2 only, which ALPN negotiates (RFC 9113 §3.2).
		protocols.SetHTTP2(true)

		c.transport.DialTLSContext = c.dialTLS
	}

	c.http = &http.Client{Transport: c.transport, CheckRedirect: c.checkRedirect}

	return c, nil
}

// dialTLS connects to addr over TLS. A handshake that fails, like a dial, leaves the request unsent. The
// connection must carry HTTP/2: net/http would otherwise fall back to HTTP/1.1 on it.
func (c *Client) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: dialTimeout}, Config: c.tls.Client(host)}

	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnect, err)
	}

	// TS 29.500 §5.2.1, RFC 9113 §3.2
	if p := conn.(*tls.Conn).ConnectionState().NegotiatedProtocol; p != "h2" {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %s did not negotiate HTTP/2 (ALPN %q)", ErrConnect, addr, p)
	}

	return conn, nil
}

// RFC 9110 §15.4: follow 307 and 308, which repeat the request, and nothing that turns it into a GET. A 303 to
// a create is an answer, not a redirection (TS 29.514 §5.3.2.3.1). Redirections keep the scheme of the PCF URI,
// so that they cannot take a request off TLS.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if s := req.Response.StatusCode; s != http.StatusTemporaryRedirect && s != http.StatusPermanentRedirect {
		return http.ErrUseLastResponse
	}

	if req.URL.Scheme != c.root.Scheme {
		return fmt.Errorf("redirection to %q: not %s", req.URL, c.root.Scheme)
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

// Result is what the PCF answered to an operation that succeeded. A body that cannot be read leaves BodyErr set
// rather than failing the operation, which the PCF has carried out (TS 29.500 §5.2.7.3).
type Result struct {
	// URI is the Individual Application Session Context: its Location after a create, and after a modification
	// the URI a permanent redirection moved it to (TS 29.500 §6.10.9).
	URI string
	// Features are the features negotiated at creation (TS 29.514 §5.8).
	Features SupportedFeatures
	// Notification is the evsNotif of the response, if any.
	Notification *EventsNotification
	BodyErr      error
}

// Created is the result of a create. Existing means the PCF already held an equal context (303), and URI is
// that context.
type Created struct {
	Result
	Existing bool
}

// Create provisions service information in a new application session context (TS 29.514 §4.2.2.2).
func (c *Client) Create(ctx context.Context, asc *AppSessionContext) (Created, error) {
	if err := validateCreate(asc); err != nil {
		return Created{}, err
	}

	body, err := json.Marshal(asc)
	if err != nil {
		return Created{}, err
	}

	resp, err := c.do(ctx, OpCreate, http.MethodPost, c.root.String()+AppSessionsPath, ContentJSON, body)
	if err != nil {
		return Created{}, err
	}

	if !resp.success() && resp.status != http.StatusSeeOther {
		return Created{}, resp.err()
	}

	uri, err := resp.location()
	if err != nil {
		return Created{}, resp.malformed(err)
	}

	if resp.status == http.StatusSeeOther {
		return Created{Result: Result{URI: uri}, Existing: true}, nil
	}

	out := Created{Result: resp.result(true)}
	out.URI = uri

	return out, nil
}

// Modify patches the service information of an application session context (TS 29.514 §4.2.3.2).
func (c *Client) Modify(ctx context.Context, uri string, p Patch) (Result, error) {
	target, err := c.resource(uri, "")
	if err != nil {
		return Result{}, err
	}

	body, _ := p.MarshalJSON()

	resp, err := c.do(ctx, OpModify, http.MethodPatch, target, ContentMergePatch, body)
	if err != nil {
		return Result{}, err
	}

	if !resp.success() {
		return Result{}, resp.err()
	}

	out := resp.result(false)
	out.URI = resp.moved.String()

	return out, nil
}

// Delete ends an application session context (TS 29.514 §4.2.4.2). With ev, the PCF reports those events in
// the response.
func (c *Client) Delete(ctx context.Context, uri string, ev *EventsSubscReqData) (Result, error) {
	target, err := c.resource(uri, DeleteSegment)
	if err != nil {
		return Result{}, err
	}

	var (
		body        []byte
		contentType string
	)

	if ev != nil {
		if len(ev.Events) == 0 {
			return Result{}, errors.New("delete: events subscription without events")
		}

		if body, err = json.Marshal(ev); err != nil {
			return Result{}, err
		}

		contentType = ContentJSON
	}

	resp, err := c.do(ctx, OpDelete, http.MethodPost, target, contentType, body)
	if err != nil {
		return Result{}, err
	}

	if !resp.success() {
		return Result{}, resp.err()
	}

	return resp.result(false), nil
}

// TS 29.514 §5.6.2.3: notifUri and suppFeat are required, and exactly one UE address; §4.2.2.2: an events
// subscription names its events and its notification URI.
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
	case r.EvSubsc != nil && len(r.EvSubsc.Events) == 0:
		return errors.New("create: events subscription without events")
	case r.EvSubsc != nil && r.EvSubsc.NotifURI == "":
		return errors.New("create: events subscription without notifUri")
	}

	return nil
}

// resource returns the URI of an application session context, or of one of its custom operations. It takes an
// absolute URI, or an absolute path on the PCF's authority.
func (c *Client) resource(uri, operation string) (string, error) {
	ref, err := url.Parse(uri)
	if err != nil || !ref.IsAbs() && !strings.HasPrefix(uri, "/") {
		return "", fmt.Errorf("invalid application session context URI %q", uri)
	}

	u := c.root.ResolveReference(ref)
	if u.Scheme != c.root.Scheme || u.Host == "" {
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
	moved  *url.URL
	now    time.Time

	bodyErr error
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

	start := time.Now()
	resp, err := c.http.Do(req)
	c.report(resp, err, time.Since(start))

	if err != nil {
		// A redirection that was not followed still answered the request.
		if resp != nil {
			return nil, &Error{Op: op, Status: resp.StatusCode, Err: err}
		}

		return nil, transportError(op, err)
	}

	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err == nil && len(b) > maxBody {
		err = fmt.Errorf("response body over %d bytes", maxBody)
	}

	r := &response{
		op: op, status: resp.StatusCode, header: resp.Header, body: b, url: resp.Request.URL,
		moved: permanentURL(resp.Request), now: c.now(),
	}

	if err != nil {
		if r.success() {
			// The operation succeeded; only its body is lost.
			r.body, r.bodyErr = nil, err
			return r, nil
		}

		return nil, &Error{Op: op, Status: resp.StatusCode, Err: err}
	}

	return r, nil
}

// report tells Observe of a request, answered with resp or not with err.
func (c *Client) report(resp *http.Response, err error, elapsed time.Duration) {
	if c.observe == nil {
		return
	}

	status := 0
	if resp != nil {
		status = resp.StatusCode
	}

	c.observe(status, err, elapsed)
}

// permanentURL returns the URI a request ended at, following only permanent redirections (RFC 9110 §15.4.9).
func permanentURL(final *http.Request) *url.URL {
	var hops []*http.Request

	for r := final; r != nil; {
		hops = append(hops, r)

		if r.Response == nil {
			break
		}

		r = r.Response.Request
	}

	u := hops[len(hops)-1].URL

	for i := len(hops) - 2; i >= 0 && hops[i].Response.StatusCode == http.StatusPermanentRedirect; i-- {
		u = hops[i].URL
	}

	return u
}

// TS 29.500 Table 5.2.7.1-1 NOTE 2: a 2xx the operation does not define counts as its 200, or its 204 if empty.
func (r *response) success() bool {
	return r.status >= 200 && r.status < 300
}

// err is the error for a status the operation does not succeed with (TS 29.500 §5.2.7, TS 29.514 §5.7).
func (r *response) err() error {
	e := &Error{Op: r.op, Status: r.status, RetryAfter: retryAfter(r.header, r.now)}

	if len(r.body) > 0 && mediaType(r.header) != "" {
		e.Problem = problemDetails(r.body)
	}

	if r.status >= 300 && r.status < 400 {
		e.Err = fmt.Errorf("unexpected redirection to %q", r.header.Get("Location"))
	}

	return e
}

// problemDetails reads what it can of a ProblemDetails, so that one bad attribute does not hide the cause.
func problemDetails(b []byte) *ProblemDetails {
	var p ProblemDetails
	if json.Unmarshal(b, &p) == nil {
		return &p
	}

	var parts map[string]json.RawMessage
	if json.Unmarshal(b, &parts) != nil {
		return nil
	}

	p = ProblemDetails{}

	for k, v := range map[string]any{
		"type": &p.Type, "title": &p.Title, "status": &p.Status, "detail": &p.Detail, "instance": &p.Instance,
		"cause": &p.Cause,
	} {
		if raw, ok := parts[k]; ok {
			_ = json.Unmarshal(raw, v)
		}
	}

	return &p
}

func (r *response) malformed(err error) error {
	return &Error{Op: r.op, Status: r.status, Err: fmt.Errorf("%w: %w", ErrMalformedResponse, err)}
}

// location returns the Individual Application Session Context the Location header points to,
// {apiRoot}/npcf-policyauthorization/v1/app-sessions/{appSessionId} (TS 29.514 §5.3.3.2). The OpenAPI
// description of the 201 also allows the URI of its events subscription, which stands for the same context.
func (r *response) location() (string, error) {
	loc := r.header.Get("Location")
	if loc == "" {
		return "", errors.New("no Location")
	}

	u, err := r.url.Parse(loc)
	if err != nil || u.Scheme != r.url.Scheme || u.Host == "" {
		return "", fmt.Errorf("invalid Location %q", loc)
	}

	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""

	p := strings.TrimSuffix(strings.TrimSuffix(u.EscapedPath(), "/"), "/"+EventsSubscriptionSegment)

	if dir, id := path.Split(p); id == "" || !strings.HasSuffix(dir, AppSessionsPath+"/") {
		return "", fmt.Errorf("location %q is not an application session context", loc)
	}

	if u.Path, err = url.PathUnescape(p); err != nil {
		return "", fmt.Errorf("invalid Location %q", loc)
	}

	u.RawPath = p

	return u.String(), nil
}

// result reads what the client uses from an AppSessionContext body (TS 29.514 §5.2.2.2): the events reported
// and, after a create, the negotiated features.
func (r *response) result(create bool) Result {
	var out Result

	if r.bodyErr != nil {
		out.BodyErr = fmt.Errorf("%w: %w", ErrMalformedResponse, r.bodyErr)
		return out
	}

	if len(r.body) == 0 {
		if r.status == http.StatusOK || r.status == http.StatusCreated {
			out.BodyErr = fmt.Errorf("%w: no body", ErrMalformedResponse)
		}

		return out
	}

	if t := mediaType(r.header); t != ContentJSON {
		out.BodyErr = fmt.Errorf("%w: content type %q", ErrMalformedResponse, r.header.Get("Content-Type"))
		return out
	}

	var body struct {
		AscRespData json.RawMessage `json:"ascRespData"`
		EvsNotif    json.RawMessage `json:"evsNotif"`
	}

	if err := json.Unmarshal(r.body, &body); err != nil {
		out.BodyErr = fmt.Errorf("%w: %w", ErrMalformedResponse, err)
		return out
	}

	var errs []error

	if body.EvsNotif != nil {
		var n EventsNotification

		if err := json.Unmarshal(body.EvsNotif, &n); err != nil {
			errs = append(errs, fmt.Errorf("evsNotif: %w", err))
		} else {
			out.Notification = &n
		}
	}

	if create {
		f, err := features(body.AscRespData)
		if err != nil {
			errs = append(errs, err)
		}

		out.Features = f
	}

	if len(errs) > 0 {
		out.BodyErr = fmt.Errorf("%w: %w", ErrMalformedResponse, errors.Join(errs...))
	}

	return out
}

// features returns the features the PCF negotiated, in ascRespData.suppFeat (TS 29.514 §5.8).
func features(resp json.RawMessage) (SupportedFeatures, error) {
	f, err := suppFeat("ascRespData", resp)
	if err != nil || f == nil {
		return "", err
	}

	return *f, nil
}

func suppFeat(name string, raw json.RawMessage) (*SupportedFeatures, error) {
	if raw == nil {
		return nil, nil
	}

	var v struct {
		SuppFeat *SupportedFeatures `json:"suppFeat"`
	}

	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	if v.SuppFeat != nil && !v.SuppFeat.Valid() {
		return nil, fmt.Errorf("%s.suppFeat %q: invalid", name, string(*v.SuppFeat))
	}

	return v.SuppFeat, nil
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
