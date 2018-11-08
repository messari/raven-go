package raven

import (
	"crypto/tls"
	"crypto/x509"
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
