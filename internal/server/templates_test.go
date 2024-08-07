package server

import (
	"testing"
)

func TestLoadTemplates(t *testing.T) {
	_, err := loadTemplates()
	if err != nil {
		t.Fatal(err)
	}
}
