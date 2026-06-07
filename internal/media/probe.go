package media

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
)

// TrackInfo describes a media track from FFprobe.
type TrackInfo struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Type     string `json:"type"` // "video" | "audio" | "subtitle"
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Channels int    `json:"channels,omitempty"`
}

// MediaInfo holds metadata about a media file.
type MediaInfo struct {
	Path     string      `json:"path"`
	Duration float64     `json:"duration"`
	Format   string      `json:"format"`
	Size     int64       `json:"size"`
	Tracks   []TrackInfo `json:"tracks"`
}

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

	extSet := make(map[string]bool, len(allowedExts))
	for _, ext := range allowedExts {
		extSet[strings.ToLower(ext)] = true
	}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if extSet[ext] {
			results = append(results, MediaInfo{
				Path:   path,
				Format: ext,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan directory %s: %w", dir, err)
	}

	return results, nil
}
