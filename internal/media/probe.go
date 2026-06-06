package media

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ffprobeOutput matches the JSON output of ffprobe.
type ffprobeOutput struct {
	Format struct {
		Filename string `json:"filename"`
		Duration string `json:"duration"`
		Size     string `json:"size"`
	} `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeStream struct {
	Index     int    `json:"index"`
	CodecType string `json:"codec_type"`
	CodecName string `json:"codec_name"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Channels  int    `json:"channels,omitempty"`
	Tags      struct {
		Language string `json:"language,omitempty"`
		Title    string `json:"title,omitempty"`
	} `json:"tags"`
}

// Probe analyzes a media file and returns its metadata.
func Probe(ffprobePath, filePath string) (*MediaInfo, error) {
	cmd := exec.Command(ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		filePath,
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe %s: %w", filePath, err)
	}

	var result ffprobeOutput
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	info := &MediaInfo{
		Path:   filePath,
		Format: filepath.Ext(filePath),
		Tracks: make([]TrackInfo, 0, len(result.Streams)),
	}

	// Parse duration
	var duration float64
	fmt.Sscanf(result.Format.Duration, "%f", &duration)
	info.Duration = duration

	// Parse streams
	for _, s := range result.Streams {
		track := TrackInfo{
			Index:    s.Index,
			Codec:    s.CodecName,
			Type:     s.CodecType,
			Width:    s.Width,
			Height:   s.Height,
			Channels: s.Channels,
			Language: s.Tags.Language,
			Title:    s.Tags.Title,
		}
		info.Tracks = append(info.Tracks, track)
	}

	return info, nil
}

// ScanDir scans a directory for media files with allowed extensions.
func ScanDir(dir string, allowedExts []string) ([]MediaInfo, error) {
	var results []MediaInfo

	// Quick file scan first
	cmd := exec.Command("find", dir, "-type", "f")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("scan directory: %w", err)
	}

	files := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, file := range files {
		if file == "" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(file))
		for _, allowed := range allowedExts {
			if ext == allowed {
				info := MediaInfo{
					Path:   file,
					Format: ext,
				}
				results = append(results, info)
				break
			}
		}
	}

	return results, nil
}
