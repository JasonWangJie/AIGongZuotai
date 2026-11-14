package studio

import (
	"strings"
	"testing"
)

func TestObjectCredentialsCannotBecomeMetricsToken(t *testing.T) {
	secret := strings.Repeat("private", 8)
	for _, storage := range []AssetStorageConfig{{SecretAccessKey: secret}, {SessionToken: secret}} {
		if err := validateObservabilityConfig(Config{MetricsToken: secret, AssetStorage: storage}); err == nil {
			t.Fatal("object storage credential reused as public observability identity")
		} else if strings.Contains(err.Error(), secret) {
			t.Fatal("credential leaked in validation error")
		}
	}
}
