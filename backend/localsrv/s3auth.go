package localsrv

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AWS Signature Version 4 verification for the experimental S3 service.
//
// The handler used to accept the configured access/secret keys but never
// verify anything, so the "access key" fields were decoration: any client
// (or anyone on the LAN) could read, write and delete the whole object store.
// When both keys are configured every request now has to carry a valid
// header-based SigV4 Authorization; when they are empty the server keeps the
// documented `--no-sign-request` behaviour.

const (
	s3SigV4Algorithm = "AWS4-HMAC-SHA256"
	s3TimeLayout     = "20060102T150405Z"
	s3MaxClockSkew   = 15 * time.Minute
)

// s3AuthEnabled reports whether the operator configured credentials. Both must
// be set; StartS3Server rejects a half-configured pair.
func (cfg S3Config) s3AuthEnabled() bool {
	return cfg.AccessKey != "" && cfg.SecretKey != ""
}

// verifyS3RequestAuth validates the SigV4 Authorization header of r. It is a
// no-op in anonymous mode. Errors are intentionally coarse (no distinction
// between unknown key and bad signature beyond the message) so the endpoint
// does not become an access-key oracle.
func verifyS3RequestAuth(r *http.Request, cfg S3Config) error {
	if !cfg.s3AuthEnabled() {
		return nil
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" {
		// Presigned query-string auth is not implemented; refuse explicitly
		// instead of silently accepting an unsigned request.
		if r.URL.Query().Get("X-Amz-Signature") != "" {
			return fmt.Errorf("presigned URLs are not supported by this server")
		}
		return fmt.Errorf("missing Authorization header")
	}
	if !strings.HasPrefix(auth, s3SigV4Algorithm+" ") {
		return fmt.Errorf("unsupported authorization algorithm")
	}

	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(auth, s3SigV4Algorithm+" "), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	credential, signature := fields["Credential"], fields["Signature"]
	signedHeaders := fields["SignedHeaders"]
	if credential == "" || signature == "" || signedHeaders == "" {
		return fmt.Errorf("malformed Authorization header")
	}

	// Credential = <access-key>/<date>/<region>/<service>/aws4_request
	scope := strings.Split(credential, "/")
	if len(scope) != 5 {
		return fmt.Errorf("malformed Credential scope")
	}
	accessKey, date, region, service, terminator := scope[0], scope[1], scope[2], scope[3], scope[4]
	if subtle.ConstantTimeCompare([]byte(accessKey), []byte(cfg.AccessKey)) != 1 {
		return fmt.Errorf("invalid access key")
	}
	if service != "s3" || terminator != "aws4_request" {
		return fmt.Errorf("unexpected credential scope")
	}

	amzDate := strings.TrimSpace(r.Header.Get("X-Amz-Date"))
	if amzDate == "" {
		return fmt.Errorf("missing X-Amz-Date header")
	}
	requestTime, err := time.Parse(s3TimeLayout, amzDate)
	if err != nil {
		return fmt.Errorf("malformed X-Amz-Date header")
	}
	skew := time.Since(requestTime)
	if skew < 0 {
		skew = -skew
	}
	if skew > s3MaxClockSkew {
		return fmt.Errorf("request time too skewed")
	}

	// The client declares the payload hash it signed. Streaming/chunked
	// payloads use a sentinel (STREAMING-…); either way the value is part of
	// the signed canonical request, so the header signature still binds it.
	payloadHash := strings.TrimSpace(r.Header.Get("X-Amz-Content-Sha256"))
	if payloadHash == "" {
		return fmt.Errorf("missing x-amz-content-sha256 header")
	}

	canonicalHeaders, err := s3CanonicalHeaders(r, signedHeaders)
	if err != nil {
		return err
	}
	canonicalRequest := strings.Join([]string{
		r.Method,
		r.URL.EscapedPath(),
		s3CanonicalQuery(r.URL.RawQuery),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	stringToSign := strings.Join([]string{
		s3SigV4Algorithm,
		amzDate,
		strings.Join([]string{date, region, service, terminator}, "/"),
		s3SHA256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := s3SigningKey(cfg.SecretKey, date, region, service)
	want := hex.EncodeToString(s3HMAC(signingKey, []byte(stringToSign)))
	if subtle.ConstantTimeCompare([]byte(want), []byte(strings.ToLower(signature))) != 1 {
		return fmt.Errorf("signature does not match")
	}
	return nil
}

// s3CanonicalHeaders renders the signed headers block (lowercase names in the
// client's order, single space after the colon, one trailing newline each).
func s3CanonicalHeaders(r *http.Request, signedHeaders string) (string, error) {
	names := strings.Split(signedHeaders, ";")
	var b strings.Builder
	for _, name := range names {
		if name == "" {
			return "", fmt.Errorf("malformed SignedHeaders")
		}
		var value string
		switch name {
		case "host":
			value = r.Host
		case "content-length":
			// Go's net/http promotes Content-Length out of r.Header, but S3
			// clients (mc, aws-cli) routinely include it in SignedHeaders.
			if r.ContentLength < 0 {
				return "", fmt.Errorf("signed header %q is missing from the request", name)
			}
			value = strconv.FormatInt(r.ContentLength, 10)
		default:
			values := r.Header.Values(name)
			if len(values) == 0 {
				// The client signed a header it did not send (or Go stored it
				// under a different canonical spelling); either way the
				// computed canonical request cannot match.
				return "", fmt.Errorf("signed header %q is missing from the request", name)
			}
			trimmed := make([]string, len(values))
			for i, v := range values {
				trimmed[i] = strings.Join(strings.Fields(v), " ")
			}
			value = strings.Join(trimmed, ",")
		}
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(strings.TrimSpace(value))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// s3CanonicalQuery renders the canonical query string: parameters sorted by
// encoded name then value, with RFC 3986 encoding (space is %20, not '+').
func s3CanonicalQuery(raw string) string {
	type pair struct{ k, v string }
	pairs := make([]pair, 0, 8)
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		dk, errK := url.PathUnescape(k)
		if errK != nil {
			dk = k
		}
		dv, errV := url.PathUnescape(v)
		if errV != nil {
			dv = v
		}
		pairs = append(pairs, pair{s3Encode(dk), s3Encode(dv)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	encoded := make([]string, len(pairs))
	for i, p := range pairs {
		encoded[i] = p.k + "=" + p.v
	}
	return strings.Join(encoded, "&")
}

// s3Encode percent-encodes s the way SigV4 requires: every byte except the
// unreserved set A-Z a-z 0-9 - _ . ~ is escaped with uppercase hex.
func s3Encode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

// s3SigningKey derives the SigV4 signing key for one request scope.
func s3SigningKey(secret, date, region, service string) []byte {
	kDate := s3HMAC([]byte("AWS4"+secret), []byte(date))
	kRegion := s3HMAC(kDate, []byte(region))
	kService := s3HMAC(kRegion, []byte(service))
	return s3HMAC(kService, []byte("aws4_request"))
}

func s3HMAC(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func s3SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
