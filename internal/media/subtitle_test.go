package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSubtitleFileConvertsSRTForLibass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.srt")
	content := "1\n00:00:00,000 --> 00:00:02,500\nHello world\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	format, converted, err := ReadSubtitleFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if format != "ass" || !strings.Contains(converted, "[Script Info]") ||
		!strings.Contains(converted, "PlayResX: 1920") || !strings.Contains(converted, "[V4+ Styles]") ||
		!strings.Contains(converted, ",Liberation Sans,64.000,") ||
		!strings.Contains(converted, ",Default,") || !strings.Contains(converted, "Hello world") {
		t.Fatalf("unexpected conversion: format=%q content=%q", format, converted)
	}
}

func TestReadSubtitleFilePreservesASS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.ass")
	content := "[Script Info]\nTitle: Test\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	format, got, err := ReadSubtitleFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if format != "ass" || got != content {
		t.Fatalf("ASS was changed: format=%q content=%q", format, got)
	}
}
