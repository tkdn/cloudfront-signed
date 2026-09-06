package main

import (
	"strings"
	"testing"
	"time"
)

func TestConfirmToken_RoundTrip(t *testing.T) {
	id := "users/alice/abc123.png"
	token := newConfirmToken(id, time.Now().Add(15*time.Minute))

	if !verifyConfirmToken(id, token) {
		t.Fatalf("verifyConfirmToken(%q, %q) = false, want true", id, token)
	}
}

func TestConfirmToken_WrongID(t *testing.T) {
	token := newConfirmToken("users/alice/abc123.png", time.Now().Add(15*time.Minute))

	if verifyConfirmToken("users/bob/abc123.png", token) {
		t.Fatalf("verifyConfirmToken with wrong id = true, want false")
	}
}

func TestConfirmToken_Expired(t *testing.T) {
	id := "users/alice/abc123.png"
	token := newConfirmToken(id, time.Now().Add(-1*time.Minute))

	if verifyConfirmToken(id, token) {
		t.Fatalf("verifyConfirmToken with expired token = true, want false")
	}
}

func TestConfirmToken_TamperedSignature(t *testing.T) {
	id := "users/alice/abc123.png"
	token := newConfirmToken(id, time.Now().Add(15*time.Minute))
	parts := strings.SplitN(token, ".", 2)
	tampered := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA." + parts[1]

	if verifyConfirmToken(id, tampered) {
		t.Fatalf("verifyConfirmToken with tampered signature = true, want false")
	}
}

func TestConfirmToken_MalformedToken(t *testing.T) {
	if verifyConfirmToken("users/alice/abc123.png", "not-a-valid-token") {
		t.Fatalf("verifyConfirmToken with malformed token = true, want false")
	}
}
