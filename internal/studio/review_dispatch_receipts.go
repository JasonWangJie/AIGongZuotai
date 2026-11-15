package studio

import "context"

// The original has already been opened and fully verified on the same private
// descriptor by OpenMediaModerationAsset. Validate the frozen thumbnail too;
// this path never needs a generation provider, signed URL, or re-import.
func verifyMediaReviewThumbnail(ctx context.Context, storage AssetStorage, info AssetInfo) error {
	if info.Thumbnail == nil {
		return nil
	}
	switch store := storage.(type) {
	case *LocalAssetStore:
		if info.StorageBackend == "s3" {
			return mediaModerationAssetUnavailable()
		}
		return store.verifyDeliveryFile(ctx, *info.Thumbnail, info.ID+".thumb.png", "image")
	case *S3AssetStorage:
		if info.StorageBackend == "s3" {
			if err := store.checkReceipt(info); err != nil {
				return err
			}
			return store.verifyObject(ctx, *info.Thumbnail)
		}
		return store.local.verifyDeliveryFile(ctx, *info.Thumbnail, info.ID+".thumb.png", "image")
	default:
		return mediaModerationAssetUnavailable()
	}
}
