package main

import "testing"

func TestVersionString(t *testing.T) {
	if Version == "" {
		t.Fatal("Version is empty")
	}
}
