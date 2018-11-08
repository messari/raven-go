// Package raven implements a client for the Sentry error logging service.
package raven

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	mrand "math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	pkgErrors "github.com/pkg/errors"
)

const (
	userAgent       = "raven-go/1.0"
	timestampFormat = `"2006-01-02T15:04:05.00"`
)

var (
	ErrPacketDropped         = errors.New("raven: packet dropped")
	ErrUnableToUnmarshalJSON = errors.New("raven: unable to unmarshal JSON")
	ErrMissingUser           = errors.New("raven: dsn missing public key and/or password")
	ErrMissingProjectID      = errors.New("raven: dsn missing project id")
	ErrInvalidSampleRate     = errors.New("raven: sample rate should be between 0 and 1")
)

type Severity string

// http://docs.python.org/2/howto/logging.html#logging-levels
const (
	DEBUG   = Severity("debug")
	INFO    = Severity("info")
	WARNING = Severity("warning")
	ERROR   = Severity("error")
	FATAL   = Severity("fatal")
)

type Timestamp time.Time

func (t Timestamp) MarshalJSON() ([]byte, error) {
	return []byte(time.Time(t).UTC().Format(timestampFormat)), nil
}

func (timestamp *Timestamp) UnmarshalJSON(data []byte) error {
	t, err := time.Parse(timestampFormat, string(data))
	if err != nil {
		return err
	}

	*timestamp = Timestamp(t)
	return nil
}

func (timestamp Timestamp) Format(format string) string {
	t := time.Time(timestamp)
	return t.Format(format)
}

// An Interface is a Sentry interface that will be serialized as JSON.
// It must implement json.Marshaler or use json struct tags.
type Interface interface {
	// The Sentry class name. Example: sentry.interfaces.Stacktrace
	Class() string
}

type Culpriter interface {
	Culprit() string
}

type Transport interface {
	Send(url, authHeader string, packet *Packet) error
}

type Extra map[string]interface{}

type outgoingPacket struct {
	packet *Packet
	ch     chan error
}

type Tag struct {
	Key   string
	Value string
}

type Tags []Tag

func (tag *Tag) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]string{tag.Key, tag.Value})
}

func (t *Tag) UnmarshalJSON(data []byte) error {
	var tag [2]string
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}
	*t = Tag{tag[0], tag[1]}
	return nil
}

func (t *Tags) UnmarshalJSON(data []byte) error {
	var tags []Tag

	switch data[0] {
	case '[':
		// Unmarshal into []Tag
		if err := json.Unmarshal(data, &tags); err != nil {
			return err
		}
	case '{':
		// Unmarshal into map[string]string
		tagMap := make(map[string]string)
		if err := json.Unmarshal(data, &tagMap); err != nil {
			return err
		}

		// Convert to []Tag
		for k, v := range tagMap {
			tags = append(tags, Tag{k, v})
		}
	default:
		return ErrUnableToUnmarshalJSON
	}

	*t = tags
	return nil
}

// https://docs.getsentry.com/hosted/clientdev/#building-the-json-packet
type Packet struct {
	// Required
	Message string `json:"message"`

	// Required, set automatically by Client.Send/Report via Packet.Init if blank
	EventID   string    `json:"event_id"`
	Project   string    `json:"project"`
	Timestamp Timestamp `json:"timestamp"`
	Level     Severity  `json:"level"`
	Logger    string    `json:"logger"`

	// Optional
	Platform    string            `json:"platform,omitempty"`
	Culprit     string            `json:"culprit,omitempty"`
	ServerName  string            `json:"server_name,omitempty"`
	Release     string            `json:"release,omitempty"`
	Environment string            `json:"environment,omitempty"`
	Tags        Tags              `json:"tags,omitempty"`
	Modules     map[string]string `json:"modules,omitempty"`
	Fingerprint []string          `json:"fingerprint,omitempty"`
	Extra       Extra             `json:"extra,omitempty"`

	Interfaces []Interface `json:"-"`
}

// NewPacket constructs a packet with the specified message and interfaces.
func NewPacket(message string, interfaces ...Interface) *Packet {
	extra := Extra{}
	setExtraDefaults(extra)
	return &Packet{
		Message:    message,
		Interfaces: interfaces,
		Extra:      extra,
	}
}

// NewPacketWithExtra constructs a packet with the specified message, extra information, and interfaces.
func NewPacketWithExtra(message string, extra Extra, interfaces ...Interface) *Packet {
	if extra == nil {
		extra = Extra{}
	}
	setExtraDefaults(extra)

	return &Packet{
		Message:    message,
		Interfaces: interfaces,
		Extra:      extra,
	}
}

