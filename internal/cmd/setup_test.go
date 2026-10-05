package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/urlhandler"
)

func TestSetupURLHandlerUnavailableOffMacOS(t *testing.T) {
	old := setupGOOS
	setupGOOS = "linux"
	t.Cleanup(func() { setupGOOS = old })
	var out bytes.Buffer
	setupURLHandlerCmd.SetOut(&out)
	err := setupURLHandlerCmd.RunE(setupURLHandlerCmd, nil)
	if !errors.Is(err, urlhandler.ErrUnsupported) || !strings.Contains(err.Error(), "URL-handler setup is unavailable on linux") {
		t.Fatalf("err = %v", err)
	}
}
