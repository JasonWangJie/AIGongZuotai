package studio

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Facts only: identities come from frozen execution records, never these fields.
type MediaScheduleObservation struct {
	Scope, Class   string
	HasServerDelay bool
	RetryNotBefore time.Time
	DelayExceeded  bool
}

// Per-import evidence, carried only by the caller's context. A saved receipt or
// an incomplete import resumed from local bytes never records a source success.
type mediaSourceObservationKey struct{}
type mediaSourceObservationCollector struct{ observation *MediaScheduleObservation }

func recordMediaSourceSuccess(ctx context.Context, response *http.Response, receivedAt time.Time) {
	collector, _ := ctx.Value(mediaSourceObservationKey{}).(*mediaSourceObservationCollector)
	if collector != nil {
		collector.observation = mediaResponseObservation("source", response, receivedAt)
	}
}

const maxMediaServerDelay = 24 * time.Hour

func mediaResponseObservation(scope string, response *http.Response, now time.Time) *MediaScheduleObservation {
	result := mediaHTTPObservation(scope, response.StatusCode, "", now)
	for _, value := range response.Header.Values("Retry-After") {
		observed := mediaHTTPObservation(scope, response.StatusCode, value, now)
		if !observed.HasServerDelay {
			continue
		}
		result.HasServerDelay = true
		result.DelayExceeded = result.DelayExceeded || observed.DelayExceeded
		if observed.RetryNotBefore.After(result.RetryNotBefore) {
			result.RetryNotBefore = observed.RetryNotBefore
		}
	}
	return result
}

type mediaScheduleStorageWriteError struct{ err error }

func (e *mediaScheduleStorageWriteError) Error() string { return "private storage write failed" }
func (e *mediaScheduleStorageWriteError) Unwrap() error { return e.err }

type mediaScheduleStorageWriter struct{ io.Writer }

func (w mediaScheduleStorageWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if err != nil {
		return n, &mediaScheduleStorageWriteError{err}
	}
	return n, nil
}

func mediaHTTPObservation(scope string, status int, retry string, now time.Time) *MediaScheduleObservation {
	class := "protocol"
	switch {
	case status >= 200 && status < 300:
		class = "success"
	case status == 429:
		class = "rate_limit"
	case status == 408 || status >= 500:
		class = "temporary"
	case status == 401 || status == 403:
		class = "identity"
	}
	o := &MediaScheduleObservation{Scope: scope, Class: class}
	value := strings.TrimSpace(retry)
	if value == "" {
		return o
	}
	digits := true
	for _, r := range value {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseUint(value, 10, 64)
		o.HasServerDelay = true
		if err != nil || seconds > uint64(maxMediaServerDelay/time.Second) {
			o.DelayExceeded = true
			// Preserve a representable deadline, but never turn overflow into a
			// shortened supported retry. Core must park DelayExceeded observations.
			if err == nil && seconds <= uint64(math.MaxInt64/int64(time.Second)) {
				o.RetryNotBefore = now.Add(time.Duration(seconds) * time.Second)
			}
			return o
		}
		o.RetryNotBefore = now.Add(time.Duration(seconds) * time.Second)
		return o
	}
	if date, err := http.ParseTime(value); err == nil {
		o.HasServerDelay = true
		o.RetryNotBefore = date
		o.DelayExceeded = date.Sub(now) > maxMediaServerDelay
	}
	return o
}

func mediaNetworkObservation(scope string, ctx context.Context) *MediaScheduleObservation {
	class := "temporary"
	if errors.Is(ctx.Err(), context.Canceled) {
		class = "cancelled"
	}
	return &MediaScheduleObservation{Scope: scope, Class: class}
}

// The compatibility duration is not proof of a server-requested delay. The
// separate observation distinguishes missing/invalid headers from explicit 0.
func retryAfterDuration(value string, now time.Time) time.Duration {
	o := mediaHTTPObservation("gateway", 200, value, now)
	if o.DelayExceeded {
		if !o.RetryNotBefore.IsZero() {
			return max(3*time.Second, o.RetryNotBefore.Sub(now))
		}
		return time.Duration(math.MaxInt64)
	}
	if o.HasServerDelay {
		return max(3*time.Second, o.RetryNotBefore.Sub(now))
	}
	return 3 * time.Second
}

func mediaErrorObservation(err error, scope, class string) error {
	var provider *MediaProviderError
	if !errors.As(err, &provider) {
		return err
	}
	copy := *provider
	if copy.Observation == nil {
		copy.Observation = &MediaScheduleObservation{Scope: scope, Class: class}
	}
	return &copy
}

func attachMediaObservation(err error, observation *MediaScheduleObservation) error {
	var provider *MediaProviderError
	if !errors.As(err, &provider) {
		return err
	}
	copy := *provider
	if copy.Observation != nil {
		return &copy
	}
	if observation == nil {
		observation = &MediaScheduleObservation{Scope: "gateway", Class: "protocol"}
	}
	value := *observation
	if strings.Contains(copy.Code, "IDENTITY") {
		value.Class = "identity"
	}
	if value.Class == "success" {
		value.Class = "protocol"
	}
	if copy.Code == "ASSET_SOURCE_NOT_ALLOWED" {
		value.Scope = "source"
		value.Class = "protocol"
	}
	copy.Observation = &value
	return &copy
}