func setExtraDefaults(extra Extra) Extra {
	extra["runtime.Version"] = runtime.Version()
	extra["runtime.NumCPU"] = runtime.NumCPU()
	extra["runtime.GOMAXPROCS"] = runtime.GOMAXPROCS(0) // 0 just returns the current value
	extra["runtime.NumGoroutine"] = runtime.NumGoroutine()
	return extra
}

// Init initializes required fields in a packet. It is typically called by
// Client.Send/Report automatically.
func (packet *Packet) Init(project string) error {
	if packet.Project == "" {
		packet.Project = project
	}
	if packet.EventID == "" {
		var err error
		packet.EventID, err = uuid()
		if err != nil {
			return err
		}
	}
	if time.Time(packet.Timestamp).IsZero() {
		packet.Timestamp = Timestamp(time.Now())
	}
	if packet.Level == "" {
		packet.Level = ERROR
	}
	if packet.Logger == "" {
		packet.Logger = "root"
	}
	if packet.ServerName == "" {
		packet.ServerName = hostname
	}
	if packet.Platform == "" {
		packet.Platform = "go"
	}

	if packet.Culprit == "" {
		for _, inter := range packet.Interfaces {
			if c, ok := inter.(Culpriter); ok {
				packet.Culprit = c.Culprit()
				if packet.Culprit != "" {
					break
				}
			}
		}
	}

	return nil
}

func (packet *Packet) AddTags(tags map[string]string) {
	for k, v := range tags {
		packet.Tags = append(packet.Tags, Tag{k, v})
	}
}

func uuid() (string, error) {
	id := make([]byte, 16)
	_, err := io.ReadFull(rand.Reader, id)
	if err != nil {
		return "", err
	}
	id[6] &= 0x0F // clear version
	id[6] |= 0x40 // set version to 4 (random uuid)
	id[8] &= 0x3F // clear variant
	id[8] |= 0x80 // set to IETF variant
	return hex.EncodeToString(id), nil
}

func (packet *Packet) JSON() ([]byte, error) {
	packetJSON, err := json.Marshal(packet)
	if err != nil {
		return nil, err
	}

	interfaces := make(map[string]Interface, len(packet.Interfaces))
	for _, inter := range packet.Interfaces {
		if inter != nil {
			interfaces[inter.Class()] = inter
		}
	}

	if len(interfaces) > 0 {
		interfaceJSON, err := json.Marshal(interfaces)
		if err != nil {
			return nil, err
		}
		packetJSON[len(packetJSON)-1] = ','
		packetJSON = append(packetJSON, interfaceJSON[1:]...)
	}

	return packetJSON, nil
}

type context struct {
	user *User
	http *Http
	tags map[string]string
}

func (c *context) setUser(u *User) { c.user = u }
func (c *context) setHttp(h *Http) { c.http = h }
func (c *context) setTags(t map[string]string) {
	if c.tags == nil {
		c.tags = make(map[string]string)
	}
	for k, v := range t {
		c.tags[k] = v
	}
}
func (c *context) clear() {
	c.user = nil
	c.http = nil
	c.tags = nil
}

// Return a list of interfaces to be used in appending with the rest
func (c *context) interfaces() []Interface {
	len, i := 0, 0
	if c.user != nil {
		len++
	}
	if c.http != nil {
		len++
	}
	interfaces := make([]Interface, len)
	if c.user != nil {
		interfaces[i] = c.user
		i++
	}
	if c.http != nil {
		interfaces[i] = c.http
		i++
	}
	return interfaces
}

// The maximum number of packets that will be buffered waiting to be delivered.
// Packets will be dropped if the buffer is full. Used by NewClient.
var MaxQueueBuffer = 100

type Options struct {
	DSN       string
	Tags      map[string]string
	Transport TransportOptions
}

func newClient(opts *Options) *Client {
	if opts == nil {
		opts = &Options{}
	}
	client := &Client{
		Transport:  newTransport(&opts.Transport),
		Tags:       opts.Tags,
		context:    &context{},
		sampleRate: 1.0,
		queue:      make(chan *outgoingPacket, MaxQueueBuffer),
	}
	client.SetDSN(os.Getenv("SENTRY_DSN"))
	client.SetRelease(os.Getenv("SENTRY_RELEASE"))
	client.SetEnvironment(os.Getenv("SENTRY_ENVIRONMENT"))
	return client
}

// New constructs a new Sentry client instance
func New(dsn string) (*Client, error) {
	client := newClient(nil)
	return client, client.SetDSN(dsn)
}

