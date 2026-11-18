package config

import "testing"

func TestLoadTrimsAPIKey(t *testing.T) {
	t.Setenv("MYWA_API_KEY", "  0123456789abcdef \r\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "0123456789abcdef" {
		t.Fatalf("APIKey = %q, want it trimmed", c.APIKey)
	}
}

func TestLoadRejectsShortKeyAfterTrim(t *testing.T) {
	t.Setenv("MYWA_API_KEY", "short          ")
	if _, err := Load(); err == nil {
		t.Fatal("want error for a key under 16 characters once spaces are removed")
	}
}
