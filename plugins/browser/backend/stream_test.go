package main

import (
	"bytes"
	"io"
	"testing"
)

type singleByteReader struct{ data []byte }

func (r *singleByteReader) Read(buffer []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	buffer[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func TestAnnexBSplitterEmitsWholeAccessUnits(t *testing.T) {
	first := []byte{0, 0, 0, 1, 0x09, 0x10, 0, 0, 0, 1, 0x67, 0x42, 0, 0, 0, 1, 0x65, 0x88}
	second := []byte{0, 0, 1, 0x09, 0x10, 0, 0, 1, 0x41, 0x9a}
	input := append(append([]byte(nil), first...), second...)
	var frames [][]byte
	splitter := annexBSplitter{emit: func(data []byte) error {
		frames = append(frames, append([]byte(nil), data...))
		return nil
	}}
	if err := splitter.read(&singleByteReader{data: input}); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || !bytes.Equal(frames[0], first) || !bytes.Equal(frames[1], second) {
		t.Fatalf("frames = %v, want exact two access units", frames)
	}
}

func TestViewportAndVideoDefaults(t *testing.T) {
	viewport := makeViewport(0, 0, 0, 0)
	if viewport.CSSWidth != defaultPageWidth || viewport.CSSHeight != defaultPageHeight || viewport.RenderScale != 1 {
		t.Fatalf("viewport defaults = %+v", viewport)
	}
	video := normalizeVideo(VideoSettings{})
	if video.FPS != defaultFPS || video.BitrateKbps != defaultBitrate {
		t.Fatalf("video defaults = %+v", video)
	}
}