// NewWithTags constructs a new Sentry client instance with default tags.
func NewWithTags(dsn string, tags map[string]string) (*Client, error) {
	client := newClient(&Options{Tags: tags})
	return client, client.SetDSN(dsn)
}

// NewClient constructs a Sentry client and spawns a background goroutine to
// handle packets sent by Client.Report.
func NewClient(opts *Options) (*Client, error) {
	client := newClient(opts)
	return client, client.SetDSN(opts.DSN)
}

// Client encapsulates a connection to a Sentry server. It must be initialized
// by calling NewClient. Modification of fields concurrently with Send or after
// calling Report for the first time is not thread-safe.
type Client struct {
	Tags map[string]string

	Transport Transport

	// DropHandler is called when a packet is dropped because the buffer is full.
	DropHandler func(*Packet)

	// Context that will get appending to all packets
	context *context

	mu          sync.RWMutex
	url         string
	projectID   string
	authHeader  string
	release     string
	environment string
	sampleRate  float32

	// default logger name (leave empty for 'root')
	defaultLoggerName string

	includePaths       []string
	ignoreErrorsRegexp *regexp.Regexp
	queue              chan *outgoingPacket

	// A WaitGroup to keep track of all currently in-progress captures
	// This is intended to be used with Client.Wait() to assure that
	// all messages have been transported before exiting the process.
	wg sync.WaitGroup

	// A Once to track only starting up the background worker once
	start sync.Once
}

func (c *Client) SetIgnoreErrors(errs []string) error {
	joinedRegexp := strings.Join(errs, "|")
	r, err := regexp.Compile(joinedRegexp)
	if err != nil {
		return fmt.Errorf("failed to compile regexp %q for %q: %v", joinedRegexp, errs, err)
	}

	c.mu.Lock()
	c.ignoreErrorsRegexp = r
	c.mu.Unlock()
	return nil
}

func (c *Client) shouldExcludeErr(errStr string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ignoreErrorsRegexp != nil && c.ignoreErrorsRegexp.MatchString(errStr)
}

