package studio

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

const (
	videoEditEndpoint             = "/v1/videos/edits_async"
	videoExtendEndpoint           = "/v1/videos/extensions_async"
	maxSourceVideoBodyBytes int64 = 64 << 20
)

type sourceVideoProvider interface {
	PrepareSourceVideoModel(context.Context, TaskInput, string, string, MediaEditSource) (MediaSubmission, error)
}

var videoDirectionName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func sourceVideoEndpoint(operation string) string {
	switch operation {
	case "video_edit":
		return videoEditEndpoint
	case "video_extend":
		return videoExtendEndpoint
	}
	return ""
}

// The caller resolves the permitted operation/preset and the authorized,
// immutable MP4. Model input bytes and storage paths never originate in the browser.
func (p *AsyncMediaProvider) PrepareSourceVideoModel(ctx context.Context, input TaskInput, upstreamModel, direction string, source MediaEditSource) (MediaSubmission, error) {
	if err := ctx.Err(); err != nil {
		return MediaSubmission{}, err
	}
	endpoint := sourceVideoEndpoint(input.Operation)
	if p.bodies == nil || input.Kind != "video" || endpoint == "" || input.Inputs.FirstFrame != nil || len(input.Inputs.ReferenceAssetIDs) != 0 || input.Inputs.WorkflowID != "" ||
		(direction != "" && (input.Operation != "video_extend" || !videoDirectionName.MatchString(direction))) {
		return MediaSubmission{}, &MediaProviderError{Code: "MEDIA_CAPABILITY_UNAVAILABLE", Message: "source video requires a frozen video operation and private request storage"}
	}
	if source.ContentType != "video/mp4" || source.Open == nil || source.ByteSize < 16 || source.ByteSize > maxSourceVideoBodyBytes || !mediaContentHash.MatchString(source.SHA256) {
		return MediaSubmission{}, sourceVideoInputError()
	}
	submission, err := p.prepareMediaModel(input, upstreamModel)
	if err != nil {
		return MediaSubmission{}, err
	}
	// prepareMediaModel marshals only the resolved scalar fields. Extend that
	// object in a fixed order, then stream the one remaining JSON string to disk.
	prefix := submission.Body[:len(submission.Body)-1]
	if direction != "" {
		value, err := json.Marshal(direction)
		if err != nil {
			return MediaSubmission{}, err
		}
		prefix = append(prefix, `,"extension_direction":`...)
		prefix = append(prefix, value...)
	}
	prefix = append(prefix, `,"video":"data:video/mp4;base64,`...)
	const suffix = `"}`
	size := int64(len(prefix) + base64.StdEncoding.EncodedLen(int(source.ByteSize)) + len(suffix))
	if size > maxSourceVideoBodyBytes {
		return MediaSubmission{}, &MediaProviderError{Code: "SOURCE_VIDEO_BODY_TOO_LARGE", Message: "encoded video request exceeds the 64 MiB limit"}
	}
	stored, err := p.bodies.WriteBody(ctx, "application/json", func(w io.Writer) error {
		if _, err := w.Write(prefix); err != nil {
			return err
		}
		if err := encodeSourceVideo(ctx, w, source); err != nil {
			return err
		}
		_, err := io.WriteString(w, suffix)
		return err
	})
	if err != nil {
		return MediaSubmission{}, bodyPreparationError(ctx, err, sourceVideoInputError())
	}
	if stored.ID == "" || len(stored.ID) > 128 || strings.ContainsAny(stored.ID, "/\\\x00\r\n") || stored.ContentType != "application/json" || stored.ByteSize != size || !mediaContentHash.MatchString(stored.SHA256) {
		return MediaSubmission{}, sourceVideoInputError()
	}
	submission.InputType, submission.Endpoint, submission.Body = input.Operation, endpoint, nil
	submission.BodyFileID, submission.BodySize, submission.ContentType, submission.BodySHA256 = stored.ID, size, stored.ContentType, stored.SHA256
	return submission, nil
}

func sourceVideoInputError() *MediaProviderError {
	return &MediaProviderError{Code: "SOURCE_VIDEO_UNAVAILABLE", Message: "private source video could not be verified"}
}

func encodeSourceVideo(ctx context.Context, destination io.Writer, source MediaEditSource) error {
	file, err := source.Open()
	var providerErr *MediaProviderError
	if errors.As(err, &providerErr) && providerErr.Code == "ASSET_STORAGE_BUSY" {
		return ErrReferenceBusy
	}
	if err != nil || file == nil {
		return sourceVideoInputError()
	}
	defer file.Close()
	reader := bufio.NewReader(io.LimitReader(mediaContextReader{ctx, file}, source.ByteSize+1))
	header, err := reader.Peek(12)
	if err != nil {
		return sourceVideoInputError()
	}
	if _, err := validateAssetEncoding(bytes.NewReader(header), "video", source.ByteSize); err != nil {
		return sourceVideoInputError()
	}
	hash := sha256.New()
	encoder := base64.NewEncoder(base64.StdEncoding, destination)
	n, err := io.Copy(io.MultiWriter(encoder, hash), reader)
	if err != nil {
		return err
	}
	if n != source.ByteSize || hex.EncodeToString(hash.Sum(nil)) != source.SHA256 {
		return sourceVideoInputError()
	}
	return encoder.Close()
}
