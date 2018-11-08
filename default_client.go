package raven

// Initialize a default *Client instance
var DefaultClient = newClient(nil)

// CapturePanic calls f and then recovers and reports a panic to the Sentry server if it occurs.
// If an error is captured, both the error and the reported Sentry error ID are returned.
func CapturePanic(f func(), tags map[string]string, interfaces ...Interface) (interface{}, string) {
	return DefaultClient.CapturePanic(f, tags, interfaces...)
}

// CapturePanicAndWait is identical to CaptureError, except it blocks and assures that the event was sent
func CapturePanicAndWait(f func(), tags map[string]string, interfaces ...Interface) (interface{}, string, error) {
	return DefaultClient.CapturePanicAndWait(f, tags, interfaces...)
}

// CaptureErrors formats and delivers an error to the Sentry server using the default *Client.
// Adds a stacktrace to the packet, excluding the call to this method.
func CaptureError(err error, tags map[string]string, interfaces ...Interface) string {
	return DefaultClient.CaptureError(err, tags, interfaces...)
}

// CaptureErrorAndWait is identical to CaptureError, except it blocks and assures that the event was sent
func CaptureErrorAndWait(err error, tags map[string]string, interfaces ...Interface) (string, error) {
	return DefaultClient.CaptureErrorAndWait(err, tags, interfaces...)
}

// CaptureMessage formats and delivers a string message to the Sentry server with the default *Client
func CaptureMessage(message string, tags map[string]string, interfaces ...Interface) string {
	return DefaultClient.CaptureMessage(message, tags, interfaces...)
}

// CaptureMessageAndWait is identical to CaptureMessage except it blocks and waits for the message to be sent.
func CaptureMessageAndWait(message string, tags map[string]string, interfaces ...Interface) (string, error) {
	return DefaultClient.CaptureMessageAndWait(message, tags, interfaces...)
}

// Capture asynchronously delivers a packet to the Sentry server with the default *Client.
// It is a no-op when client is nil. A channel is provided if it is important to check for a
// send's success.
func Capture(packet *Packet, captureTags map[string]string) (eventID string, ch chan error) {
	return DefaultClient.Capture(packet, captureTags)
}

// Sets the DSN for the default *Client instance
func SetDSN(dsn string) error { return DefaultClient.SetDSN(dsn) }

// SetRelease sets the "release" tag on the default *Client
func SetRelease(release string) { DefaultClient.SetRelease(release) }

// SetEnvironment sets the "environment" tag on the default *Client
func SetEnvironment(environment string) { DefaultClient.SetEnvironment(environment) }

// SetDefaultLoggerName sets the "defaultLoggerName" on the default *Client
func SetDefaultLoggerName(name string) {
	DefaultClient.SetDefaultLoggerName(name)
}

func Close() { DefaultClient.Close() }

// Wait blocks and waits for all events to finish being sent to Sentry server
func Wait() { DefaultClient.Wait() }

func URL() string { return DefaultClient.URL() }

func ProjectID() string { return DefaultClient.ProjectID() }

func Release() string { return DefaultClient.Release() }

func IncludePaths() []string { return DefaultClient.IncludePaths() }

func SetIncludePaths(p []string) { DefaultClient.SetIncludePaths(p) }

func SetUserContext(u *User)             { DefaultClient.SetUserContext(u) }
func SetHttpContext(h *Http)             { DefaultClient.SetHttpContext(h) }
func SetTagsContext(t map[string]string) { DefaultClient.SetTagsContext(t) }
func ClearContext()                      { DefaultClient.ClearContext() }

func SetIgnoreErrors(errs ...string) error {
	return DefaultClient.SetIgnoreErrors(errs)
}

// SetSampleRate sets the "sample rate" on the degault *Client
func SetSampleRate(rate float32) error { return DefaultClient.SetSampleRate(rate) }