// SetDSN updates a client with a new DSN. It safe to call after and
// concurrently with calls to Report and Send.
func (client *Client) SetDSN(dsn string) error {
	if dsn == "" {
		return nil
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	uri, err := url.Parse(dsn)
	if err != nil {
		return err
	}

	if uri.User == nil {
		return ErrMissingUser
	}
	publicKey := uri.User.Username()
	secretKey, hasSecretKey := uri.User.Password()
	uri.User = nil

	if idx := strings.LastIndex(uri.Path, "/"); idx != -1 {
		client.projectID = uri.Path[idx+1:]
		uri.Path = uri.Path[:idx+1] + "api/" + client.projectID + "/store/"
	}
	if client.projectID == "" {
		return ErrMissingProjectID
	}

	client.url = uri.String()

	if hasSecretKey {
		client.authHeader = fmt.Sprintf("Sentry sentry_version=4, sentry_key=%s, sentry_secret=%s", publicKey, secretKey)
	} else {
		client.authHeader = fmt.Sprintf("Sentry sentry_version=4, sentry_key=%s", publicKey)
	}

	return nil
}

// SetRelease sets the "release" tag.
func (client *Client) SetRelease(release string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.release = release
}

// SetEnvironment sets the "environment" tag.
func (client *Client) SetEnvironment(environment string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.environment = environment
}

// SetDefaultLoggerName sets the default logger name.
func (client *Client) SetDefaultLoggerName(name string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.defaultLoggerName = name
}

// SetSampleRate sets how much sampling we want on client side
func (client *Client) SetSampleRate(rate float32) error {
	client.mu.Lock()
	defer client.mu.Unlock()

	if rate < 0 || rate > 1 {
		return ErrInvalidSampleRate
	}
	client.sampleRate = rate
	return nil
}

func (client *Client) worker() {
	for outgoingPacket := range client.queue {

		client.mu.RLock()
		url, authHeader := client.url, client.authHeader
		client.mu.RUnlock()

		outgoingPacket.ch <- client.Transport.Send(url, authHeader, outgoingPacket.packet)
		client.wg.Done()
	}
}

// Capture asynchronously delivers a packet to the Sentry server. It is a no-op
// when client is nil. A channel is provided if it is important to check for a
// send's success.
func (client *Client) Capture(packet *Packet, captureTags map[string]string) (eventID string, ch chan error) {
	ch = make(chan error, 1)

	if client == nil {
		// return a chan that always returns nil when the caller receives from it
		close(ch)
		return
	}

	if client.sampleRate < 1.0 && mrand.Float32() > client.sampleRate {
		return
	}

	if packet == nil {
		close(ch)
		return
	}

	if client.shouldExcludeErr(packet.Message) {
		return
	}

	// Keep track of all running Captures so that we can wait for them all to finish
	// *Must* call client.wg.Done() on any path that indicates that an event was
	// finished being acted upon, whether success or failure
	client.wg.Add(1)

	// Merge capture tags and client tags
	packet.AddTags(captureTags)
	packet.AddTags(client.Tags)

	// Initialize any required packet fields
	client.mu.RLock()
	packet.AddTags(client.context.tags)
	projectID := client.projectID
	release := client.release
	environment := client.environment
	defaultLoggerName := client.defaultLoggerName
	client.mu.RUnlock()

	// set the global logger name on the packet if we must
	if packet.Logger == "" && defaultLoggerName != "" {
		packet.Logger = defaultLoggerName
	}

	err := packet.Init(projectID)
	if err != nil {
		ch <- err
		client.wg.Done()
		return
	}

	if packet.Release == "" {
		packet.Release = release
	}

	if packet.Environment == "" {
		packet.Environment = environment
	}

	outgoingPacket := &outgoingPacket{packet, ch}

	// Lazily start background worker until we
	// do our first write into the queue.
	client.start.Do(func() {
		go client.worker()
	})

	select {
	case client.queue <- outgoingPacket:
	default:
		// Send would block, drop the packet
		if client.DropHandler != nil {
			client.DropHandler(packet)
		}
		ch <- ErrPacketDropped
		client.wg.Done()
	}

	return packet.EventID, ch
}

// CaptureMessage formats and delivers a string message to the Sentry server.
func (client *Client) CaptureMessage(message string, tags map[string]string, interfaces ...Interface) string {
	id, _ := client.captureMessage(message, tags, interfaces...)
	return id
}

// CaptureMessageAndWait is identical to CaptureMessage except it blocks and waits for the message to be sent.
func (client *Client) CaptureMessageAndWait(message string, tags map[string]string, interfaces ...Interface) (string, error) {
	id, ch := client.captureMessage(message, tags, interfaces...)
	return id, <-ch
}

// CaptureMessageAndWait is identical to CaptureMessage except it blocks and waits for the message to be sent.
func (client *Client) captureMessage(message string, tags map[string]string, interfaces ...Interface) (string, <-chan error) {
	cha := make(chan error)
	if client == nil {
		close(cha)
		return "", cha
	}

	if client.shouldExcludeErr(message) {
		close(cha)
		return "", cha
	}

	packet := NewPacket(message, append(append(interfaces, client.context.interfaces()...), &Message{message, nil})...)
	eventID, ch := client.Capture(packet, tags)
	return eventID, ch
}

// CaptureErrors formats and delivers an error to the Sentry server.
// Adds a stacktrace to the packet, excluding the call to this method.
func (client *Client) CaptureError(err error, tags map[string]string, interfaces ...Interface) string {
	id, _ := client.captureError(err, tags, interfaces...)
	return id
}

// CaptureErrorAndWait is identical to CaptureError, except it blocks and assures that the event was sent
func (client *Client) CaptureErrorAndWait(err error, tags map[string]string, interfaces ...Interface) (string, error) {
	id, ch := client.captureError(err, tags, interfaces...)
	return id, <-ch
}

// CaptureErrorAndWait is identical to CaptureError, except it blocks and assures that the event was sent
func (client *Client) captureError(err error, tags map[string]string, interfaces ...Interface) (string, <-chan error) {
	cha := make(chan error)

	if err == nil {
		close(cha)
		return "", cha
	}

	if client == nil {
		close(cha)
		return "", cha
	}

	if client.shouldExcludeErr(err.Error()) {
		close(cha)
		return "", cha
	}

	extra := extractExtra(err)
	cause := pkgErrors.Cause(err)

	packet := NewPacketWithExtra(err.Error(), extra, append(append(interfaces, client.context.interfaces()...), NewException(cause, GetOrNewStacktrace(err, cause, 1, 3, client.includePaths)))...)
	eventID, ch := client.Capture(packet, tags)
	return eventID, ch
}

// CapturePanic calls f and then recovers and reports a panic to the Sentry server if it occurs.
// If an error is captured, both the error and the reported Sentry error ID are returned.
func (client *Client) CapturePanic(f func(), tags map[string]string, interfaces ...Interface) (interface{}, string) {
	err, errorID, _ := client.capturePanic(f, tags, interfaces...)
	return err, errorID
}

func (client *Client) CapturePanicAndWait(f func(), tags map[string]string, interfaces ...Interface) (interface{}, string, error) {
	err, errorID, ch := client.capturePanic(f, tags, interfaces...)
	return err, errorID, <-ch
}

// CapturePanicAndWait is identical to CaptureError, except it blocks and assures that the event was sent
func (client *Client) capturePanic(f func(), tags map[string]string, interfaces ...Interface) (err interface{}, errorID string, ch <-chan error) {
	// Note: This doesn't need to check for client, because we still want to go through the defer/recover path
	// Down the line, Capture will be noop'd, so while this does a _tiny_ bit of overhead constructing the
	// *Packet just to be thrown away, this should not be the normal case. Could be refactored to
	// be completely noop though if we cared.
	defer func() {
		var packet *Packet
		err = recover()
		cha := make(chan error)
		ch = cha
		switch rval := err.(type) {
		case nil:
			close(cha)
			return
		case error:
			if client.shouldExcludeErr(rval.Error()) {
				close(cha)
				return
			}
			packet = NewPacket(rval.Error(), append(append(interfaces, client.context.interfaces()...), NewException(rval, NewStacktrace(2, 3, client.includePaths)))...)
		default:
			rvalStr := fmt.Sprint(rval)
			if client.shouldExcludeErr(rvalStr) {
				close(cha)
				return
			}
			packet = NewPacket(rvalStr, append(append(interfaces, client.context.interfaces()...), NewException(errors.New(rvalStr), NewStacktrace(2, 3, client.includePaths)))...)
		}

		errorID, ch = client.Capture(packet, tags)
	}()

	f()
	return
}

func (client *Client) Close() {
	close(client.queue)
}

// Wait blocks and waits for all events to finish being sent to Sentry server
func (client *Client) Wait() {
	client.wg.Wait()
}

func (client *Client) URL() string {
	client.mu.RLock()
	defer client.mu.RUnlock()

	return client.url
}

func (client *Client) ProjectID() string {
	client.mu.RLock()
	defer client.mu.RUnlock()

	return client.projectID
}

func (client *Client) Release() string {
	client.mu.RLock()
	defer client.mu.RUnlock()

	return client.release
}

func (client *Client) IncludePaths() []string {
	client.mu.RLock()
	defer client.mu.RUnlock()

	return client.includePaths
}

func (client *Client) SetIncludePaths(p []string) {
	client.mu.Lock()
	defer client.mu.Unlock()

	client.includePaths = p
}

func (c *Client) SetUserContext(u *User) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.context.setUser(u)
}

