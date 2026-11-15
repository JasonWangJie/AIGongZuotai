package studio

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"unicode/utf8"
)

const maxMediaDeliveryPlaintext = 64 << 10

type MediaDeliveryConfig struct{ EncryptionKey string }

func validateMediaDeliveryKeyIndependence(c Config) error {
	encoded := c.MediaDelivery.EncryptionKey
	if encoded == "" {
		return nil
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != encoded {
		return errors.New("invalid independent media delivery encryption key")
	}
	for _, secret := range []string{c.Mail.OutboxKey, c.APIKey, c.AsyncImageAPIKey, c.Mail.Password} {
		if secret == "" {
			continue
		}
		decoded, _ := base64.StdEncoding.Strict().DecodeString(secret)
		if encoded == secret || string(key) == secret || len(decoded) == 32 && bytes.Equal(decoded, key) {
			return errors.New("media delivery encryption key must be independent of other credentials")
		}
	}
	return nil
}

type MediaDeliveryPlannedItem struct {
	Position int
	AssetID  string
	Receipt  *AssetInfo
}

// Never serialized into Task, events, audit or a client response.
type MediaDeliveryRecord struct {
	TaskID, ID, WorkspaceID, ProjectID  string
	State                               string
	Version                             uint64
	KeyID, ManifestDigest, HandleDigest string
	Ciphertext                          []byte
	ItemCount                           int
	ResultStatus                        string
	Items                               []MediaDeliveryPlannedItem
}

type mediaDeliveryCipher struct {
	aead              cipher.AEAD
	keyID, databaseID string
}

func newMediaDeliveryCipher(encoded string, required, ephemeral bool) (*mediaDeliveryCipher, error) {
	if encoded == "" && !required {
		return nil, nil
	}
	var key []byte
	var err error
	if encoded == "" && ephemeral {
		key = make([]byte, 32)
		_, err = rand.Read(key)
	} else {
		key, err = base64.StdEncoding.Strict().DecodeString(encoded)
	}
	if err != nil || len(key) != 32 || encoded != "" && base64.StdEncoding.EncodeToString(key) != encoded {
		return nil, errors.New("media delivery requires an independent 32-byte standard base64 encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("media delivery encryption unavailable")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("media delivery encryption unavailable")
	}
	h := sha256.Sum256(key)
	return &mediaDeliveryCipher{aead: aead, keyID: hex.EncodeToString(h[:16]), databaseID: newID("memory_delivery")}, nil
}

func mediaDeliveryDigest(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func mediaDeliveryAAD(databaseID string, r *MediaDeliveryRecord) []byte {
	b, _ := json.Marshal(struct {
		Database, Task, Delivery, Workspace, Project, Key, Manifest, Handle, Result string
		Count, Format                                                               int
	}{databaseID, r.TaskID, r.ID, r.WorkspaceID, r.ProjectID, r.KeyID, r.ManifestDigest, r.HandleDigest, r.ResultStatus, r.ItemCount, 1})
	return append([]byte("studio-media-delivery-v1\x00"), b...)
}

func validMediaDeliverySources(handle MediaHandle, result MediaQueryResult) bool {
	if handle.ID == "" || handle.Count < 1 || handle.Count > 4 || len(result.Result.Artifacts) != 0 || len(result.Items) < 1 || len(result.Items) > handle.Count || handle.Kind != "image" && handle.Kind != "video" {
		return false
	}
	if handle.Kind == "video" && len(result.Items) != 1 {
		return false
	}
	want := "SUCCEEDED"
	if len(result.Items) < handle.Count {
		want = "PARTIAL"
	}
	if result.Result.Status != want {
		return false
	}
	for i, item := range result.Items {
		if item.Slot != i || item.Kind != handle.Kind || len(item.SourceURL) == 0 || len(item.SourceURL) > 8192 || !utf8.ValidString(item.SourceURL) || strings.ContainsAny(item.SourceURL, "\x00\r\n") || item.ByteSize < 0 || item.ByteSize > 256<<20 || len(item.ContentType) > 128 || !utf8.ValidString(item.ContentType) || strings.ContainsAny(item.ContentType, "\x00\r\n") {
			return false
		}
		u, err := url.Parse(item.SourceURL)
		if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" {
			return false
		}
	}
	return true
}

func (c *mediaDeliveryCipher) seal(t *taskRecord, result MediaQueryResult) (*MediaDeliveryRecord, error) {
	if c == nil || !validMediaDeliverySources(t.Handle, result) {
		return nil, errors.New("invalid private media delivery result")
	}
	plain, err := json.Marshal(result.Items)
	if err != nil || len(plain) > maxMediaDeliveryPlaintext {
		return nil, errors.New("invalid private media delivery result")
	}
	r := &MediaDeliveryRecord{TaskID: t.Task.ID, ID: newID("delivery"), WorkspaceID: t.Task.WorkspaceID, ProjectID: t.Task.ProjectID, State: "READY", KeyID: c.keyID, ManifestDigest: mediaDeliveryDigest(result.Items), HandleDigest: mediaDeliveryDigest(t.Handle), ItemCount: len(result.Items), ResultStatus: result.Result.Status}
	for i := range result.Items {
		r.Items = append(r.Items, MediaDeliveryPlannedItem{Position: i, AssetID: mediaDeliveryAssetID(r.ID, i)})
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, errors.New("media delivery encryption unavailable")
	}
	r.Ciphertext = c.aead.Seal(nonce, nonce, plain, mediaDeliveryAAD(c.databaseID, r))
	return r, nil
}

func (c *mediaDeliveryCipher) open(t *taskRecord) ([]MediaDeliveryItem, error) {
	r := t.Delivery
	fail := func() ([]MediaDeliveryItem, error) {
		return nil, errors.New("private media delivery cannot be verified")
	}
	if c == nil || r == nil || r.KeyID != c.keyID || r.HandleDigest != mediaDeliveryDigest(t.Handle) || r.TaskID != t.Task.ID || r.WorkspaceID != t.Task.WorkspaceID || r.ProjectID != t.Task.ProjectID || len(r.Ciphertext) > maxMediaDeliveryPlaintext+64 || len(r.Ciphertext) < c.aead.NonceSize()+c.aead.Overhead() {
		return fail()
	}
	n := c.aead.NonceSize()
	plain, err := c.aead.Open(nil, r.Ciphertext[:n], r.Ciphertext[n:], mediaDeliveryAAD(c.databaseID, r))
	if err != nil {
		return fail()
	}
	var items []MediaDeliveryItem
	if json.Unmarshal(plain, &items) != nil || mediaDeliveryDigest(items) != r.ManifestDigest || len(items) != r.ItemCount || !validMediaDeliverySources(t.Handle, MediaQueryResult{Result: MediaResult{Status: r.ResultStatus}, Items: items}) {
		return fail()
	}
	return items, nil
}

func cloneMediaDelivery(r *MediaDeliveryRecord) *MediaDeliveryRecord {
	if r == nil {
		return nil
	}
	out := *r
	out.Ciphertext = append([]byte(nil), r.Ciphertext...)
	out.Items = append([]MediaDeliveryPlannedItem(nil), r.Items...)
	for i := range out.Items {
		if r.Items[i].Receipt != nil {
			b, _ := json.Marshal(r.Items[i].Receipt)
			out.Items[i].Receipt = &AssetInfo{}
			_ = json.Unmarshal(b, out.Items[i].Receipt)
		}
	}
	return &out
}

func sameMediaDeliveryReceipt(a, b *AssetInfo) bool { return reflect.DeepEqual(a, b) }
