package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"proctor/protocol"
)

var credentialCodes = map[string]bool{
	"UnrecognizedClientException":         true,
	"AccessDeniedException":               true,
	"AccessDenied":                        true,
	"InvalidSignatureException":           true,
	"SignatureDoesNotMatch":               true,
	"IncompleteSignature":                 true,
	"IncompleteSignatureException":        true,
	"InvalidClientTokenId":                true,
	"MissingAuthenticationToken":          true,
	"MissingAuthenticationTokenException": true,
	"ExpiredTokenException":               true,
	"UnknownService":                      true,
}

var validationCodes = map[string]bool{
	"ValidationException":    true,
	"SerializationException": true,
}

func (d *driver) classify(ctx context.Context, err error, what string) *protocol.Error {
	var api smithy.APIError
	var de *smithy.DeserializationError
	var unknownCA x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	var verify *tls.CertificateVerificationError
	var netErr net.Error
	var send *smithyhttp.RequestSendError
	switch {
	case errors.As(err, &api):
		code := api.ErrorCode()
		msg := fmt.Sprintf("%s: %s (%s)", what, d.scrub(api.ErrorMessage()), code)
		switch {
		case credentialCodes[code]:
			return protocol.Lastingf(protocol.CauseQueryError, "%s; the credential or its permissions were refused", msg)
		case validationCodes[code]:
			return protocol.Lastingf(protocol.CauseInvalidRequest, "%s", msg)
		}
		return protocol.Errorf(protocol.CauseQueryError, "%s", msg)
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
		return protocol.Errorf(protocol.CauseTimeout, "%s: timed out", what)
	case errors.As(err, &verify) || errors.As(err, &unknownCA) || errors.As(err, &hostErr) || errors.As(err, &invalidCert):
		return protocol.Lastingf(protocol.CauseUnreachable, "%s: the endpoint's TLS certificate isn't trusted (check ca_bundle): %s", what, d.scrub(err.Error()))
	case errors.As(err, &netErr) && netErr.Timeout():
		return protocol.Errorf(protocol.CauseTimeout, "%s: timed out: %s", what, d.scrub(err.Error()))
	case errors.As(err, &de):
		return protocol.Errorf(protocol.CauseUnreadableResponse, "%s: the response can't be read: %s", what, d.scrub(err.Error()))
	case errors.As(err, &send) || errors.As(err, &netErr):
		return protocol.Errorf(protocol.CauseUnreachable, "%s: %s", what, d.scrub(err.Error()))
	}
	return protocol.Errorf(protocol.CauseQueryError, "%s: %s", what, d.scrub(err.Error()))
}

func (d *driver) scrub(s string) string {
	for _, secret := range d.secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}
