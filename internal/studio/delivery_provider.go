package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// These are private execution data. SourceURL belongs only in the encrypted
// delivery manifest, never Task JSON, an event, a log, or an asset receipt.
type MediaDeliveryItem struct {
	Slot        int    `json:"slot"`
	Kind        string `json:"kind"`
	SourceURL   string `json:"source_url"`
	ContentType string `json:"content_type"`
	ByteSize    int64  `json:"byte_size"`
}

type MediaQueryResult struct {
	Result MediaResult
	Items  []MediaDeliveryItem
}

type MediaDeliveryProvider interface {
	QueryStatus(context.Context, MediaHandle) (MediaQueryResult, error)
	ImportDeliveryItem(context.Context, string, MediaDeliveryItem) (Artifact, error)
	LoadDeliveryReceipt(context.Context, string, MediaDeliveryItem) (Artifact, bool, error)
}

var _ MediaDeliveryProvider = (*AsyncMediaProvider)(nil)

// This completion marker is written only after original, thumbnail and storage
// promotion are complete. The containing AssetInfo binds exact stored bytes.
type AssetDeliveryReceipt struct {
	Version    int    `json:"version"`
	DeliveryID string `json:"delivery_id"`
	Slot       int    `json:"slot"`
	ItemSHA256 string `json:"item_sha256"`
}

var mediaDeliveryID = regexp.MustCompile(`^delivery_[a-f0-9]{32}$`)

func mediaDeliveryIdentity(deliveryID string, slot int) string {
	return "delivery-v1\n" + deliveryID + "\n" + strconv.Itoa(slot)
}

func mediaDeliveryAssetID(deliveryID string, slot int) string {
	sum := sha256.Sum256([]byte(mediaDeliveryIdentity(deliveryID, slot)))
	return "asset_" + hex.EncodeToString(sum[:])
}

func mediaDeliveryItemDigest(item MediaDeliveryItem) string {
	body, _ := json.Marshal(item)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func validDeliveryMarker(marker *AssetDeliveryReceipt, id string) bool {
	if marker == nil {
		return true
	}
	_, err := hex.DecodeString(marker.ItemSHA256)
	return marker.Version == 1 && mediaDeliveryID.MatchString(marker.DeliveryID) && marker.Slot >= 0 && marker.Slot < 4 &&
		len(marker.ItemSHA256) == 64 && err == nil && mediaDeliveryAssetID(marker.DeliveryID, marker.Slot) == id
}

func deliveryIdentityError() error {
	return &MediaProviderError{Code: "DELIVERY_IDENTITY_CHANGED", Message: "private delivery identity or completed receipt changed; retain the original task", ReconcileRequired: true}
}

func deliveryReceiptError() error {
	return &MediaProviderError{Code: "DELIVERY_RECEIPT_UNAVAILABLE", Message: "private delivery bytes or completion receipt are missing or invalid; retain the original task", ReconcileRequired: true}
}

func (p *AsyncMediaProvider) validateDeliveryItem(deliveryID string, item MediaDeliveryItem) error {
	if !mediaDeliveryID.MatchString(deliveryID) || item.Slot < 0 || item.Slot >= 4 || (item.Kind != "image" && item.Kind != "video") ||
		item.Kind == "video" && item.Slot != 0 || len(item.SourceURL) == 0 || len(item.SourceURL) > 8192 ||
		len(item.ContentType) > 128 || strings.ContainsAny(item.ContentType, "\x00\r\n") || item.ByteSize < 0 || item.ByteSize > 256<<20 {
		return deliveryIdentityError()
	}
	u, err := p.assets.deliveryURL(item.SourceURL)
	if err != nil || u.String() != item.SourceURL {
		return deliveryIdentityError()
	}
	return nil
}

func mediaDeliveryArtifact(item MediaDeliveryItem, info AssetInfo) Artifact {
	return Artifact{ID: info.ID, URL: "/api/v1/assets/" + info.ID, Kind: item.Kind,
		Title: fmt.Sprintf("生成%s %d", map[string]string{"image": "图片", "video": "视频"}[item.Kind], item.Slot+1), StorageInfo: &info}
}

// LoadDeliveryReceipt returns found only after verifying the complete private
// original and thumbnail. A damaged completion marker never permits redownload.
func (p *AsyncMediaProvider) LoadDeliveryReceipt(ctx context.Context, deliveryID string, item MediaDeliveryItem) (Artifact, bool, error) {
	if err := p.validateDeliveryItem(deliveryID, item); err != nil {
		return Artifact{}, false, err
	}
	id := mediaDeliveryAssetID(deliveryID, item.Slot)
	info, exists, err := p.assets.deliveryManifest(id)
	if err != nil || !exists || info.Delivery == nil {
		return Artifact{}, false, err
	}
	marker := info.Delivery
	if marker.DeliveryID != deliveryID || marker.Slot != item.Slot || marker.ItemSHA256 != mediaDeliveryItemDigest(item) {
		return Artifact{}, false, deliveryIdentityError()
	}
	if err = p.assets.verifyDeliveryInfo(ctx, info, item.Kind, true); err != nil {
		return Artifact{}, false, err
	}
	return mediaDeliveryArtifact(item, info), true, nil
}

// The caller must first commit the encrypted manifest with this delivery ID and
// item. No gateway query or POST occurs here, including on storage recovery.
func (p *AsyncMediaProvider) ImportDeliveryItemObserved(ctx context.Context, deliveryID string, item MediaDeliveryItem) (Artifact, *MediaScheduleObservation, error) {
	collector := &mediaSourceObservationCollector{}
	artifact, err := p.ImportDeliveryItem(context.WithValue(ctx, mediaSourceObservationKey{}, collector), deliveryID, item)
	if err != nil {
		var providerError *MediaProviderError
		if errors.As(err, &providerError) && providerError.Observation != nil {
			observation := *providerError.Observation
			return artifact, &observation, err
		}
		return artifact, nil, err
	}
	return artifact, collector.observation, nil
}

func (p *AsyncMediaProvider) ImportDeliveryItem(ctx context.Context, deliveryID string, item MediaDeliveryItem) (Artifact, error) {
	if err := p.validateDeliveryItem(deliveryID, item); err != nil {
		return Artifact{}, err
	}
	id := mediaDeliveryAssetID(deliveryID, item.Slot)
	stripe, _ := strconv.ParseUint(id[len("asset_"):len("asset_")+2], 16, 8)
	lock := &p.assets.deliveryLocks[stripe%uint64(len(p.assets.deliveryLocks))]
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	if artifact, found, err := p.LoadDeliveryReceipt(ctx, deliveryID, item); err != nil || found {
		return artifact, err
	}
	info, exists, err := p.assets.deliveryManifest(id)
	if err != nil {
		return Artifact{}, err
	}
	if exists {
		// An incomplete import may continue thumbnail/promotion from its saved
		// bytes, but must never overwrite changed or missing original content.
		if err = p.assets.verifyDeliveryInfo(ctx, info, item.Kind, false); err != nil {
			return Artifact{}, err
		}
	} else {
		for _, suffix := range []string{".bin", ".thumb.png"} {
			if _, statErr := p.assets.root.Stat(id + suffix); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
				return Artifact{}, deliveryReceiptError()
			}
		}
	}
	info, err = p.assets.Import(ctx, mediaDeliveryIdentity(deliveryID, item.Slot), item.SourceURL, item.Kind)
	if err != nil {
		return Artifact{}, err
	}
	if err = p.assets.verifyDeliveryInfo(ctx, info, item.Kind, true); err != nil {
		return Artifact{}, err
	}
	info.Delivery = &AssetDeliveryReceipt{Version: 1, DeliveryID: deliveryID, Slot: item.Slot, ItemSHA256: mediaDeliveryItemDigest(item)}
	p.assets.publishMu.Lock()
	err = writeAssetManifest(p.assets.root, info)
	p.assets.publishMu.Unlock()
	if err != nil {
		return Artifact{}, err
	}
	return mediaDeliveryArtifact(item, info), nil
}

