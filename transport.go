package raven

import (
	"bytes"
	"compress/zlib"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/certifi/gocertifi"
)

func certpool() (*x509.CertPool, error) {
	systemPool, err := x509.SystemCertPool()

	if err == nil && len(systemPool.Subjects()) > 0 {
		return systemPool, nil
	}
	return gocertifi.CACerts()
}

// helper for Options.CertPool
func DefaultCertPoolWithExtra(certs []*x509.Certificate) (*x509.CertPool, error) {
	pool, err := certpool()

	if err != nil {
		return nil, err
	}

	for _, cert := range certs {
		pool.AddCert(cert)
	}

	return pool, nil
}

func newTransport(opts *TransportOptions) Transport {
	t := &HTTPTransport{}

	var pool *x509.CertPool
	var err error

	if opts != nil && opts.CertPool != nil {
		pool = opts.CertPool
	} else {
		pool, err = certpool()

		if err != nil {
			log.Println("raven: failed to load root TLS certificates:", err)
		}
	}

	t.Client = &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}

	return t
}

type TransportOptions struct {
	CertPool *x509.CertPool
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
