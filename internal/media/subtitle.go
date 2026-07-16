package media

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	astisub "github.com/asticode/go-astisub"
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

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if ext != "srt" && ext != "vtt" {
		return ext, string(data), nil
	}

	// JASSUB renders libass formats. Parse text subtitles with astisub and
	// serialize them as SSA so SRT and WebVTT work without an FFmpeg install.
	subtitles, err := astisub.OpenFile(path)
	if err != nil {
		return "", "", err
	}
	prepareForLibass(subtitles)
	var converted bytes.Buffer
	if err := subtitles.WriteToSSA(&converted); err != nil {
		return "", "", err
	}
	return "ass", converted.String(), nil
}

func prepareForLibass(subtitles *astisub.Subtitles) {
	playResX, playResY := 1920, 1080
	if subtitles.Metadata == nil {
		subtitles.Metadata = &astisub.Metadata{}
	}
	subtitles.Metadata.SSAScriptType = "v4.00+"
	subtitles.Metadata.SSAPlayResX = &playResX
	subtitles.Metadata.SSAPlayResY = &playResY

	fontSize, outline, shadow := 64.0, 2.0, 1.0
	borderStyle, alignment, margin, encoding := 1, 2, 24, 1
	bold, italic := false, false
	defaultStyle := &astisub.Style{
		ID: "Default",
		InlineStyle: &astisub.StyleAttributes{
			SSAFontName: "Liberation Sans", SSAFontSize: &fontSize,
			SSAPrimaryColour: astisub.ColorWhite,
			SSAOutlineColour: astisub.ColorBlack,
			SSABackColour:    astisub.ColorBlack,
			SSABold:          &bold, SSAItalic: &italic,
			SSABorderStyle: &borderStyle, SSAOutline: &outline, SSAShadow: &shadow,
			SSAAlignment:  &alignment,
			SSAMarginLeft: &margin, SSAMarginRight: &margin, SSAMarginVertical: &margin,
			SSAEncoding: &encoding,
		},
	}
	if subtitles.Styles == nil {
		subtitles.Styles = make(map[string]*astisub.Style)
	}
	subtitles.Styles[defaultStyle.ID] = defaultStyle
	for _, item := range subtitles.Items {
		item.Style = defaultStyle
		for lineIndex := range item.Lines {
			for itemIndex := range item.Lines[lineIndex].Items {
				lineItem := &item.Lines[lineIndex].Items[itemIndex]
				if lineItem.InlineStyle == nil {
					continue
				}
				var tags strings.Builder
				if lineItem.InlineStyle.SRTBold || lineItem.InlineStyle.WebVTTBold {
					tags.WriteString(`\b1`)
				}
				if lineItem.InlineStyle.SRTItalics || lineItem.InlineStyle.WebVTTItalics {
					tags.WriteString(`\i1`)
				}
				if lineItem.InlineStyle.SRTUnderline || lineItem.InlineStyle.WebVTTUnderline {
					tags.WriteString(`\u1`)
				}
				if tags.Len() > 0 {
					lineItem.InlineStyle.SSAEffect = "{" + tags.String() + "}"
				}
			}
		}
	}
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
