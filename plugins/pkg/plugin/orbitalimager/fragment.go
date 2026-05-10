// Image fragmentation for orbitalimager v0.1.0.
//
// Splits a decodable raster image into a grid of fixed-pixel-size square
// packets and writes them to disk alongside a metadata.json descriptor.
// The on-disk layout is the source of truth that the future RPC surface
// (one endpoint serves metadata.json, another serves an individual packet)
// will read from. Nothing in this file knows or cares about RPC.

package orbitalimager

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/sha3"
)

// SupportedExtensions is the allowlist of source-image extensions the
// fragmenter will accept. Lower-case, leading dot. Add new formats here
// (and import the matching decoder above) to expand support.
var SupportedExtensions = []string{".png", ".jpg", ".jpeg", ".gif"}

// IsSupportedExtension reports whether name's extension is in
// SupportedExtensions. The check is case-insensitive.
func IsSupportedExtension(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, allowed := range SupportedExtensions {
		if ext == allowed {
			return true
		}
	}
	return false
}

// PacketInfo describes a single tile written to disk.
type PacketInfo struct {
	Index            int    `json:"index"`
	Filename         string `json:"filename"`
	Row              int    `json:"row"`
	Col              int    `json:"col"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	HashKeccak256Hex string `json:"hash_keccak256"`
}

// Metadata is the on-disk descriptor written as metadata.json next to the
// packets. The future metadata RPC returns this verbatim.
type Metadata struct {
	Version             string       `json:"version"`
	ImageID             string       `json:"image_id"`
	ShipName            string       `json:"ship_name"`
	IMO                 uint64       `json:"imo"`
	SourceFilename      string       `json:"source_filename"`
	SourceMimeType      string       `json:"source_mime_type"`
	SourceExtension     string       `json:"source_extension"`
	ImageWidth          int          `json:"image_width"`
	ImageHeight         int          `json:"image_height"`
	TilePixelSize       int          `json:"tile_pixel_size"`
	TileRows            int          `json:"tile_rows"`
	TileCols            int          `json:"tile_cols"`
	PacketCount         int          `json:"packet_count"`
	PacketExtension     string       `json:"packet_extension"`
	FullImageHashKecHex string       `json:"full_image_hash_keccak256"`
	CreatedAtUnix       int64        `json:"created_at_unix"`
	Sensor              string       `json:"sensor"`
	Packets             []PacketInfo `json:"packets"`
}

// FragmentOptions controls a single FragmentImage call.
type FragmentOptions struct {
	// SourceBytes is the encoded image (PNG/JPEG/GIF). Required.
	SourceBytes []byte
	// SourceFilename is the original filename, used to derive ImageID and
	// the mocked ship name. Extension drives the IsSupportedExtension gate
	// upstream of this call.
	SourceFilename string
	// SourceMimeType e.g. "image/png". Stored in metadata; not validated.
	SourceMimeType string
	// OutputDir is the parent directory under which a per-image folder is
	// created. Required.
	OutputDir string
	// TilePixelSize is the side length of each square packet, in pixels.
	// Defaults to 256 if zero.
	TilePixelSize int
	// Sensor is copied into metadata as a free-form identifier.
	Sensor string
}

// FragmentResult is what the caller gets back after a successful fragment.
type FragmentResult struct {
	ImageDir     string
	MetadataPath string
	Metadata     *Metadata
}

// FragmentImage decodes opts.SourceBytes, slices it into square tiles of
// opts.TilePixelSize pixels, writes each tile as a PNG into
// <OutputDir>/<image_id>/packets/, and writes metadata.json next to it.
//
// Tiles on the right and bottom edges may be smaller than TilePixelSize
// if the image dimensions are not a clean multiple — their actual width
// and height are recorded in PacketInfo so a recomposer can stitch them
// back without ambiguity.
//
// The output directory is wiped and recreated on each call so that a
// re-fragment is deterministic and never leaves orphaned packets from a
// previous run with a different tile size.
func FragmentImage(opts FragmentOptions) (*FragmentResult, error) {
	if len(opts.SourceBytes) == 0 {
		return nil, fmt.Errorf("fragment: SourceBytes is empty")
	}
	if opts.SourceFilename == "" {
		return nil, fmt.Errorf("fragment: SourceFilename is required")
	}
	if opts.OutputDir == "" {
		return nil, fmt.Errorf("fragment: OutputDir is required")
	}
	tile := opts.TilePixelSize
	if tile <= 0 {
		tile = 256
	}

	img, _, err := image.Decode(bytes.NewReader(opts.SourceBytes))
	if err != nil {
		return nil, fmt.Errorf("fragment: decode source image: %w", err)
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("fragment: zero-sized image (%dx%d)", w, h)
	}

	imageID := deriveImageID(opts.SourceFilename)
	imageDir := filepath.Join(opts.OutputDir, imageID)
	packetsDir := filepath.Join(imageDir, "packets")

	if err := os.RemoveAll(imageDir); err != nil {
		return nil, fmt.Errorf("fragment: clear %s: %w", imageDir, err)
	}
	if err := os.MkdirAll(packetsDir, 0o755); err != nil {
		return nil, fmt.Errorf("fragment: mkdir %s: %w", packetsDir, err)
	}

	cols := ceilDiv(w, tile)
	rows := ceilDiv(h, tile)

	packets := make([]PacketInfo, 0, rows*cols)
	idx := 0
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			x0 := c * tile
			y0 := r * tile
			x1 := minInt(x0+tile, w)
			y1 := minInt(y0+tile, h)

			rect := image.Rect(0, 0, x1-x0, y1-y0)
			tileImg := image.NewRGBA(rect)
			draw.Draw(tileImg, rect, img,
				image.Point{X: bounds.Min.X + x0, Y: bounds.Min.Y + y0},
				draw.Src)

			filename := fmt.Sprintf("%04d.png", idx)
			fullPath := filepath.Join(packetsDir, filename)
			encoded, err := encodePNG(tileImg)
			if err != nil {
				return nil, fmt.Errorf("fragment: encode packet %d: %w", idx, err)
			}
			if err := os.WriteFile(fullPath, encoded, 0o644); err != nil {
				return nil, fmt.Errorf("fragment: write %s: %w", fullPath, err)
			}

			packets = append(packets, PacketInfo{
				Index:            idx,
				Filename:         filename,
				Row:              r,
				Col:              c,
				Width:            rect.Dx(),
				Height:           rect.Dy(),
				HashKeccak256Hex: hexKeccak(encoded),
			})
			idx++
		}
	}

	meta := &Metadata{
		Version:             "0.1.0",
		ImageID:             imageID,
		ShipName:            deriveShipName(opts.SourceFilename),
		IMO:                 deriveMockIMO(opts.SourceFilename),
		SourceFilename:      filepath.Base(opts.SourceFilename),
		SourceMimeType:      opts.SourceMimeType,
		SourceExtension:     strings.ToLower(filepath.Ext(opts.SourceFilename)),
		ImageWidth:          w,
		ImageHeight:         h,
		TilePixelSize:       tile,
		TileRows:            rows,
		TileCols:            cols,
		PacketCount:         len(packets),
		PacketExtension:     ".png",
		FullImageHashKecHex: hexKeccak(opts.SourceBytes),
		CreatedAtUnix:       time.Now().Unix(),
		Sensor:              opts.Sensor,
		Packets:             packets,
	}

	metaPath := filepath.Join(imageDir, "metadata.json")
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("fragment: marshal metadata: %w", err)
	}
	if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
		return nil, fmt.Errorf("fragment: write metadata: %w", err)
	}

	return &FragmentResult{
		ImageDir:     imageDir,
		MetadataPath: metaPath,
		Metadata:     meta,
	}, nil
}

// deriveImageID returns a filesystem-safe identifier from a source path.
// We strip the directory and extension, then replace any character that
// isn't alnum/dash/underscore with '-' so the result is safe to use as a
// directory name on every host filesystem we care about.
func deriveImageID(sourcePath string) string {
	base := filepath.Base(sourcePath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	stem = nonSafeIDChars.ReplaceAllString(stem, "-")
	stem = strings.Trim(stem, "-")
	if stem == "" {
		return "image"
	}
	return stem
}

// deriveShipName returns the human-readable ship name. v0.1.0 mocks this
// from the source filename: the basename minus extension, with separators
// normalised to spaces and title-cased so "tanker_evergreen-01.png"
// renders as "Tanker Evergreen 01".
func deriveShipName(sourcePath string) string {
	id := deriveImageID(sourcePath)
	spaced := strings.ReplaceAll(id, "-", " ")
	spaced = strings.ReplaceAll(spaced, "_", " ")
	parts := strings.Fields(spaced)
	for i, p := range parts {
		if len(p) == 0 {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// deriveMockIMO returns an IMO number derived from the filename. If the
// stem parses as a uint, we use it directly (so a fixture named
// "9133701.png" yields imo=9133701, matching the orchestrator default).
// Otherwise zero — real IMO arrives via the request, not the fixture.
func deriveMockIMO(sourcePath string) uint64 {
	stem := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
	if n, err := strconv.ParseUint(stem, 10, 64); err == nil {
		return n
	}
	return 0
}

var nonSafeIDChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func encodePNG(img image.Image) ([]byte, error) {
	var enc png.Encoder
	enc.CompressionLevel = png.DefaultCompression
	var buf bytes.Buffer
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func hexKeccak(b []byte) string {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