func (c *Client) SetHttpContext(h *Http) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.context.setHttp(h)
}

func (c *Client) SetTagsContext(t map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.context.setTags(t)
}

func (c *Client) ClearContext() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.context.clear()
}

// HTTPTransport is the default transport, delivering packets to Sentry via the
// HTTP API.
type HTTPTransport struct {
	*http.Client
}

func (t *HTTPTransport) Send(url, authHeader string, packet *Packet) error {
	if url == "" {
		return nil
	}

	body, contentType, err := serializedPacket(packet)
	if err != nil {
		return fmt.Errorf("error serializing packet: %v", err)
	}
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return fmt.Errorf("can't create new request: %v", err)
	}
	req.Header.Set("X-Sentry-Auth", authHeader)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", contentType)
	res, err := t.Do(req)
	if err != nil {
		return err
	}
	io.Copy(ioutil.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("raven: got http status %d - x-sentry-error: %s", res.StatusCode, res.Header.Get("X-Sentry-Error"))
	}
	return nil
}

func serializedPacket(packet *Packet) (io.Reader, string, error) {
	packetJSON, err := packet.JSON()
	if err != nil {
		return nil, "", fmt.Errorf("error marshaling packet %+v to JSON: %v", packet, err)
	}

	// Only deflate/base64 the packet if it is bigger than 1KB, as there is
	// overhead.
	if len(packetJSON) > 1000 {
		buf := &bytes.Buffer{}
		b64 := base64.NewEncoder(base64.StdEncoding, buf)
		deflate, _ := zlib.NewWriterLevel(b64, zlib.BestCompression)
		deflate.Write(packetJSON)
		deflate.Close()
		b64.Close()
		return buf, "application/octet-stream", nil
	}
	return bytes.NewReader(packetJSON), "application/json", nil
}

var hostname string

func init() {
	hostname, _ = os.Hostname()
}
