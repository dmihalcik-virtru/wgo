package dash

import (
	"testing"
	"time"
)

func TestLiveFixtureValidates(t *testing.T) {
	if err := liveFixture(time.Now()).Validate(); err != nil {
		t.Fatalf("fixture snapshot invalid: %v", err)
	}
}
