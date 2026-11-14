package main

import "testing"

func TestAssetStorageEnvironment(t *testing.T) {
	values := map[string]string{"ASSET_STORAGE_BACKEND": "s3", "S3_ENDPOINT": "https://objects.example", "S3_REGION": "region-a", "S3_BUCKET": "private-bucket", "S3_PREFIX": "studio/v1", "S3_ACCESS_KEY_ID": "test-id", "S3_SECRET_ACCESS_KEY": "test-secret", "S3_SESSION_TOKEN": "test-session", "S3_FORCE_PATH_STYLE": "true"}
	lookup := func(name string) (string, bool) { value, exists := values[name]; return value, exists }
	config, err := assetStorageFromEnv(lookup)
	if err != nil || !config.ForcePathStyle || config.Backend != "s3" || config.Endpoint != values["S3_ENDPOINT"] || config.SecretAccessKey != values["S3_SECRET_ACCESS_KEY"] || config.SessionToken != values["S3_SESSION_TOKEN"] {
		t.Fatal("object storage configuration mapping failed")
	}
	for _, invalid := range []string{"", "1", "yes", "TRUE", "private-value-that-must-not-be-logged"} {
		values["S3_FORCE_PATH_STYLE"] = invalid
		if _, err = assetStorageFromEnv(lookup); err == nil || err.Error() != "S3_FORCE_PATH_STYLE must be true or false when set" {
			t.Fatal("ambiguous storage boolean accepted or value leaked")
		}
	}
	delete(values, "S3_FORCE_PATH_STYLE")
	config, err = assetStorageFromEnv(lookup)
	if err != nil || config.ForcePathStyle {
		t.Fatal("default path style changed")
	}
}
