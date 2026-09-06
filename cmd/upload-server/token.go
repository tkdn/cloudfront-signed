package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// confirmTokenSecret is process-local by design: this repository is a
// verification sandbox, not a multi-instance deployment, so there is no
// need to share it via flags, env vars, or Secrets Manager.
var confirmTokenSecret = []byte("DONT_USE_THIS_CODE_confirm_token_secret")

func newConfirmToken(id string, expiresAt time.Time) string {
	exp := expiresAt.Unix()
	mac := computeConfirmTokenMAC(id, exp)
	return fmt.Sprintf("%s.%d", base64.RawURLEncoding.EncodeToString(mac), exp)
}

func verifyConfirmToken(id, token string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	if time.Now().After(time.Unix(exp, 0)) {
		return false
	}
	want := computeConfirmTokenMAC(id, exp)
	return subtle.ConstantTimeCompare(sig, want) == 1
}

func computeConfirmTokenMAC(id string, exp int64) []byte {
	mac := hmac.New(sha256.New, confirmTokenSecret)
	mac.Write([]byte(id))
	mac.Write([]byte("."))
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	return mac.Sum(nil)
}
