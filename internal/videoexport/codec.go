package videoexport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type mediaInfo struct {
	DurationMS, Frames                       int64
	Width, Height, SampleRate, AudioChannels int
	Audio                                    bool
}
type streamInfo struct {
	Type        string `json:"codec_type"`
	Codec       string `json:"codec_name"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	FrameCount  string `json:"nb_frames"`
	Rate        string `json:"avg_frame_rate"`
	Duration    string `json:"duration"`
	SampleRate  string `json:"sample_rate"`
	Channels    int    `json:"channels"`
	Disposition struct {
		Attached int `json:"attached_pic"`
	} `json:"disposition"`
}

var movArguments = []string{"-format_whitelist", "mov", "-protocol_whitelist", "file", "-codec_whitelist", "h264,aac",
	"-max_streams", "2", "-max_pixels", strconv.Itoa(maxPixels), "-probesize", "4000000", "-analyzeduration", "4000000",
	"-enable_drefs", "0", "-use_absolute_path", "0", "-f", "mov"}

type limitedBuffer struct {
	b     bytes.Buffer
	limit int
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.b.Len() {
		return 0, ErrCodec
	}
	return w.b.Write(p)
}

func (e *Executor) command(ctx context.Context, dir, tool string, args []string, limit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir = dir
	cmd.Env = []string{"LC_ALL=C", "AV_LOG_FORCE_NOCOLOR=1", "TEMP=" + dir, "TMP=" + dir}
	if root := os.Getenv("SystemRoot"); root != "" {
		cmd.Env = append(cmd.Env, "SystemRoot="+root)
	}
	output := &limitedBuffer{limit: limit}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if err := runControlled(cmd); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == ErrUnavailable {
			return nil, err
		}
		return nil, ErrCodec
	}
	return output.b.Bytes(), nil
}

func (e *Executor) inspect(ctx context.Context, dir, name string) (mediaInfo, error) {
	args := []string{"-v", "error", "-max_alloc", "67108864"}
	args = append(args, movArguments...)
	args = append(args, "-show_entries", "format=format_name:format_tags=major_brand:stream=codec_type,codec_name,width,height,nb_frames,avg_frame_rate,duration,sample_rate,channels:stream_disposition=attached_pic", "-of", "json", name)
	data, err := e.command(ctx, dir, e.cfg.FFprobePath, args, 32<<10)
	if err != nil {
		return mediaInfo{}, err
	}
	var raw struct {
		Streams []streamInfo `json:"streams"`
		Format  struct {
			Name string `json:"format_name"`
			Tags struct {
				Brand string `json:"major_brand"`
			} `json:"tags"`
		} `json:"format"`
	}
	if json.Unmarshal(data, &raw) != nil || !strings.Contains(raw.Format.Name, "mp4") {
		return mediaInfo{}, ErrCodec
	}
	switch raw.Format.Tags.Brand {
	case "isom", "iso2", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "M4V ":
	default:
		return mediaInfo{}, ErrCodec
	}
	var info mediaInfo
	for _, s := range raw.Streams {
		switch s.Type {
		case "video":
			if info.Width != 0 || s.Codec != "h264" || s.Disposition.Attached != 0 || s.Width < 1 || s.Height < 1 || s.Width > 4096 || s.Height > 4096 || s.Width > maxPixels/s.Height {
				return info, ErrCodec
			}
			frames, er := strconv.ParseInt(s.FrameCount, 10, 64)
			sec, ed := strconv.ParseFloat(s.Duration, 64)
			num, den, ok := strings.Cut(s.Rate, "/")
			n, en := strconv.ParseUint(num, 10, 32)
			d, erate := strconv.ParseUint(den, 10, 32)
			if er != nil || frames < 1 || frames > 18_000 || ed != nil || math.IsNaN(sec) || math.IsInf(sec, 0) || sec <= 0 || sec > 300 ||
				!ok || en != nil || erate != nil || n == 0 || d == 0 || n > 60*d {
				return info, ErrCodec
			}
			info.Width, info.Height = s.Width, s.Height
			info.Frames = frames
			info.DurationMS = int64(math.Round(sec * 1000))
		case "audio":
			rate, er := strconv.Atoi(s.SampleRate)
			if info.Audio || s.Codec != "aac" || er != nil || rate < 8000 || rate > 96000 || s.Channels < 1 || s.Channels > 2 {
				return info, ErrCodec
			}
			info.Audio, info.SampleRate, info.AudioChannels = true, rate, s.Channels
		default:
			return info, ErrCodec
		}
	}
	if info.Width == 0 {
		return info, ErrCodec
	}
	return info, nil
}

func seconds(ms int64) string { return fmt.Sprintf("%d.%03d", ms/1000, ms%1000) }

func renderArgs(clips []Clip, infos []mediaInfo) []string {
	args := []string{"-nostdin", "-v", "error", "-xerror", "-n", "-max_alloc", "67108864", "-filter_complex_threads", "1", "-filter_buffered_frames", "128"}
	var graph, concat strings.Builder
	var elapsed int64
	for i, c := range clips {
		args = append(args, movArguments...)
		args = append(args, "-threads", "1", "-err_detect", "explode", "-ss", seconds(c.StartMS), "-t", seconds(c.EndMS-c.StartMS), "-i", inputName(i))
		previous := frameCount(elapsed)
		elapsed += c.EndMS - c.StartMS
		frames := frameCount(elapsed) - previous
		fmt.Fprintf(&graph, "[%d:v:0]setpts=PTS-STARTPTS,fps=24:start_time=0,scale=1280:720:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=1280:720:(ow-iw)/2:(oh-ih)/2,setsar=1,format=yuv420p,tpad=stop_mode=clone:stop_duration=0.05,trim=end_frame=%d,setpts=N/(24*TB)[v%d];", i, frames, i)
		if infos[i].Audio {
			fmt.Fprintf(&graph, "[%d:a:0]aresample=48000:async=1:first_pts=0,aformat=channel_layouts=stereo,apad=whole_len=%d,", i, frames*2000)
		} else {
			graph.WriteString("anullsrc=r=48000:cl=stereo,")
		}
		fmt.Fprintf(&graph, "atrim=end_sample=%d,asetpts=N/SR/TB[a%d];", frames*2000, i)
		fmt.Fprintf(&concat, "[v%d][a%d]", i, i)
	}
	fmt.Fprintf(&graph, "%sconcat=n=%d:v=1:a=1[v][a]", concat.String(), len(clips))
	return append(args, "-filter_complex", graph.String(), "-map", "[v]", "-map", "[a]", "-map_metadata", "-1", "-map_chapters", "-1", "-sn", "-dn",
		"-c:v", "libx264", "-threads:v", "1", "-preset", "veryfast", "-b:v", "2000k", "-maxrate:v", "3000k", "-bufsize:v", "3000k", "-pix_fmt", "yuv420p", "-r", "24",
		"-c:a", "aac", "-threads:a", "1", "-ac", "2", "-ar", "48000", "-b:a", "128k", "-t", seconds((frameCount(elapsed)*1000+fps/2)/fps),
		"-fs", strconv.Itoa(MaxOutputBytes), "-movflags", "+faststart", "-f", "mp4", "export.mp4")
}
