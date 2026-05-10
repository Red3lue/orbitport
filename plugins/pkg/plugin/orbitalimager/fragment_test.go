package orbitalimager

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestIsSupportedExtension(t *testing.T) {
	cases := map[string]bool{
		"foo.png":         true,
		"foo.PNG":         true,
		"path/to/bar.jpg": true,
		"bar.jpeg":        true,
		"bar.gif":         true,
		"bar.webp":        false,
		"bar.bmp":         false,
		"bar":             false,
		"":                false,
	}
	for name, want := range cases {
		if got := IsSupportedExtension(name); got != want {
			t.Errorf("IsSupportedExtension(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestDeriveShipName(t *testing.T) {
	cases := map[string]string{
		"tanker_evergreen-01.png":      "Tanker Evergreen 01",
		"/abs/path/to/9133701.png":     "9133701",
		"./relative/USS-Phare.jpeg":    "USS Phare",
		"":                             "Image",
		"synthetic://placeholder":      "Placeholder",
	}
	for in, want := range cases {
		if got := deriveShipName(in); got != want {
			t.Errorf("deriveShipName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveMockIMO(t *testing.T) {
	if got := deriveMockIMO("9133701.png"); got != 9133701 {
		t.Errorf("numeric stem: got %d, want 9133701", got)
	}
	if got := deriveMockIMO("evergreen.png"); got != 0 {
		t.Errorf("non-numeric stem: got %d, want 0", got)
	}
}

func TestFragmentImage_GridAndMetadata(t *testing.T) {
	// 600x300 image, tile 256 → 3 cols x 2 rows = 6 packets.
	// Edge tiles measure 88 wide and 44 tall.
	const w, h = 600, 300
	const tile = 256

	src := makePNG(t, w, h)
	dir := t.TempDir()

	res, err := FragmentImage(FragmentOptions{
		SourceBytes:    src,
		SourceFilename: "tanker_test-01.png",
		SourceMimeType: "image/png",
		OutputDir:      dir,
		TilePixelSize:  tile,
		Sensor:         "phare-mock-1",
	})
	if err != nil {
		t.Fatalf("FragmentImage: %v", err)
	}

	wantImageDir := filepath.Join(dir, "tanker_test-01")
	if res.ImageDir != wantImageDir {
		t.Errorf("ImageDir = %s, want %s", res.ImageDir, wantImageDir)
	}

	m := res.Metadata
	if m.TileRows != 2 || m.TileCols != 3 || m.PacketCount != 6 {
		t.Errorf("grid: rows=%d cols=%d count=%d, want 2/3/6", m.TileRows, m.TileCols, m.PacketCount)
	}
	if m.ImageWidth != w || m.ImageHeight != h {
		t.Errorf("dims: %dx%d, want %dx%d", m.ImageWidth, m.ImageHeight, w, h)
	}
	if m.ShipName != "Tanker Test 01" {
		t.Errorf("ShipName = %q, want %q", m.ShipName, "Tanker Test 01")
	}
	if m.PacketExtension != ".png" {
		t.Errorf("PacketExtension = %q, want .png", m.PacketExtension)
	}
	if m.SourceExtension != ".png" {
		t.Errorf("SourceExtension = %q, want .png", m.SourceExtension)
	}
	if m.Version != "0.1.0" {
		t.Errorf("Version = %q, want 0.1.0", m.Version)
	}

	// Right-edge tiles (col=2) should be 600-512=88 wide; bottom-edge tiles
	// (row=1) should be 300-256=44 tall.
	for _, p := range m.Packets {
		wantW := tile
		if p.Col == m.TileCols-1 {
			wantW = w - tile*(m.TileCols-1)
		}
		wantH := tile
		if p.Row == m.TileRows-1 {
			wantH = h - tile*(m.TileRows-1)
		}
		if p.Width != wantW || p.Height != wantH {
			t.Errorf("packet idx=%d (r=%d c=%d): %dx%d, want %dx%d",
				p.Index, p.Row, p.Col, p.Width, p.Height, wantW, wantH)
		}
		path := filepath.Join(res.ImageDir, "packets", p.Filename)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("packet file missing: %s (%v)", path, err)
		}
	}

	// metadata.json on disk must round-trip to the same content.
	raw, err := os.ReadFile(res.MetadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var loaded Metadata
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	if loaded.PacketCount != 6 || loaded.TileRows != 2 || loaded.TileCols != 3 {
		t.Errorf("on-disk metadata grid mismatch: %+v", loaded)
	}
}

func TestFragmentImage_Reentrant(t *testing.T) {
	// Re-fragmenting the same source must wipe and rewrite cleanly so a
	// previous run with a different tile size never leaves orphan packets.
	src := makePNG(t, 300, 300)
	dir := t.TempDir()

	if _, err := FragmentImage(FragmentOptions{
		SourceBytes: src, SourceFilename: "ship.png",
		OutputDir: dir, TilePixelSize: 100,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := FragmentImage(FragmentOptions{
		SourceBytes: src, SourceFilename: "ship.png",
		OutputDir: dir, TilePixelSize: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(res.ImageDir, "packets"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != res.Metadata.PacketCount {
		t.Errorf("packets dir has %d files, metadata says %d (orphans from prior run?)",
			len(entries), res.Metadata.PacketCount)
	}
}

func TestFragmentImage_ValidationErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := FragmentImage(FragmentOptions{OutputDir: dir, SourceFilename: "x.png"}); err == nil {
		t.Error("empty bytes: expected error, got nil")
	}
	if _, err := FragmentImage(FragmentOptions{SourceBytes: []byte{1}, OutputDir: dir}); err == nil {
		t.Error("missing filename: expected error, got nil")
	}
	if _, err := FragmentImage(FragmentOptions{SourceBytes: []byte{1}, SourceFilename: "x.png"}); err == nil {
		t.Error("missing OutputDir: expected error, got nil")
	}
	if _, err := FragmentImage(FragmentOptions{
		SourceBytes: []byte("not an image"),
		SourceFilename: "x.png", OutputDir: dir,
	}); err == nil {
		t.Error("undecodable bytes: expected error, got nil")
	}
}

// makePNG returns a wxh PNG with a deterministic gradient pattern.
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x80, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
