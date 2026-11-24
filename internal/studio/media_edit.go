package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	imageEditEndpoint            = "/v1/images/edits_async"
	maxMediaEditBodyBytes  int64 = 33 << 20
	maxMediaReferenceBytes int64 = 8 << 20
)

// MediaBodyStore publishes an immutable file only after write succeeds. The
// implementation owns path confinement, atomic publication and disk limits;
// the provider revalidates the saved content before any upstream submission.
type MediaBodyStore interface {
	WriteBody(context.Context, string, func(io.Writer) error) (MediaBodyInfo, error)
	OpenBody(string) (*os.File, error)
}

type MediaBodyInfo struct {
	ID          string
	ContentType string
	SHA256      string
	ByteSize    int64
}

// MediaEditSource is created only after project ownership and upload validation.
// Open must return the same immutable private file represented by its metadata.
// Neither browser URLs nor original client filenames enter this interface.
type MediaEditSource struct {
	ContentType string
	ByteSize    int64
	SHA256      string
	Open        func() (io.ReadCloser, error)
}

var mediaContentHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func submissionIdentityError() *MediaProviderError {
	return &MediaProviderError{Code: "SUBMISSION_IDENTITY_CHANGED", Message: "saved submission identity or bytes do not match this provider", ReconcileRequired: true, Observation: &MediaScheduleObservation{Scope: "gateway", Class: "identity"}}
}

func referencePreparationError() *MediaProviderError {
	return &MediaProviderError{Code: "MEDIA_REFERENCE_UNAVAILABLE", Message: "reference image could not be verified; no generation was submitted"}
}

func (p *AsyncMediaProvider) PrepareEditModel(ctx context.Context, input TaskInput, upstreamModel string, refs []MediaEditSource) (MediaSubmission, error) {
	if err := ctx.Err(); err != nil {
		return MediaSubmission{}, err
	}
	if input.Kind != "image" || len(refs) < 1 || len(refs) > 4 || p.bodies == nil {
		return MediaSubmission{}, &MediaProviderError{Code: "MEDIA_CAPABILITY_UNAVAILABLE", Message: "image editing requires one to four private references and a request store"}
	}
	// This validates the frozen prompt/model/count/preset using the same limits
	// as generation. Only its small JSON allocation is discarded; no image data
	// is encoded in JSON or stored in the database.
	submission, err := p.PrepareModel(input, upstreamModel)
	if err != nil {
		return MediaSubmission{}, err
	}
	return p.prepareMultipart(ctx, input, upstreamModel, submission, refs, false)
}

// PrepareFirstFrameModel freezes one registered image in the gateway's
// documented input_reference file part. It makes no network submission.
func (p *AsyncMediaProvider) PrepareFirstFrameModel(ctx context.Context, input TaskInput, upstreamModel string, firstFrame MediaEditSource) (MediaSubmission, error) {
	if err := ctx.Err(); err != nil {
		return MediaSubmission{}, err
	}
	if input.Kind != "video" || input.Operation != "first_frame" || input.Inputs.FirstFrame == nil || input.Inputs.WorkflowID == "" || len(input.Inputs.ReferenceAssetIDs) != 0 || input.Parameters.Seconds < 1 || p.bodies == nil {
		return MediaSubmission{}, &MediaProviderError{Code: "MEDIA_CAPABILITY_UNAVAILABLE", Message: "first-frame video requires a selected image, a frozen duration and private request storage"}
	}
	submission, err := p.prepareMediaModel(input, upstreamModel)
	if err != nil {
		return MediaSubmission{}, err
	}
	submission.InputType = "first_frame"
	return p.prepareMultipart(ctx, input, upstreamModel, submission, []MediaEditSource{firstFrame}, true)
}

func (p *AsyncMediaProvider) prepareMultipart(ctx context.Context, input TaskInput, upstreamModel string, submission MediaSubmission, refs []MediaEditSource, video bool) (MediaSubmission, error) {
	for _, ref := range refs {
		if ref.Open == nil || ref.ByteSize < 1 || ref.ByteSize > maxMediaReferenceBytes || !mediaContentHash.MatchString(ref.SHA256) || referenceExtension(ref.ContentType) == "" {
			return MediaSubmission{}, referencePreparationError()
		}
	}
	boundary := newID("mediaedit")
	contentType := "multipart/form-data; boundary=" + boundary
	stored, err := p.bodies.WriteBody(ctx, contentType, func(destination io.Writer) error {
		writer := multipart.NewWriter(destination)
		if err := writer.SetBoundary(boundary); err != nil {
			return err
		}
		fields := [][2]string{{"model", upstreamModel}, {"prompt", strings.TrimSpace(input.Inputs.Prompt)}}
		if video {
			fields = append(fields, [2]string{"seconds", strconv.Itoa(input.Parameters.Seconds)})
		} else {
			fields = append(fields, [2]string{"n", strconv.Itoa(input.Parameters.Count)})
		}
		for _, field := range [][2]string{{"aspect_ratio", input.Parameters.AspectRatio}, {"size", input.Parameters.Size}, {"resolution", input.Parameters.Resolution}} {
			if field[1] != "" {
				fields = append(fields, field)
			}
		}
		for _, field := range fields {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := writer.WriteField(field[0], field[1]); err != nil {
				return err
			}
		}
		for index, ref := range refs {
			if err := ctx.Err(); err != nil {
				return err
			}
			field, filename := "image[]", fmt.Sprintf("reference-%d", index+1)
			if video {
				field, filename = "input_reference", "first-frame"
			}
			if err := writeMediaReference(ctx, writer, field, filename, ref); err != nil {
				return err
			}
		}
		return writer.Close()
	})
	if err != nil {
		return MediaSubmission{}, bodyPreparationError(ctx, err, referencePreparationError())
	}
	if stored.ID == "" || len(stored.ID) > 128 || strings.ContainsAny(stored.ID, "/\\\x00\r\n") || stored.ContentType != contentType || stored.ByteSize < 1 || stored.ByteSize > maxMediaEditBodyBytes || !mediaContentHash.MatchString(stored.SHA256) {
		return MediaSubmission{}, referencePreparationError()
	}
	submission.Body = nil
	submission.BodyFileID, submission.BodySize, submission.ContentType = stored.ID, stored.ByteSize, stored.ContentType
	submission.BodySHA256 = stored.SHA256
	if !video {
		submission.Endpoint = imageEditEndpoint
	}
	return submission, nil
}

