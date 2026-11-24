package studio

import (
	"context"
	"errors"
	"io"
)

type firstFrameProvider interface {
	PrepareFirstFrameModel(context.Context, TaskInput, string, MediaEditSource) (MediaSubmission, error)
}

func taskOperation(input TaskInput) (string, error) {
	op := input.Operation
	if op == "" {
		op = "generate"
	}
	if sourceVideoOperation(op) || input.Inputs.SourceVideo != nil || input.Inputs.ExtensionDirection != "" {
		return sourceVideoInputOperation(input)
	}
	refs := len(input.Inputs.ReferenceAssetIDs)
	frame := input.Inputs.FirstFrame
	if input.Kind == "image" || input.Kind == "video" {
		switch op {
		case "generate":
			if refs == 0 && frame == nil && input.Inputs.WorkflowID == "" {
				return op, nil
			}
		case "edit":
			if input.Kind == "image" && frame == nil && input.Inputs.WorkflowID == "" {
				return op, nil
			}
		case "first_frame":
			if input.Kind == "video" && refs == 0 && input.Inputs.WorkflowID != "" && frame != nil && frame.AssetID != "" && frame.ArtifactID != "" && frame.TaskID != "" && frame.ContentVersion != "" && frame.Revision > 0 {
				return op, nil
			}
		}
	}
	return "", failure(422, "INVALID_OPERATION", "任务类型、操作或参考素材不匹配")
}

func (a *App) firstFrameInfo(project subscriptionScope, input TaskInput, model Model) (*AssetInfo, error) {
	if a.store == nil || !model.Capabilities.VideoFirstFrameSupported {
		return nil, failure(422, "VIDEO_FIRST_FRAME_UNAVAILABLE", "当前视频模型未配置首帧文件输入")
	}
	if _, ok := a.media.(firstFrameProvider); !ok {
		return nil, failure(422, "VIDEO_FIRST_FRAME_UNAVAILABLE", "当前视频渠道未配置首帧文件输入")
	}
	info, err := a.store.workflowFirstFrameInfo(project, input.Inputs.WorkflowID, *input.Inputs.FirstFrame, input)
	if err != nil {
		return nil, err
	}
	if info.ID != input.Inputs.FirstFrame.ArtifactID || !mediaContentHash.MatchString(info.SHA256) || info.ByteSize < 1 || info.ByteSize > maxMediaReferenceBytes || referenceExtension(info.ContentType) == "" {
		return nil, failure(422, "FIRST_FRAME_UNAVAILABLE", "选中图片不符合首帧原件要求")
	}
	return &info, nil
}

func (a *App) firstFrameSource(ctx context.Context, expected AssetInfo) MediaEditSource {
	return a.privateMediaSource(ctx, expected, "image")
}

func (a *App) privateMediaSource(ctx context.Context, expected AssetInfo, kind string) MediaEditSource {
	return MediaEditSource{ContentType: expected.ContentType, ByteSize: expected.ByteSize, SHA256: expected.SHA256, Open: func() (io.ReadCloser, error) {
		f, actual, err := OpenMediaModerationAsset(ctx, a.assets, expected.ID)
		if err != nil {
			return nil, err
		}
		if !sameMediaDeliveryReceipt(&actual, &expected) {
			f.Close()
			return nil, referencePreparationError()
		}
		contentType, err := validateAssetEncoding(f, kind, actual.ByteSize)
		if err != nil || contentType != expected.ContentType {
			f.Close()
			return nil, referencePreparationError()
		}
		if kind == "video" {
			if err = checkSourceMP4(f, actual.ByteSize); err != nil {
				f.Close()
				return nil, referencePreparationError()
			}
		}
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
		return f, nil
	}}
}

func (a *App) prepareFirstFrame(ctx context.Context, input TaskInput, upstream string, info AssetInfo) (*MediaSubmission, error) {
	select {
	case a.editPrepareSlots <- struct{}{}:
		defer func() { <-a.editPrepareSlots }()
	default:
		return nil, failure(429, "REFERENCE_PREPARATION_BUSY", "私有输入准备繁忙，请稍后核对")
	}
	provider, ok := a.media.(firstFrameProvider)
	if !ok {
		return nil, failure(422, "VIDEO_FIRST_FRAME_UNAVAILABLE", "当前视频渠道未配置首帧输入")
	}
	frozen, err := provider.PrepareFirstFrameModel(ctx, input, upstream, a.firstFrameSource(ctx, info))
	if err != nil {
		var pe *MediaProviderError
		if errors.As(err, &pe) {
			switch pe.Code {
			case "REFERENCE_PREPARATION_BUSY", "ASSET_STORAGE_BUSY":
				return nil, failure(429, pe.Code, "私有输入准备繁忙，请稍后核对")
			case "REFERENCE_STORAGE_FULL":
				return nil, failure(507, pe.Code, "私有素材存储容量已满")
			}
		}
		return nil, failure(422, "FIRST_FRAME_UNAVAILABLE", "首帧原件校验失败，未提交视频生成")
	}
	return &frozen, nil
}
