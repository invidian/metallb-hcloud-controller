// +build e2e

package main

import (
	"testing"
)

func TestNewHcloudClient(t *testing.T) {
	if _, err := newHcloudClient(); err != nil {
		t.Fatalf("Creating client with valid token should work. Did you set %q environment variable", hcloudTokenEnv)
	}
}