func referenceExtension(contentType string) string {
	switch contentType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

func bodyPreparationError(ctx context.Context, err, fallback error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrReferenceBusy) {
		return &MediaProviderError{Code: "REFERENCE_PREPARATION_BUSY", Message: "reference preparation capacity is temporarily exhausted"}
	}
	if errors.Is(err, ErrReferenceStorageFull) {
		return &MediaProviderError{Code: "REFERENCE_STORAGE_FULL", Message: "private reference storage capacity is exhausted"}
	}
	// Filesystem errors can contain private paths.
	return fallback
}

func writeMediaReference(ctx context.Context, writer *multipart.Writer, field, filename string, ref MediaEditSource) error {
	file, err := ref.Open()
	var providerErr *MediaProviderError
	if errors.As(err, &providerErr) && providerErr.Code == "ASSET_STORAGE_BUSY" {
		return ErrReferenceBusy
	}
	if err != nil || file == nil {
		return referencePreparationError()
	}
	defer file.Close()
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s%s"`, field, filename, referenceExtension(ref.ContentType)))
	header.Set("Content-Type", ref.ContentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	hash := sha256.New()
	count, err := io.CopyBuffer(io.MultiWriter(part, hash), io.LimitReader(mediaContextReader{ctx, file}, ref.ByteSize+1), make([]byte, 32<<10))
	if err != nil {
		return err
	}
	if count != ref.ByteSize || hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 {
		return referencePreparationError()
	}
	return nil
}

type mediaContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r mediaContextReader) Read(destination []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(destination)
}

func (p *AsyncMediaProvider) openSubmissionBody(ctx context.Context, submission MediaSubmission) (io.ReadCloser, string, int64, error) {
	if submission.BodyFileID == "" {
		if submission.InputType != "" || len(submission.Body) == 0 || len(submission.Body) > 128<<10 || submission.BodySize != 0 || submission.ContentType != "" && submission.ContentType != "application/json" || submission.Endpoint == imageEditEndpoint {
			return nil, "", 0, submissionIdentityError()
		}
		sum := sha256.Sum256(submission.Body)
		if submission.BodySHA256 != hex.EncodeToString(sum[:]) {
			return nil, "", 0, submissionIdentityError()
		}
		return io.NopCloser(bytes.NewReader(submission.Body)), "application/json", int64(len(submission.Body)), nil
	}
	prefix, limit := fileSubmissionShape(submission)
	if limit == 0 || p.bodies == nil || len(submission.Body) != 0 || submission.BodySize < 1 || submission.BodySize > limit || !mediaContentHash.MatchString(submission.BodySHA256) {
		return nil, "", 0, submissionIdentityError()
	}
	file, err := p.bodies.OpenBody(submission.BodyFileID)
	if err != nil || file == nil {
		return nil, "", 0, submissionIdentityError()
	}
	valid := false
	defer func() {
		if !valid {
			file.Close()
		}
	}()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != submission.BodySize {
		return nil, "", 0, submissionIdentityError()
	}
	// Verify the frozen format, then hash and send from this same descriptor.
	actualPrefix := make([]byte, len(prefix))
	if _, err = io.ReadFull(file, actualPrefix); err != nil || !bytes.Equal(actualPrefix, prefix) {
		return nil, "", 0, submissionIdentityError()
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, "", 0, submissionIdentityError()
	}
	hash := sha256.New()
	count, err := io.CopyBuffer(hash, io.LimitReader(mediaContextReader{ctx, file}, submission.BodySize+1), make([]byte, 32<<10))
	if err != nil || count != submission.BodySize || hex.EncodeToString(hash.Sum(nil)) != submission.BodySHA256 {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, "", 0, err
		}
		return nil, "", 0, submissionIdentityError()
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, "", 0, submissionIdentityError()
	}
	valid = true
	return file, submission.ContentType, submission.BodySize, nil
}

func fileSubmissionShape(submission MediaSubmission) ([]byte, int64) {
	if endpoint := sourceVideoEndpoint(submission.InputType); endpoint != "" {
		if submission.Kind == "video" && submission.Endpoint == endpoint && submission.ContentType == "application/json" {
			return []byte(`{"model":`), maxSourceVideoBodyBytes
		}
		return nil, 0
	}
	edit := submission.Kind == "image" && submission.Endpoint == imageEditEndpoint && submission.InputType == ""
	firstFrame := submission.Kind == "video" && submission.Endpoint == videoGenerationEndpoint && submission.InputType == "first_frame"
	mediaType, params, err := mime.ParseMediaType(submission.ContentType)
	boundary := params["boundary"]
	if (!edit && !firstFrame) || err != nil || mediaType != "multipart/form-data" || len(params) != 1 || len(boundary) < 1 || len(boundary) > 70 || strings.ContainsAny(submission.ContentType, "\r\n") {
		return nil, 0
	}
	return []byte("--" + boundary + "\r\n"), maxMediaEditBodyBytes
}
