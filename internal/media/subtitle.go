package media

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// SubtitleInfo describes a detected subtitle.
type SubtitleInfo struct {
	Path     string `json:"path"`
	Format   string `json:"format"` // "srt", "ass", "ssa", "vtt"
	Language string `json:"language,omitempty"`
	Index    int    `json:"index"` // 0-based index in subtitle list
}

// ExtractSubtitles extracts embedded subtitles or finds external subtitle files.
func ExtractSubtitles(ffmpegPath, videoPath string) ([]SubtitleInfo, error) {
	var subs []SubtitleInfo

	// Look for external subtitle files
	dir := filepath.Dir(videoPath)
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))

	for _, ext := range []string{".srt", ".ass", ".ssa", ".vtt"} {
		// Same name as video: movie.srt
		candidate := filepath.Join(dir, base+ext)
		if _, err := os.Stat(candidate); err == nil {
			subs = append(subs, SubtitleInfo{
				Path:   candidate,
				Format: strings.TrimPrefix(ext, "."),
				Index:  len(subs),
			})
		}

		// With language suffix: movie.chi.srt, movie.en.ass
		pattern := filepath.Join(dir, base+".*"+ext)
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			if _, err := os.Stat(m); err == nil {
				subs = append(subs, SubtitleInfo{
					Path:   m,
					Format: strings.TrimPrefix(ext, "."),
					Index:  len(subs),
				})
			}
		}
	}

	return subs, nil
}

// ReadSubtitleFile reads the content of a subtitle file.
func ReadSubtitleFile(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}

	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	return ext, string(data), nil
}

// ExtractSubtitleTrack uses FFmpeg to extract an embedded subtitle track.
func ExtractSubtitleTrack(ffmpegPath, videoPath string, trackIndex int) (string, string, error) {
	outputFile := filepath.Join(os.TempDir(), "syncwatch_subtitle_"+filepath.Base(videoPath))

	// Determine format from track info
	// Default to .ass for output since FFmpeg can convert to it
	outputFile += ".ass"

	cmd := exec.Command(ffmpegPath,
		"-v", "quiet",
		"-i", videoPath,
		"-map", "0:s:"+strconv.Itoa(trackIndex),
		"-c:s", "copy",
		"-f", "ass",
		outputFile,
	)

	if err := cmd.Run(); err != nil {
		// Try with text copy
		cmd = exec.Command(ffmpegPath,
			"-v", "quiet",
			"-i", videoPath,
			"-map", "0:s:"+strconv.Itoa(trackIndex),
			outputFile,
		)
		if err := cmd.Run(); err != nil {
			return "", "", err
		}
	}

	defer os.Remove(outputFile)

	data, err := os.ReadFile(outputFile)
	if err != nil {
		return "", "", err
	}

	return "ass", string(data), nil
}
