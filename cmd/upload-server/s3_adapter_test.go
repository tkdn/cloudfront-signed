package main

import (
	"testing"
)

func TestPostPolicyForm_FieldsShape(t *testing.T) {
	form := postPolicyForm{
		URL: "https://bucket.s3.amazonaws.com/",
		Fields: map[string]string{
			"key":    "users/alice/abc123.png",
			"policy": "eyJ...",
		},
	}
	if form.URL == "" {
		t.Fatalf("URL must not be empty")
	}
	if form.Fields["key"] != "users/alice/abc123.png" {
		t.Fatalf("Fields[key] = %q, unexpected", form.Fields["key"])
	}
}
