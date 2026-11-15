package tunnelconfig

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ManagedInput struct {
	URL      string   `json:"url"`
	InletIDs []string `json:"inlet_ids,omitempty"`
}

func ValidateEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery {
		return nil, errors.New("provisioning URL must be absolute and have no userinfo, query or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("provisioning requires HTTPS except for loopback development")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	token, err := hex.DecodeString(parts[len(parts)-1])
	if err != nil || len(token) != 32 {
		return nil, errors.New("provisioning URL must end with a 64-character API secret")
	}
	return u, nil
}

func DecodeManagedInput(r io.Reader) (ManagedInput, error) {
	var in ManagedInput
	b, err := io.ReadAll(io.LimitReader(r, (16<<10)+1))
	if err != nil {
		return in, err
	}
	if len(b) > 16<<10 {
		return in, errors.New("managed input exceeds 16 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&in); err != nil {
		return in, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return in, errors.New("managed input must contain one JSON object")
	}
	if _, err = ValidateEndpoint(in.URL); err != nil {
		return in, err
	}
	seen := map[string]bool{}
	for _, id := range in.InletIDs {
		if !validID(id) || seen[id] {
			return in, errors.New("inlet_ids must be valid and unique")
		}
		seen[id] = true
	}
	return in, nil
}

// DecodeResponse requires the established encrypted provisioning envelope. The
// API URL is a bearer credential and must never be included in an error/log.
func DecodeResponse(data []byte, endpoint string) (ManagedProfile, error) {
	var empty ManagedProfile
	u, err := ValidateEndpoint(endpoint)
	if err != nil {
		return empty, err
	}
	if len(data) == 0 || len(data) > 128<<10 {
		return empty, errors.New("invalid provisioning response size")
	}
	var envelope struct {
		Version int    `json:"v"`
		Nonce   string `json:"n"`
		Data    string `json:"d"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&envelope) != nil || d.Decode(new(any)) != io.EOF || envelope.Version != 1 {
		return empty, errors.New("invalid encrypted provisioning envelope")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	token, _ := hex.DecodeString(parts[len(parts)-1])
	mac := hmac.New(sha256.New, token)
	_, _ = mac.Write([]byte("mpx-provision-config-envelope-v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return empty, errors.New("invalid provisioning cipher")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return empty, errors.New("invalid provisioning cipher")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return empty, errors.New("invalid provisioning nonce")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(envelope.Data)
	if err != nil || len(sealed) > MaxDocumentBytes+gcm.Overhead() {
		return empty, errors.New("invalid encrypted provisioning payload")
	}
	plain, err := gcm.Open(nil, nonce, sealed, []byte("mpx-provision-envelope-v1"))
	if err != nil {
		return empty, errors.New("provisioning authentication failed")
	}
	return DecodeManaged(bytes.NewReader(plain))
}

func Fetch(ctx context.Context, endpoint string) (ManagedProfile, error) {
	var empty ManagedProfile
	u, err := ValidateEndpoint(endpoint)
	if err != nil {
		return empty, err
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are disabled") }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return empty, errors.New("invalid provisioning request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return empty, ctx.Err()
		}
		return empty, errors.New("provisioning fetch failed; check network, TLS and server availability")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return empty, fmt.Errorf("provisioning HTTP status %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (128<<10)+1))
	if err != nil {
		return empty, errors.New("provisioning response read failed")
	}
	return DecodeResponse(data, endpoint)
}