func (store *LocalAssetStore) deliveryManifest(id string) (AssetInfo, bool, error) {
	if _, err := store.root.Stat(id + ".json"); errors.Is(err, os.ErrNotExist) {
		return AssetInfo{}, false, nil
	} else if err != nil {
		return AssetInfo{}, false, deliveryReceiptError()
	}
	info, err := readAssetManifest(store.root, id)
	if err != nil || !validDeliveryMarker(info.Delivery, id) {
		return AssetInfo{}, false, deliveryReceiptError()
	}
	return info, true, nil
}

func (store *LocalAssetStore) verifyDeliveryInfo(ctx context.Context, info AssetInfo, kind string, complete bool) error {
	if !validAssetInfo(info, info.ID) || kind == "image" && (!strings.HasPrefix(info.ContentType, "image/") || info.ByteSize > 32<<20) || kind == "video" && info.ContentType != "video/mp4" ||
		complete && kind == "image" && info.Thumbnail == nil {
		return deliveryReceiptError()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if info.StorageBackend == "s3" {
		if store.destination == nil {
			return deliveryIdentityError()
		}
		if err := store.destination.checkReceipt(info); err != nil {
			return err
		}
		if err := store.destination.verifyObject(ctx, info); err != nil {
			return err
		}
		if info.Thumbnail != nil {
			return store.destination.verifyObject(ctx, *info.Thumbnail)
		}
		return nil
	}
	if err := store.verifyDeliveryFile(ctx, info, info.ID+".bin", kind); err != nil {
		return err
	}
	if info.Thumbnail != nil {
		return store.verifyDeliveryFile(ctx, *info.Thumbnail, info.ID+".thumb.png", "image")
	}
	return nil
}

func (store *LocalAssetStore) verifyDeliveryFile(ctx context.Context, info AssetInfo, name, kind string) error {
	file, err := store.root.Open(name)
	if err != nil {
		return deliveryReceiptError()
	}
	defer file.Close()
	// This helper hashes and validates real encoding on the same descriptor,
	// including context checks and exact content-type comparison.
	return verifyMediaModerationFile(ctx, file, info)
}
