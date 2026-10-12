package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/urlhandler"
)

func TestSetupURLHandlerUnavailableOffMacOS(t *testing.T) {
	oldGOOS, oldUninstall, oldDryRun := setupGOOS, setupUninstall, setupDryRun
	setupGOOS, setupUninstall, setupDryRun = "linux", false, false
	var out bytes.Buffer
	setupURLHandlerCmd.SetOut(&out)
	t.Cleanup(func() {
		setupGOOS, setupUninstall, setupDryRun = oldGOOS, oldUninstall, oldDryRun
		setupURLHandlerCmd.SetOut(nil)
	})
	err := setupURLHandlerCmd.RunE(setupURLHandlerCmd, nil)
	if !errors.Is(err, urlhandler.ErrUnsupported) || !strings.Contains(err.Error(), "URL-handler setup is unavailable on linux") {
		t.Fatalf("err = %v", err)
	}
}
